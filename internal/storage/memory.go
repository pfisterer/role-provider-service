package storage

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
	"gorm.io/gorm"
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
	created  time.Time
	expires  *time.Time
}

// live reports whether the tuple still counts (see liveTuple).
func (t memTuple) live() bool { return t.expires == nil || t.expires.After(time.Now()) }

// MemoryStore is a thread-safe, in-memory implementation of the Store interface.
// It is intended for local development only — data is not persisted across restarts.
type MemoryStore struct {
	mu       sync.RWMutex
	groups   map[string]*common.Group // keyed by Group.ID
	sources  map[uuid.UUID]*common.Source
	tuples   []memTuple
	syncLogs []common.SyncLog
	managers map[string][]common.Manager // keyed by group ID
	changes  []common.Change
	log      *zap.SugaredLogger
}

// NewMemoryStore creates an empty in-memory store.
func NewMemoryStore(log *zap.SugaredLogger) *MemoryStore {
	log.Info("In-memory storage initialized (data will not persist across restarts)")
	return &MemoryStore{
		groups:   make(map[string]*common.Group),
		sources:  make(map[uuid.UUID]*common.Source),
		managers: make(map[string][]common.Manager),
		log:      log,
	}
}

// ── Groups ────────────────────────────────────────────────────────────────────

func (s *MemoryStore) GroupRelations(_ context.Context) (map[string][]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]map[string]bool{}
	for _, t := range s.tuples {
		if t.relation == common.RelationMember || !t.live() {
			continue
		}
		if seen[t.objID] == nil {
			seen[t.objID] = map[string]bool{}
		}
		seen[t.objID][t.relation] = true
	}
	out := map[string][]string{}
	for id, rels := range seen {
		for r := range rels {
			out[id] = append(out[id], r)
		}
		slices.Sort(out[id])
	}
	return out, nil
}

func (s *MemoryStore) GroupOwners(_ context.Context, ids []string) (map[string]*uuid.UUID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]*uuid.UUID{}
	for _, id := range ids {
		if g, ok := s.groups[id]; ok {
			out[id] = g.SourceID
		}
	}
	return out, nil
}

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
		return nil, fmt.Errorf("group %q: %w", id, gorm.ErrRecordNotFound)
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
		return fmt.Errorf("group %q: %w", id, gorm.ErrRecordNotFound)
	}
	g.DisplayName = displayName
	g.Description = description
	g.UpdatedAt = time.Now().UTC()
	return nil
}

// DeleteGroup removes the group with its members and its memberships in other groups (see PostgresStore.DeleteGroup).
func (s *MemoryStore) DeleteGroup(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.groups, id)
	delete(s.managers, id)
	for gid, ms := range s.managers {
		s.managers[gid] = slices.DeleteFunc(ms, func(m common.Manager) bool {
			g, _ := common.SplitGroupRelation(strings.TrimPrefix(m.Token, common.GroupPrefix))
			return strings.HasPrefix(m.Token, common.GroupPrefix) && g == id
		})
	}
	kept := s.tuples[:0]
	for _, t := range s.tuples {
		if t.objID == id || (t.subjType == "group" && t.subjID == id) {
			continue
		}
		kept = append(kept, t)
	}
	s.tuples = kept
	return nil
}

// ── Members / Tuples ──────────────────────────────────────────────────────────

