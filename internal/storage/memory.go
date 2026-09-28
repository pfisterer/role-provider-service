package storage

import (
	"context"
	"fmt"
	"sort"
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
	objID    string // group ID (no prefix)
	relation string // "member" or a configured relation such as "dozent"
	subjType string // "user" | "group"
	subjID   string // email or group ID (no prefix)
	subjRel  string // "member" when subjType == "group", else ""
	sourceID *uuid.UUID
}

// MemoryStore is a thread-safe, in-memory implementation of the Store interface.
// It is intended for local development only — data is not persisted across restarts.
type MemoryStore struct {
	mu       sync.RWMutex
	groups   map[string]*common.Group // keyed by Group.ID
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
				!strings.Contains(strings.ToLower(g.DisplayName), needle) &&
				!strings.Contains(strings.ToLower(g.Description), needle) {
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

func (s *MemoryStore) AddMember(_ context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	relation = common.NormalizeRelation(relation)
	subjRel := subjRelFor(memberType)
	for _, t := range s.tuples {
		if t.objID == groupID && t.relation == relation && t.subjType == memberType && t.subjID == memberID && t.subjRel == subjRel {
			return nil // idempotent
		}
	}
	s.tuples = append(s.tuples, memTuple{
		id:       uuid.New(),
		objID:    groupID,
		relation: relation,
		subjType: memberType,
		subjID:   memberID,
		subjRel:  subjRel,
		sourceID: sourceID,
	})
	return nil
}

func (s *MemoryStore) RemoveMember(_ context.Context, groupID, relation, memberType, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	relation = common.NormalizeRelation(relation)
	subjRel := subjRelFor(memberType)
	kept := s.tuples[:0]
	for _, t := range s.tuples {
		if t.objID == groupID && t.relation == relation && t.subjType == memberType && t.subjID == memberID && t.subjRel == subjRel {
			continue
		}
		kept = append(kept, t)
	}
	s.tuples = kept
	return nil
}

// GetDirectMembers returns the direct members of a group holding relation.
// "member" (or "") lists every direct tuple, since each relation implies
// membership; any other relation lists only its own tuples.
func (s *MemoryStore) GetDirectMembers(_ context.Context, groupID, relation string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	filter := filterFor(common.NormalizeRelation(relation))
	seen := map[string]struct{}{}
	var out []string
	for _, t := range s.tuples {
		// Skip pattern rules — they are membership rules, not concrete members.
		if t.objID != groupID || t.subjType == "pattern" || !relationMatches(t.relation, filter) {
			continue
		}
		token := common.BuildToken(t.subjType, t.subjID)
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out, nil
}

// GetAllMembers resolves the transitive members holding relation on a group.
// "member" (or "") means everyone in the group, whatever their relation. A
// sub-group contributes all of its members: group:A in B#dozent makes every
// member of A a dozent of B.
func (s *MemoryStore) GetAllMembers(_ context.Context, groupID, relation string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type step struct{ group, relation string }
	visited := map[step]struct{}{}
	seen := map[string]struct{}{}
	queue := []step{{groupID, common.NormalizeRelation(relation)}}
	var tokens []string

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, done := visited[current]; done {
			continue
		}
		visited[current] = struct{}{}
		for _, t := range s.tuples {
			if t.objID != current.group || t.subjType == "pattern" || !relationMatches(t.relation, filterFor(current.relation)) {
				continue
			}
			token := common.BuildToken(t.subjType, t.subjID)
			if _, already := seen[token]; !already {
				seen[token] = struct{}{}
				tokens = append(tokens, token)
			}
			if t.subjType == "group" {
				queue = append(queue, step{t.subjID, common.RelationMember})
			}
		}
	}
	return tokens, nil
}

// GetUserTokens returns the user token, a "group:<id>" token for every group the
// user is a member of (transitively; any relation counts), and a
// "group:<id>#<relation>" token for every other relation the user holds.
func (s *MemoryStore) GetUserTokens(_ context.Context, email string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matchesUser := func(t memTuple) bool {
		return (t.subjType == "user" && t.subjID == email) ||
			(t.subjType == "pattern" && common.MatchEmailPattern(t.subjID, email))
	}

	// Membership: groups the user is directly in (any relation), then every
	// group that contains one of those.
	memberOf := map[string]struct{}{}
	var order []string
	queue := []string{}
	add := func(id string) {
		if _, already := memberOf[id]; !already {
			memberOf[id] = struct{}{}
			order = append(order, id)
			queue = append(queue, id)
		}
	}
	for _, t := range s.tuples {
		if matchesUser(t) {
			add(t.objID)
		}
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, t := range s.tuples {
			if t.subjType == "group" && t.subjID == current {
				add(t.objID)
			}
		}
	}

	tokens := []string{common.UserPrefix + email}
	for _, id := range order {
		tokens = append(tokens, common.GroupPrefix+id)
	}

	// Relations beyond membership: held directly, through a pattern, or through
	// a group the user is a member of.
	seen := map[string]struct{}{}
	for _, t := range s.tuples {
		if t.relation == common.RelationMember {
			continue
		}
		_, viaGroup := memberOf[t.subjID]
		if matchesUser(t) || (t.subjType == "group" && viaGroup) {
			token := common.GroupToken(t.objID, t.relation)
			if _, dup := seen[token]; !dup {
				seen[token] = struct{}{}
				tokens = append(tokens, token)
			}
		}
	}
	return tokens, nil
}

// SearchUsers returns email addresses that contain query, case-insensitively.
// Email-only by design — see the PostgresStore implementation.
func (s *MemoryStore) SearchUsers(_ context.Context, query string, limit int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(query))
	seen := map[string]struct{}{}
	var out []string
	for _, t := range s.tuples {
		if t.subjType != "user" {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(t.subjID), needle) {
			continue
		}
		if _, dup := seen[t.subjID]; dup {
			continue
		}
		seen[t.subjID] = struct{}{}
		out = append(out, t.subjID)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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

func (s *MemoryStore) UpdateSource(_ context.Context, id uuid.UUID, name, schedule, dnEmailRegexp, groupRelationRegexp, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[id]
	if !ok {
		return fmt.Errorf("source %q not found", id)
	}
	src.Name = name
	src.Schedule = schedule
	src.DNEmailRegexp = dnEmailRegexp
	src.GroupRelationRegexp = groupRelationRegexp
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

func (s *MemoryStore) ReplaceTuples(_ context.Context, sourceID uuid.UUID, newTuples []common.TuplePair, descriptions map[string]string) (added, removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type key struct{ groupID, relation, memberType, memberID string }

	existingIdx := map[key]int{}
	for i, t := range s.tuples {
		if t.sourceID != nil && *t.sourceID == sourceID {
			existingIdx[key{t.objID, t.relation, t.subjType, t.subjID}] = i
		}
	}

	newSet := map[key]struct{}{}
	for _, p := range newTuples {
		k := key{p.GroupID, common.NormalizeRelation(p.Relation), p.MemberType, p.MemberID}
		if _, dup := newSet[k]; dup {
			continue
		}
		newSet[k] = struct{}{}

		// Auto-create missing group, and keep an existing group's description in
		// sync with the import.
		if g, ok := s.groups[p.GroupID]; !ok {
			now := time.Now().UTC()
			sid := sourceID
			s.groups[p.GroupID] = &common.Group{
				ID:          p.GroupID,
				Token:       common.GroupPrefix + p.GroupID,
				DisplayName: p.GroupID,
				Description: descriptions[p.GroupID],
				SourceID:    &sid,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
		} else if desc, has := descriptions[p.GroupID]; has && g.Description != desc {
			g.Description = desc
			g.UpdatedAt = time.Now().UTC()
		}

		if _, exists := existingIdx[k]; !exists {
			sid := sourceID
			s.tuples = append(s.tuples, memTuple{
				id:       uuid.New(),
				objID:    p.GroupID,
				relation: k.relation,
				subjType: p.MemberType,
				subjID:   p.MemberID,
				subjRel:  subjRelFor(p.MemberType),
				sourceID: &sid,
			})
			added++
		}
	}

	// Remove tuples that are no longer in the new set.
	kept := s.tuples[:0]
	for _, t := range s.tuples {
		if t.sourceID != nil && *t.sourceID == sourceID {
			k := key{t.objID, t.relation, t.subjType, t.subjID}
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

// subjRelFor is the subject relation stored with a tuple: a group subject always
// means "the members of that group" in this iteration; users and patterns have none.
func subjRelFor(memberType string) string {
	if memberType == "group" {
		return common.RelationMember
	}
	return ""
}

// filterFor turns the relation asked about into a tuple filter: asking for
// "member" means every relation, since each implies membership.
func filterFor(relation string) string {
	if relation == common.RelationMember {
		return ""
	}
	return relation
}

// relationMatches reports whether a tuple's relation passes the filter ("" = any).
func relationMatches(tupleRelation, filter string) bool {
	return filter == "" || tupleRelation == filter
}
