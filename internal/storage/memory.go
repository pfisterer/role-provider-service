package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
)

// memTuple is the internal representation of a Zanzibar-style tuple.
type memTuple struct {
	id       uuid.UUID
	objID    string     // group ID (no prefix)
	subjType string     // "user" | "group"
	subjID   string     // email or group ID (no prefix)
	subjRel  string     // "member" when subjType == "group", else ""
	sourceID *uuid.UUID
}

// MemoryStore is a thread-safe, in-memory implementation of the Store interface.
// It is intended for local development only — data is not persisted across restarts.
type MemoryStore struct {
	mu       sync.RWMutex
	groups   map[string]*common.Group   // keyed by Group.ID
	sources  map[uuid.UUID]*common.Source
	tuples   []memTuple
	syncLogs []common.SyncLog
	log      *zap.SugaredLogger
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore(log *zap.SugaredLogger) *MemoryStore {
	log.Info("In-memory storage initialized (data will not persist across restarts)")
	return &MemoryStore{
		groups:  make(map[string]*common.Group),
		sources: make(map[uuid.UUID]*common.Source),
		log:     log,
	}
}

// ── Groups ────────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateGroup(_ context.Context, g *common.Group) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.groups[g.ID]; exists {
		return fmt.Errorf("group %q already exists", g.ID)
	}
	now := time.Now().UTC()
	g.CreatedAt = now
	g.UpdatedAt = now
	g.Token = common.GroupPrefix + g.ID
	cp := *g
	s.groups[g.ID] = &cp
	return nil
}

func (s *MemoryStore) GetGroup(_ context.Context, id string) (*common.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.groups[id]
	if !ok {
		return nil, fmt.Errorf("group %q not found", id)
	}
	cp := *g
	return &cp, nil
}

func (s *MemoryStore) ListGroups(_ context.Context, query string, sourceID *uuid.UUID, limit int) ([]common.Group, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(query))
	var out []common.Group
	for _, g := range s.groups {
		if needle != "" {
			if !strings.Contains(strings.ToLower(g.ID), needle) &&
				!strings.Contains(strings.ToLower(g.DisplayName), needle) {
				continue
			}
		}
		if sourceID != nil {
			match := (g.SourceID != nil && *g.SourceID == *sourceID)
			if !match {
				// Also include groups that have at least one tuple from this source.
				for _, t := range s.tuples {
					if t.objID == g.ID && t.sourceID != nil && *t.sourceID == *sourceID {
						match = true
						break
					}
				}
			}
			if !match {
				continue
			}
		}
		cp := *g
		out = append(out, cp)
	}

	// Stable sort by ID.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) UpdateGroup(_ context.Context, id, displayName, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return fmt.Errorf("group %q not found", id)
	}
	g.DisplayName = displayName
	g.Description = description
	g.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemoryStore) DeleteGroup(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, id)
	return nil
}

// ── Members / Tuples ──────────────────────────────────────────────────────────

func (s *MemoryStore) AddMember(_ context.Context, groupID, memberType, memberID string, sourceID *uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subjRel := ""
	if memberType == "group" {
		subjRel = "member"
	}
	for _, t := range s.tuples {
		if t.objID == groupID && t.subjType == memberType && t.subjID == memberID && t.subjRel == subjRel {
			return nil // idempotent
		}
	}
	s.tuples = append(s.tuples, memTuple{
		id:       uuid.New(),
		objID:    groupID,
		subjType: memberType,
		subjID:   memberID,
		subjRel:  subjRel,
		sourceID: sourceID,
	})
	return nil
}

func (s *MemoryStore) RemoveMember(_ context.Context, groupID, memberType, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	subjRel := ""
	if memberType == "group" {
		subjRel = "member"
	}
	kept := s.tuples[:0]
	for _, t := range s.tuples {
		if t.objID == groupID && t.subjType == memberType && t.subjID == memberID && t.subjRel == subjRel {
			continue
		}
		kept = append(kept, t)
	}
	s.tuples = kept
	return nil
}

func (s *MemoryStore) GetDirectMembers(_ context.Context, groupID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for _, t := range s.tuples {
		if t.objID == groupID {
			out = append(out, common.BuildToken(t.subjType, t.subjID))
		}
	}
	return out, nil
}

// GetAllMembers resolves all transitive members of a group (users + sub-groups) in memory.
func (s *MemoryStore) GetAllMembers(_ context.Context, groupID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := map[string]struct{}{}
	queue := []string{groupID}
	var tokens []string

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, t := range s.tuples {
			if t.objID != current {
				continue
			}
			token := common.BuildToken(t.subjType, t.subjID)
			if _, already := seen[token]; already {
				continue
			}
			seen[token] = struct{}{}
			tokens = append(tokens, token)
			if t.subjType == "group" {
				queue = append(queue, t.subjID)
			}
		}
	}
	return tokens, nil
}

