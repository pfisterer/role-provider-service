// Package catalog holds an in-memory, read-optimized snapshot of the group
// catalog. Group search (type-ahead in the UI, SearchGroupTokens in consumers)
// is a frequent, latency-sensitive operation, but the group set only changes on
// imports — the ideal cache profile. Serving search from this snapshot keeps
// every keystroke a microsecond-scale in-memory substring scan instead of a
// store round-trip. The store stays the source of truth; the cache is refreshed
// on startup, on a background ticker, and immediately after each sync.
package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
)

// Loader returns the full current group set (the store is the source of truth).
type Loader func(context.Context) ([]common.Group, error)

// GroupCache is a concurrency-safe, sorted in-memory snapshot of all groups.
type GroupCache struct {
	load Loader
	log  *zap.SugaredLogger

	mu     sync.RWMutex
	groups []common.Group // kept sorted by ID for stable, limited search results
}

// New creates a cache backed by load. Call Refresh once before serving.
func New(load Loader, log *zap.SugaredLogger) *GroupCache {
	return &GroupCache{load: load, log: log}
}

// Refresh reloads the full group set from the loader and replaces the snapshot.
func (c *GroupCache) Refresh(ctx context.Context) error {
	groups, err := c.load(ctx)
	if err != nil {
		return err
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	c.mu.Lock()
	c.groups = groups
	c.mu.Unlock()
	return nil
}

// StartAutoRefresh refreshes the snapshot every interval until ctx is cancelled.
// A non-positive interval disables the ticker (startup + after-sync refreshes only).
func (c *GroupCache) StartAutoRefresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := c.Refresh(ctx); err != nil {
					c.log.Warnw("group cache refresh failed", zap.Error(err))
				}
			}
		}
	}()
}

// Size returns the number of cached groups (for logging / health).
func (c *GroupCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.groups)
}

// Search returns groups whose ID or display name contains query
// (case-insensitive). An empty query matches all. Results are sorted by ID and
// truncated to limit (limit <= 0 means no limit). Served entirely from memory.
func (c *GroupCache) Search(query string, limit int) []common.Group {
	q := strings.ToLower(strings.TrimSpace(query))

	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]common.Group, 0, min(len(c.groups), max(limit, 0)))
	for _, g := range c.groups {
		if q != "" &&
			!strings.Contains(strings.ToLower(g.ID), q) &&
			!strings.Contains(strings.ToLower(g.DisplayName), q) {
			continue
		}
		out = append(out, g)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
