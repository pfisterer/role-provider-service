package sync

import (
	"context"
	gosync "sync"
	"testing"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

// HTTP handlers register and unregister schedules concurrently; run with -race, this caught the unguarded map.
func TestScheduler_ConcurrentRegister(t *testing.T) {
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	s := NewScheduler(NewEngine(store, relations, zap.NewNop().Sugar()), store, zap.NewNop().Sugar())
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	var wg gosync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := ids[i%len(ids)]
			if i%3 == 0 {
				s.Unregister(id)
				return
			}
			if err := s.Register(id, "0 3 * * *"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) > len(ids) || len(s.cron.Entries()) != len(s.jobs) {
		t.Errorf("jobs = %d, cron entries = %d", len(s.jobs), len(s.cron.Entries()))
	}
}

// A schedule that cannot run is refused before it is stored, and replacing a schedule with a bad one keeps the old job.
func TestScheduler_InvalidSchedule(t *testing.T) {
	for _, bad := range []string{"every day", "* * *", "61 * * * *", "@sometimes"} {
		if ValidateSchedule(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"*/15 * * * *", "0 3 * * 1-5", "@hourly"} {
		if err := ValidateSchedule(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}

	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	s := NewScheduler(NewEngine(store, relations, zap.NewNop().Sugar()), store, zap.NewNop().Sugar())
	id := uuid.New()
	if err := s.Register(id, "0 3 * * *"); err != nil {
		t.Fatal(err)
	}
	if err := s.Register(id, "every day"); err == nil {
		t.Fatal("bad schedule registered")
	}
	if _, ok := s.jobs[id]; !ok || len(s.cron.Entries()) != 1 {
		t.Errorf("the old job is gone: jobs = %v", s.jobs)
	}
}
