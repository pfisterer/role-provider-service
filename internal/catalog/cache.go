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

// Search answers a group search box (see query for the syntax). Besides the
// groups themselves it offers each relation someone holds in a group as an
// entry of its own — "group:standort-ma#beschaeftigte" — because that token is
// what a person wants to enter, and nobody can be expected to know it by heart.
// The relation's name counts as a search term for that entry.
//
// An empty query lists the groups alone, sorted by ID. Otherwise an exact ID
// comes first, then entries matching more terms, then by ID. limit <= 0 means
// no limit. Served entirely from memory.
func (c *GroupCache) Search(raw string, limit int) []common.Group {
	q := parseQuery(raw)

	c.mu.RLock()
	defer c.mu.RUnlock()

	if q.empty() {
		n := len(c.groups)
		if limit > 0 && limit < n {
			n = limit
		}
		return append([]common.Group(nil), c.groups[:n]...)
	}

	type hit struct {
		group common.Group
		score int
	}
	var hits []hit
	consider := func(g common.Group, fields []string) {
		score := q.score(fields)
		if score < 0 {
			return
		}
		if q.exact(fold(g.ID)) {
			score += 1000
		}
		hits = append(hits, hit{g, score})
	}
	for _, g := range c.groups {
		base := []string{fold(g.ID), fold(g.DisplayName), fold(g.Description)}
		consider(g, base)
		for _, rel := range g.Relations {
			consider(relationEntry(g, rel), append(base, fold(rel), fold(g.ID)+"#"+fold(rel)))
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].group.ID < hits[j].group.ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]common.Group, len(hits))
	for i, h := range hits {
		out[i] = h.group
	}
	return out
}

// relationEntry is a group's relation presented as a search result: its token
// is the relation token, its description the group's with the role named.
func relationEntry(g common.Group, relation string) common.Group {
	e := g
	e.ID = g.ID + common.RelationSeparator + relation
	e.Token = common.GroupToken(g.ID, relation)
	e.DisplayName = e.ID
	desc := g.Description
	if desc == "" {
		desc = g.DisplayName
	}
	e.Description = strings.TrimSpace(desc + " · Rolle: " + relation)
	e.Relations = nil
	return e
}
