package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/pfisterer/role-provider-service/internal/catalog"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/helper"
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

	_, log := helper.InitLogger(cfg.DevMode)
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
		return store.ListGroups(ctx, "", nil, 0)
	}, log)
	if err := groupCache.Refresh(context.Background()); err != nil {
		log.Warnw("initial group cache load failed", zap.Error(err))
	} else {
		log.Infow("group search cache loaded", "groups", groupCache.Size())
	}
	groupCache.StartAutoRefresh(context.Background(), time.Duration(cfg.GroupCacheRefreshSeconds)*time.Second)

	// Service layer.
	timeout := time.Duration(cfg.ServiceTimeoutSeconds) * time.Second
	groupSvc := groupmgmt.NewService(store, groupCache, timeout, log)

	// Sync engine + scheduler.
	engine := syncp.NewEngine(store, log)
	engine.SetAfterSync(func(ctx context.Context) {
		if err := groupCache.Refresh(ctx); err != nil {
			log.Warnw("group cache refresh after sync failed", zap.Error(err))
		}
	})
	scheduler := syncp.NewScheduler(engine, store, log)
	if err := scheduler.Start(context.Background()); err != nil {
		log.Warnw("failed to start sync scheduler", zap.Error(err))
	}
	defer scheduler.Stop()

	// HTTP router.
	router := webserver.SetupRouter(webserver.SetupConfig{
		DevMode:          cfg.DevMode,
		Log:              log,
		APITokens:        cfg.APITokens,
		APIWriteTokens:   cfg.APIWriteTokens,
		GroupSvc:         groupSvc,
		Store:            store,
		SyncEngine:       engine,
		Scheduler:        scheduler,
		MaxResponseLimit: cfg.MaxResponseLimit,
	})

	log.Infow("Listening", "bind", cfg.GinBindString)
	if err := router.Run(cfg.GinBindString); err != nil {
		log.Fatalw("server stopped", zap.Error(err))
	}
}
