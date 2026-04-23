package sync

import (
	"context"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

// Scheduler manages cron-based sync jobs for all sources that have a schedule set.
type Scheduler struct {
	cron   *cron.Cron
	engine *Engine
	store  storage.Store
	log    *zap.SugaredLogger
	jobs   map[uuid.UUID]cron.EntryID
}

func NewScheduler(engine *Engine, store storage.Store, log *zap.SugaredLogger) *Scheduler {
	return &Scheduler{
		cron:   cron.New(),
		engine: engine,
		store:  store,
		log:    log,
		jobs:   make(map[uuid.UUID]cron.EntryID),
	}
}

// Start loads all sources with a schedule and registers them, then starts the cron runner.
func (s *Scheduler) Start(ctx context.Context) error {
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.Schedule != "" {
			if err := s.Register(src.ID, src.Schedule); err != nil {
				s.log.Warnw("failed to register cron for source", "source_id", src.ID, "schedule", src.Schedule, zap.Error(err))
			}
		}
	}
	s.cron.Start()
	s.log.Infow("sync scheduler started", "registered_jobs", len(s.jobs))
	return nil
}

// Stop gracefully stops the cron scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// Register adds or replaces the cron schedule for a source.
func (s *Scheduler) Register(sourceID uuid.UUID, schedule string) error {
	s.Unregister(sourceID)

	entryID, err := s.cron.AddFunc(schedule, func() {
		s.log.Infow("scheduled sync triggered", "source_id", sourceID)
		if err := s.engine.RunSync(context.Background(), sourceID, nil); err != nil {
			s.log.Errorw("scheduled sync failed", "source_id", sourceID, zap.Error(err))
		}
	})
	if err != nil {
		return err
	}
	s.jobs[sourceID] = entryID
	s.log.Infow("cron job registered", "source_id", sourceID, "schedule", schedule)
	return nil
}

// Unregister removes the cron job for a source.
func (s *Scheduler) Unregister(sourceID uuid.UUID) {
	if id, ok := s.jobs[sourceID]; ok {
		s.cron.Remove(id)
		delete(s.jobs, sourceID)
	}
}
