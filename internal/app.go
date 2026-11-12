package app

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/cloud-self-service-golib/logging"
	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
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
	case "memory":
		store = storage.NewMemoryStore(log)
	default:
		// Unreachable: loadAppConfiguration refuses any other value.
		log.Fatalw("unknown DB_TYPE", "db_type", cfg.DBType)
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
		sources, err := store.ListSources(ctx)
		if err != nil {
			return nil, err
		}
		names := map[uuid.UUID]string{}
		for _, src := range sources {
			names[src.ID] = src.Name
		}
		for i := range groups {
			groups[i].Relations = relations[groups[i].ID]
			if groups[i].SourceID != nil {
				groups[i].SourceName = names[*groups[i].SourceID]
			}
		}
		return groups, nil
	}, log)

	// The local source: every group maintained here belongs to it.
	localID, err := setupLocalSource(context.Background(), cfg.LocalSourceName, store, log)
	if err != nil {
		log.Fatalw("local source", zap.Error(err))
	}

	// Service layer.
	timeout := time.Duration(cfg.ServiceTimeoutSeconds) * time.Second
	// Validated in loadAppConfiguration already.
	relations, _ := common.NewRelations(cfg.GroupRelations)
	groupSvc := groupmgmt.NewService(store, groupCache, relations, localID, timeout, log)

	// Groups of no source — created through the API before there was a local source, or left by a deleted source — and the sources configured for adoption pass to the local source. Both are no-ops once done.
	if err := adoptIntoLocal(context.Background(), cfg.AdoptSources, store, groupSvc, log); err != nil {
		log.Fatalw("adopting groups into the local source", zap.Error(err))
	}

	if err := groupCache.Refresh(context.Background()); err != nil {
		log.Warnw("initial group cache load failed", zap.Error(err))
	} else {
		log.Infow("group search cache loaded", "groups", groupCache.Size())
	}
	groupCache.StartAutoRefresh(context.Background(), time.Duration(cfg.GroupCacheRefreshSeconds)*time.Second)

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

	// People maintaining groups through the self-service UI.
	var verifier webserver.TokenVerifier
	if cfg.OIDC.IssuerURL != "" {
		v, err := oidcauth.New(oidcauth.Config{IssuerURL: cfg.OIDC.IssuerURL, ClientID: cfg.OIDC.ClientID, JWKSURL: cfg.OIDC.JWKSURL}, log)
		if err != nil {
			log.Fatalw("oidc verifier", zap.Error(err))
		}
		verifier = v
		log.Infow("group maintenance for signed-in people enabled", "issuer", cfg.OIDC.IssuerURL, "admin_tokens", cfg.AdminTokens)
	}

	// HTTP router.
	router, err := webserver.SetupRouter(webserver.SetupConfig{
		DevMode:            cfg.DevMode,
		Log:                log,
		APITokens:          cfg.APITokens,
		APIWriteTokens:     cfg.APIWriteTokens,
		SourceUploadTokens: cfg.SourceUploadTokens,
		GroupSvc:           groupSvc,
		Store:              store,
		SyncEngine:         engine,
		Scheduler:          scheduler,
		MaxResponseLimit:   cfg.MaxResponseLimit,
		CORSAllowedOrigins: cfg.CORSAllowedOrigins,
		TrustedProxies:     cfg.TrustedProxies,
		Verifier:           verifier,
		AdminTokens:        cfg.AdminTokens,
		DevAuthUser:        cfg.DevAuthUser,
		LocalSourceName:    cfg.LocalSourceName,
	})
	if err != nil {
		log.Fatalw("http router", zap.Error(err))
	}

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

	if err := syncp.ValidateSchedule(cfg.SyncSchedule); err != nil {
		return uuid.Nil, fmt.Errorf("KEYCLOAK_SYNC_SCHEDULE: %w", err)
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
	// The groups the mapping names are reserved for this source, so nobody creates one by hand before the first user of a new location signs in.
	reserved := mapping.Groups()
	if src == nil {
		src = &common.Source{ID: uuid.New(), Name: "keycloak", Type: common.SourceTypeKeycloak, Schedule: cfg.SyncSchedule, ReservedNames: reserved}
		if err := store.CreateSource(ctx, src); err != nil {
			return uuid.Nil, fmt.Errorf("create keycloak source: %w", err)
		}
		log.Infow("created keycloak source", "source_id", src.ID, "schedule", cfg.SyncSchedule)
	} else if src.Schedule != cfg.SyncSchedule || !slices.Equal(src.ReservedNames, reserved) {
		src.Schedule, src.ReservedNames = cfg.SyncSchedule, reserved
		if err := store.UpdateSource(ctx, src); err != nil {
			return uuid.Nil, fmt.Errorf("update keycloak source: %w", err)
		}
	}

	// The groups this source will fill must not belong to anyone else; caught
	// here, a collision stops the deployment instead of every sync.
	if err := syncp.CheckOwnership(ctx, store, src.ID, reserved); err != nil {
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

// setupLocalSource makes sure the one local source exists under the configured name and returns its id.
func setupLocalSource(ctx context.Context, name string, store storage.Store, log *zap.SugaredLogger) (uuid.UUID, error) {
	sources, err := store.ListSources(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("list sources: %w", err)
	}
	for i := range sources {
		src := &sources[i]
		if src.Type != common.SourceTypeLocal {
			continue
		}
		if src.Name != name {
			src.Name = name
			if err := store.UpdateSource(ctx, src); err != nil {
				return uuid.Nil, fmt.Errorf("rename local source: %w", err)
			}
		}
		return src.ID, nil
	}
	src := &common.Source{ID: uuid.New(), Name: name, Type: common.SourceTypeLocal}
	if err := store.CreateSource(ctx, src); err != nil {
		return uuid.Nil, fmt.Errorf("create local source: %w", err)
	}
	log.Infow("created local source", "source_id", src.ID, "name", name)
	return src.ID, nil
}

// systemCaller is the actor recorded for what the service does by itself at startup.
var systemCaller = groupmgmt.Caller{Actor: "system", Admin: true}

// adoptIntoLocal hands the groups of no source, and every group of the sources named in adopt, to the local source; each adopted source is deleted afterwards. A name that matches no source is assumed to be adopted already.
func adoptIntoLocal(ctx context.Context, adopt []string, store storage.Store, svc *groupmgmt.Service, log *zap.SugaredLogger) error {
	if ids, err := svc.Adopt(ctx, systemCaller, nil, "belonged to no source"); err != nil {
		return err
	} else if len(ids) > 0 {
		log.Infow("groups of no source passed to the local source", "groups", len(ids))
	}
	if len(adopt) == 0 {
		return nil
	}
	sources, err := store.ListSources(ctx)
	if err != nil {
		return err
	}
	for _, name := range adopt {
		i := slices.IndexFunc(sources, func(s common.Source) bool { return s.Name == name })
		if i < 0 {
			log.Infow("source to adopt not found, adopted already", "source", name)
			continue
		}
		src := sources[i]
		if src.Type != common.SourceTypeCSV && src.Type != common.SourceTypeLDIF {
			return fmt.Errorf("source %q is of type %s; only csv and ldif sources can be adopted", name, src.Type)
		}
		ids, err := svc.Adopt(ctx, systemCaller, &src.ID, fmt.Sprintf("taken over from source %q", src.Name))
		if err != nil {
			return fmt.Errorf("adopt source %q: %w", name, err)
		}
		if err := store.DeleteSource(ctx, src.ID); err != nil {
			return fmt.Errorf("delete adopted source %q: %w", name, err)
		}
		log.Infow("source adopted into the local source and deleted", "source", name, "groups", len(ids))
	}
	return nil
}
