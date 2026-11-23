package sync

import (
	"context"
	"fmt"
	gosync "sync"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
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
	// mu guards jobs: HTTP handlers register and unregister concurrently.
	mu   gosync.Mutex
	jobs map[uuid.UUID]cron.EntryID
}

// ValidateSchedule checks a cron expression the way Register will parse it, so a source is never stored with a schedule that cannot run.
func ValidateSchedule(schedule string) error {
	if _, err := cron.ParseStandard(schedule); err != nil {
		return fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}
	return nil
}

// CheckSchedule validates a schedule for a source of the given type. Only the keycloak source reads its data itself; CSV and LDIF sources get theirs only by upload, so a scheduled run of one would have no content and fail every time — such a schedule is refused rather than stored.
func CheckSchedule(sourceType, schedule string) error {
	if schedule == "" {
		return nil
	}
	if sourceType != common.SourceTypeKeycloak {
		return fmt.Errorf("CSV and LDIF sources sync only by upload (POST /v1/sync/sources/{id}/upload), so a schedule has nothing to run; leave schedule empty (only the keycloak source runs on a schedule)")
	}
	return ValidateSchedule(schedule)
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

// Start loads all sources with a schedule and registers them, then starts the cron runner. Only the keycloak source's schedule is registered; see CheckSchedule.
func (s *Scheduler) Start(ctx context.Context) error {
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return err
	}
	for _, src := range sources {
		// A schedule stored before uploads became the only way in: it is left in the database, like a stored file path, so an older image still finds it, but never registered, since every run would fail for want of content.
		if src.Schedule != "" && src.Type != common.SourceTypeKeycloak {
			s.log.Warnw("stored schedule of a source that syncs only by upload is ignored; clear it with PUT /v1/sync/sources/{id}", "source", src.Name, "source_id", src.ID, "type", src.Type, "schedule", src.Schedule)
			continue
		}
		if src.Schedule != "" {
			if err := s.Register(src.ID, src.Schedule); err != nil {
				s.log.Warnw("failed to register cron for source", "source_id", src.ID, "schedule", src.Schedule, zap.Error(err))
			}
		}
	}
	s.cron.Start()
	s.mu.Lock()
	registered := len(s.jobs)
	s.mu.Unlock()
	s.log.Infow("sync scheduler started", "registered_jobs", registered)
	return nil
}

// Stop gracefully stops the cron scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// Register adds or replaces the cron schedule for a source.
func (s *Scheduler) Register(sourceID uuid.UUID, schedule string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entryID, err := s.cron.AddFunc(schedule, func() {
		s.log.Infow("scheduled sync triggered", "source_id", sourceID)
		if err := s.engine.RunSync(context.Background(), sourceID, nil); err != nil {
			s.log.Errorw("scheduled sync failed", "source_id", sourceID, zap.Error(err))
		}
	})
	if err != nil {
		return err
	}
	// The old job goes only once the new one is in, so a bad schedule leaves the source running as before.
	s.unregisterLocked(sourceID)
	s.jobs[sourceID] = entryID
	s.log.Infow("cron job registered", "source_id", sourceID, "schedule", schedule)
	return nil
}

// Unregister removes the cron job for a source.
func (s *Scheduler) Unregister(sourceID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unregisterLocked(sourceID)
}

func (s *Scheduler) unregisterLocked(sourceID uuid.UUID) {
	if id, ok := s.jobs[sourceID]; ok {
		s.cron.Remove(id)
		delete(s.jobs, sourceID)
	}
}