func (s *MemoryStore) AddMember(_ context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID, expiresAt *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	relation = common.NormalizeRelation(relation)
	subjRel := subjRelFor(memberType)
	for i, t := range s.tuples {
		if t.objID == groupID && t.relation == relation && t.subjType == memberType && t.subjID == memberID && t.subjRel == subjRel {
			s.tuples[i].expires = expiresAt
			return nil
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
		created:  time.Now().UTC(),
		expires:  expiresAt,
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

func (s *MemoryStore) ChangeRelation(_ context.Context, groupID, memberType, memberID, from, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	from, to = common.NormalizeRelation(from), common.NormalizeRelation(to)
	subjRel := subjRelFor(memberType)
	find := func(relation string) int {
		return slices.IndexFunc(s.tuples, func(t memTuple) bool {
			return t.objID == groupID && t.relation == relation && t.subjType == memberType && t.subjID == memberID && t.subjRel == subjRel
		})
	}
	old := find(from)
	if old < 0 {
		return fmt.Errorf("entry: %w", gorm.ErrRecordNotFound)
	}
	if from == to {
		return nil
	}
	target := find(to)
	if target < 0 {
		s.tuples[old].relation = to
		return nil
	}
	s.tuples[target].expires = laterExpiry(s.tuples[old].expires, s.tuples[target].expires)
	s.tuples = slices.Delete(s.tuples, old, old+1)
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
		if t.objID != groupID || t.subjType == "pattern" || !t.live() || !relationMatches(t.relation, filter) {
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

func (s *MemoryStore) ListMemberships(_ context.Context, groupID string) ([]common.Membership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []common.Membership{}
	for _, t := range s.tuples {
		if t.objID != groupID {
			continue
		}
		out = append(out, common.Membership{
			Member:    common.BuildToken(t.subjType, t.subjID),
			Relation:  t.relation,
			ExpiresAt: t.expires,
			Expired:   !t.live(),
			CreatedAt: t.created,
		})
	}
	slices.SortFunc(out, func(a, b common.Membership) int {
		return strings.Compare(a.Member+"#"+a.Relation, b.Member+"#"+b.Relation)
	})
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
			if t.objID != current.group || t.subjType == "pattern" || !t.live() || !relationMatches(t.relation, filterFor(current.relation)) {
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
		return t.live() && ((t.subjType == "user" && t.subjID == email) ||
			(t.subjType == "pattern" && common.MatchEmailPattern(t.subjID, email)))
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
			if t.subjType == "group" && t.subjID == current && t.live() {
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
		if t.relation == common.RelationMember || !t.live() {
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
		if t.subjType != "user" || !t.live() {
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
		return nil, fmt.Errorf("source %q: %w", id, gorm.ErrRecordNotFound)
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

func (s *MemoryStore) UpdateSource(_ context.Context, upd *common.Source) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.sources[upd.ID]
	if !ok {
		return fmt.Errorf("source %q: %w", upd.ID, gorm.ErrRecordNotFound)
	}
	src.Name = upd.Name
	src.Schedule = upd.Schedule
	src.DNEmailRegexp = upd.DNEmailRegexp
	src.GroupRelationRegexp = upd.GroupRelationRegexp
	src.FilePath = upd.FilePath
	src.ReservedNames = slices.Clone(upd.ReservedNames)
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
		return fmt.Errorf("source %q: %w", id, gorm.ErrRecordNotFound)
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
				created:  time.Now().UTC(),
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

	// As in the Postgres store: a group of this source with nothing in it and nothing pointing at it goes.
	for id, g := range s.groups {
		if g.SourceID == nil || *g.SourceID != sourceID || len(s.managers[id]) > 0 {
			continue
		}
		used := slices.ContainsFunc(s.tuples, func(t memTuple) bool { return t.objID == id || (t.subjType == "group" && t.subjID == id) })
		for _, ms := range s.managers {
			used = used || slices.ContainsFunc(ms, func(m common.Manager) bool {
				gid, _ := common.SplitGroupRelation(strings.TrimPrefix(m.Token, common.GroupPrefix))
				return strings.HasPrefix(m.Token, common.GroupPrefix) && gid == id
			})
		}
		if !used {
			delete(s.groups, id)
		}
	}
	return
}

// ── Moving between sources ───────────────────────────────────────────────────

func (s *MemoryStore) MoveToSource(_ context.Context, from *uuid.UUID, to uuid.UUID) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owned := func(id *uuid.UUID) bool {
		if from == nil {
			return id == nil
		}
		return id != nil && *id == *from
	}
	var moved []string
	for _, g := range s.groups {
		if owned(g.SourceID) {
			sid := to
			g.SourceID = &sid
			moved = append(moved, g.ID)
		}
	}
	for i := range s.tuples {
		if owned(s.tuples[i].sourceID) {
			sid := to
			s.tuples[i].sourceID = &sid
		}
	}
	slices.Sort(moved)
	return moved, nil
}

// ── Managers ─────────────────────────────────────────────────────────────────

func (s *MemoryStore) ListManagers(_ context.Context, groupID string) ([]common.Manager, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := slices.Clone(s.managers[groupID])
	slices.SortFunc(out, func(a, b common.Manager) int { return strings.Compare(a.Token, b.Token) })
	return out, nil
}

func (s *MemoryStore) AddManager(_ context.Context, groupID string, m common.Manager) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.managers[groupID] {
		if existing.Token == m.Token {
			return nil
		}
	}
	m.CreatedAt = time.Now().UTC()
	s.managers[groupID] = append(s.managers[groupID], m)
	return nil
}

func (s *MemoryStore) RemoveManager(_ context.Context, groupID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managers[groupID] = slices.DeleteFunc(s.managers[groupID], func(m common.Manager) bool { return m.Token == token })
	return nil
}

func (s *MemoryStore) ManagedGroups(_ context.Context, tokens []string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := []string{}
	for gid, ms := range s.managers {
		if slices.ContainsFunc(ms, func(m common.Manager) bool { return slices.Contains(tokens, m.Token) }) {
			ids = append(ids, gid)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ── History ──────────────────────────────────────────────────────────────────

func (s *MemoryStore) AddChange(_ context.Context, c *common.Change) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	s.changes = append(s.changes, *c)
	return nil
}

func (s *MemoryStore) ListChanges(_ context.Context, groupID string, limit int) ([]common.Change, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []common.Change{}
	for i := len(s.changes) - 1; i >= 0; i-- {
		if s.changes[i].GroupID == groupID {
			out = append(out, s.changes[i])
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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
