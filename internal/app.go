package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/cloud-self-service-golib/logging"
	"github.com/pfisterer/role-provider-service/internal/catalog"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/keycloak"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
	"github.com/pfisterer/role-provider-service/internal/webserver"
	"go.uber.org/zap"
)

// RunApplication is the main entry point wiring all components together.
func RunApplication() {
	cfg, err := loadAppConfiguration()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	_, log := logging.Init(cfg.DevMode)
	defer log.Sync() //nolint:errcheck
	log.Info("Starting role-provider-service")

	// Storage.
	var store storage.Store
	switch cfg.DBType {
	case "postgres":
		store, err = storage.NewPostgresStore(cfg.DBConnectionString, log)
		if err != nil {
			log.Fatalw("failed to initialize postgres storage", zap.Error(err))
		}
	default:
		if cfg.DBType != "memory" {
			log.Warnw("unknown DB_TYPE, falling back to memory store", "db_type", cfg.DBType)
		}
		store = storage.NewMemoryStore(log)
	}

	if cfg.DBAddMockData {
		if err := storage.SeedMockData(context.Background(), store, log); err != nil {
			log.Fatalw("failed to seed mock data", zap.Error(err))
		}
	}

	// In-memory group search cache. The store stays the source of truth; the
	// cache serves group search (type-ahead) without a store round-trip per
	// request. Populated now, on a background ticker, and after each sync.
	groupCache := catalog.New(func(ctx context.Context) ([]common.Group, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		groups, err := store.ListGroups(ctx, "", nil, 0)
		if err != nil {
			return nil, err
		}
		relations, err := store.GroupRelations(ctx)
		if err != nil {
			return nil, err
		}
		for i := range groups {
			groups[i].Relations = relations[groups[i].ID]
		}
		return groups, nil
	}, log)
	if err := groupCache.Refresh(context.Background()); err != nil {
		log.Warnw("initial group cache load failed", zap.Error(err))
	} else {
		log.Infow("group search cache loaded", "groups", groupCache.Size())
	}
	groupCache.StartAutoRefresh(context.Background(), time.Duration(cfg.GroupCacheRefreshSeconds)*time.Second)

	// Service layer.
	timeout := time.Duration(cfg.ServiceTimeoutSeconds) * time.Second
	// Validated in loadAppConfiguration already.
	relations, _ := common.NewRelations(cfg.GroupRelations)
	groupSvc := groupmgmt.NewService(store, groupCache, relations, timeout, log)

	// Sync engine + scheduler.
	engine := syncp.NewEngine(store, relations, log)
	engine.SetAfterSync(func(ctx context.Context) {
		if err := groupCache.Refresh(ctx); err != nil {
			log.Warnw("group cache refresh after sync failed", zap.Error(err))
		}
	})
	scheduler := syncp.NewScheduler(engine, store, log)

	// Keycloak source: created from the configuration, so the environment
	// alone decides whether it exists and how often it runs.
	var keycloakSourceID *uuid.UUID
	if cfg.Keycloak.RealmURL != "" {
		id, err := setupKeycloakSource(context.Background(), cfg.Keycloak, relations, store, engine, groupSvc, log)
		if err != nil {
			log.Fatalw("keycloak source", zap.Error(err))
		}
		keycloakSourceID = &id
	}

	if err := scheduler.Start(context.Background()); err != nil {
		log.Warnw("failed to start sync scheduler", zap.Error(err))
	}
	defer scheduler.Stop()

	// The first read right away rather than at the next scheduled minute — in
	// the background, because Keycloak being slow must not delay the start.
	if keycloakSourceID != nil {
		go func() {
			if err := engine.RunSync(context.Background(), *keycloakSourceID, nil); err != nil {
				log.Errorw("initial keycloak sync failed", zap.Error(err))
			}
		}()
	}

	// HTTP router.
	router := webserver.SetupRouter(webserver.SetupConfig{
		DevMode:            cfg.DevMode,
		Log:                log,
		APITokens:          cfg.APITokens,
		APIWriteTokens:     cfg.APIWriteTokens,
		GroupSvc:           groupSvc,
		Store:              store,
		SyncEngine:         engine,
		Scheduler:          scheduler,
		MaxResponseLimit:   cfg.MaxResponseLimit,
		CORSAllowedOrigins: cfg.CORSAllowedOrigins,
	})

	log.Infow("Listening", "bind", cfg.GinBindString)
	if err := router.Run(cfg.GinBindString); err != nil {
		log.Fatalw("server stopped", zap.Error(err))
	}
}

// setupKeycloakSource connects to Keycloak, checks the mapping against the
// configured relations, and makes sure the one keycloak source exists with the
// configured schedule. Returns its id.
func setupKeycloakSource(ctx context.Context, cfg KeycloakConfig, relations common.Relations, store storage.Store,
	engine *syncp.Engine, groupSvc *groupmgmt.Service, log *zap.SugaredLogger) (uuid.UUID, error) {
	client, err := keycloak.NewClient(cfg.RealmURL, cfg.ClientID, cfg.ClientSecret)
	if err != nil {
		return uuid.Nil, err
	}
	mapping, err := keycloak.ParseMapping(cfg.Mapping, relations)
	if err != nil {
		return uuid.Nil, err
	}

	sources, err := store.ListSources(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list sources: %w", err)
	}
	var src *common.Source
	for i := range sources {
		if sources[i].Type == common.SourceTypeKeycloak {
			src = &sources[i]
			break
		}
	}
	if src == nil {
		src = &common.Source{ID: uuid.New(), Name: "keycloak", Type: common.SourceTypeKeycloak, Schedule: cfg.SyncSchedule}
		if err := store.CreateSource(ctx, src); err != nil {
			return uuid.Nil, fmt.Errorf("create keycloak source: %w", err)
		}
		log.Infow("created keycloak source", "source_id", src.ID, "schedule", cfg.SyncSchedule)
	} else if src.Schedule != cfg.SyncSchedule {
		if err := store.UpdateSource(ctx, src.ID, src.Name, cfg.SyncSchedule, "", "", ""); err != nil {
			return uuid.Nil, fmt.Errorf("update keycloak source schedule: %w", err)
		}
	}

	// The groups this source will fill must not belong to anyone else; caught
	// here, a collision stops the deployment instead of every sync.
	if err := syncp.CheckOwnership(ctx, store, src.ID, mapping.Groups()); err != nil {
		return uuid.Nil, err
	}

	kc := keycloak.NewSource(client, mapping, store, log)
	kc.SetSourceID(src.ID)
	engine.SetKeycloak(kc.Fetch)
	groupSvc.SetUserLookup(kc.EnsureUser)
	log.Infow("keycloak source configured", "source_id", src.ID, "realm", cfg.RealmURL,
		"locations", len(mapping.Locations), "roles", len(mapping.Roles))
	return src.ID, nil
}