// GetUserTokens returns the user token + all group tokens for the given email (reverse recursive lookup).
func (s *MemoryStore) GetUserTokens(_ context.Context, email string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tokens := []string{common.UserPrefix + email}
	seen := map[string]struct{}{email: {}}

	// Seed: find groups the user is directly in.
	queue := []string{}
	for _, t := range s.tuples {
		if t.subjType == "user" && t.subjID == email {
			if _, already := seen[t.objID]; !already {
				seen[t.objID] = struct{}{}
				queue = append(queue, t.objID)
				tokens = append(tokens, common.GroupPrefix+t.objID)
			}
		}
	}

	// Expand: find groups that contain those groups.
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, t := range s.tuples {
			if t.subjType == "group" && t.subjID == current {
				if _, already := seen[t.objID]; !already {
					seen[t.objID] = struct{}{}
					queue = append(queue, t.objID)
					tokens = append(tokens, common.GroupPrefix+t.objID)
				}
			}
		}
	}
	return tokens, nil
}

// ── Sources ───────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateSource(_ context.Context, src *common.Source) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	src.CreatedAt = now
	src.UpdatedAt = now
	cp := *src
	s.sources[src.ID] = &cp
	return nil
}

func (s *MemoryStore) GetSource(_ context.Context, id uuid.UUID) (*common.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src, ok := s.sources[id]
	if !ok {
		return nil, fmt.Errorf("source %q not found", id)
	}
	cp := *src
	return &cp, nil
}

func (s *MemoryStore) ListSources(_ context.Context) ([]common.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]common.Source, 0, len(s.sources))
	for _, src := range s.sources {
		out = append(out, *src)
	}
	// Sort by Name.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (s *MemoryStore) UpdateSource(_ context.Context, id uuid.UUID, name, schedule, dnEmailRegexp, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[id]
	if !ok {
		return fmt.Errorf("source %q not found", id)
	}
	src.Name = name
	src.Schedule = schedule
	src.DNEmailRegexp = dnEmailRegexp
	src.FilePath = filePath
	src.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *MemoryStore) DeleteSource(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Remove all tuples owned by this source and orphan their groups.
	kept := s.tuples[:0]
	for _, t := range s.tuples {
		if t.sourceID != nil && *t.sourceID == id {
			continue
		}
		kept = append(kept, t)
	}
	s.tuples = kept
	for _, g := range s.groups {
		if g.SourceID != nil && *g.SourceID == id {
			g.SourceID = nil
		}
	}
	delete(s.sources, id)
	return nil
}

func (s *MemoryStore) UpdateSourceSyncStatus(_ context.Context, id uuid.UUID, status string, syncedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[id]
	if !ok {
		return fmt.Errorf("source %q not found", id)
	}
	src.LastSyncStatus = status
	src.LastSyncedAt = &syncedAt
	src.UpdatedAt = time.Now().UTC()
	return nil
}

// ── Sync Logs ─────────────────────────────────────────────────────────────────

func (s *MemoryStore) CreateSyncLog(_ context.Context, l *common.SyncLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncLogs = append(s.syncLogs, *l)
	return nil
}

func (s *MemoryStore) UpdateSyncLog(_ context.Context, l *common.SyncLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.syncLogs {
		if existing.ID == l.ID {
			s.syncLogs[i] = *l
			return nil
		}
	}
	return fmt.Errorf("sync log %q not found", l.ID)
}

func (s *MemoryStore) ListSyncLogs(_ context.Context, sourceID uuid.UUID, limit int) ([]common.SyncLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []common.SyncLog
	for _, l := range s.syncLogs {
		if l.SourceID == sourceID {
			out = append(out, l)
		}
	}
	// Sort by StartedAt DESC.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].StartedAt.After(out[j-1].StartedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ── ReplaceTuples ─────────────────────────────────────────────────────────────

func (s *MemoryStore) ReplaceTuples(_ context.Context, sourceID uuid.UUID, newTuples []common.TuplePair) (added, removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type key struct{ groupID, memberType, memberID string }

	existingIdx := map[key]int{}
	for i, t := range s.tuples {
		if t.sourceID != nil && *t.sourceID == sourceID {
			existingIdx[key{t.objID, t.subjType, t.subjID}] = i
		}
	}

	newSet := map[key]struct{}{}
	for _, p := range newTuples {
		k := key{p.GroupID, p.MemberType, p.MemberID}
		if _, dup := newSet[k]; dup {
			continue
		}
		newSet[k] = struct{}{}

		// Auto-create missing group.
		if _, ok := s.groups[p.GroupID]; !ok {
			now := time.Now().UTC()
			sid := sourceID
			s.groups[p.GroupID] = &common.Group{
				ID:          p.GroupID,
				Token:       common.GroupPrefix + p.GroupID,
				DisplayName: p.GroupID,
				SourceID:    &sid,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
		}

		if _, exists := existingIdx[k]; !exists {
			subjRel := ""
			if p.MemberType == "group" {
				subjRel = "member"
			}
			sid := sourceID
			s.tuples = append(s.tuples, memTuple{
				id:       uuid.New(),
				objID:    p.GroupID,
				subjType: p.MemberType,
				subjID:   p.MemberID,
				subjRel:  subjRel,
				sourceID: &sid,
			})
			added++
		}
	}

	// Remove tuples that are no longer in the new set.
	kept := s.tuples[:0]
	for _, t := range s.tuples {
		if t.sourceID != nil && *t.sourceID == sourceID {
			k := key{t.objID, t.subjType, t.subjID}
			if _, keep := newSet[k]; !keep {
				removed++
				continue
			}
		}
		kept = append(kept, t)
	}
	s.tuples = kept
	return
}
