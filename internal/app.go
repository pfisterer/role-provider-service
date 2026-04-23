package app

import (
	"context"
	"fmt"
	"os"
	"time"

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

	// Service layer.
	timeout := time.Duration(cfg.ServiceTimeoutSeconds) * time.Second
	groupSvc := groupmgmt.NewService(store, timeout, log)

	// Sync engine + scheduler.
	engine := syncp.NewEngine(store, log)
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
