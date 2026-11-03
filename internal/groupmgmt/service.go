package groupmgmt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/catalog"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// ErrAlreadyExists is returned when a resource already exists.
var ErrAlreadyExists = errors.New("already exists")

// ErrOwnedBySource marks a change through the API to a group a sync source
// maintains.
var ErrOwnedBySource = errors.New("owned by a sync source")

// ErrReserved marks a group id a sync source has reserved for itself.
var ErrReserved = errors.New("reserved by a sync source")

// ErrForbidden marks a change the caller may not make: the group is not one they maintain, or the change is for admins.
var ErrForbidden = errors.New("forbidden")

// ErrCycle marks a group that would end up containing itself.
var ErrCycle = errors.New("membership cycle")

// ErrInvalidToken is returned when a token string has an invalid format.
var ErrInvalidToken = fmt.Errorf("invalid token: must start with '%s' or '%s'", common.GroupPrefix, common.UserPrefix)

// ErrInvalidRelation is returned for a relation that is not configured.
var ErrInvalidRelation = errors.New("invalid relation")

// ErrInvalidExpiry is returned for an expiry that is not in the future.
var ErrInvalidExpiry = errors.New("invalid expiry")

// ActorAPIToken is the actor recorded for a change made with a write token: a service or an operator's script, not a person signed in.
const ActorAPIToken = "api-token"

// Caller is who changes a group and with which right. Everything is judged by Tokens, the tokens of the person the request acts as; Actor and OnBehalfOf only go into the history.
type Caller struct {
	// Actor is the person signed in, or ActorAPIToken.
	Actor string
	// OnBehalfOf is the person an admin acts for; empty otherwise.
	OnBehalfOf string
	// Tokens are the tokens of the person the request acts as.
	Tokens []string
	// Admin may maintain every group of the local source, create and delete groups.
	Admin bool
}

// APITokenCaller is the caller behind a write token. A write token has always been able to change every group the API owns, so it is an admin here.
func APITokenCaller() Caller { return Caller{Actor: ActorAPIToken, Admin: true} }

// Service handles all group and member management operations.
type Service struct {
	store     storage.Store
	cache     *catalog.GroupCache
	relations common.Relations
	// localID is the local source: every group created here belongs to it, and only its groups can be changed here.
	localID uuid.UUID
	timeout time.Duration
	log     *zap.SugaredLogger
	// userLookup, if set, runs before a user's tokens are resolved.
	userLookup func(ctx context.Context, email string)
}

func NewService(store storage.Store, cache *catalog.GroupCache, relations common.Relations, localID uuid.UUID, timeout time.Duration, log *zap.SugaredLogger) *Service {
	return &Service{store: store, cache: cache, relations: relations, localID: localID, timeout: timeout, log: log}
}

// Relations lists the relations a group can carry, "member" first.
func (s *Service) Relations() []string { return s.relations.List() }

// LocalSourceID is the source the groups maintained here belong to.
func (s *Service) LocalSourceID() uuid.UUID { return s.localID }

func (s *Service) checkRelation(relation string) (string, error) {
	rel, err := s.relations.Check(relation)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRelation, err)
	}
	return rel, nil
}

func (s *Service) ctx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, s.timeout)
}

// refreshCache best-effort reloads the group search cache after a catalog change
// (manual group create/update/delete) so edits are searchable immediately,
// without waiting for the background backstop refresh. Non-fatal.
func (s *Service) refreshCache(ctx context.Context) {
	if s.cache == nil {
		return
	}
	if err := s.cache.Refresh(ctx); err != nil {
		s.log.Warnw("group cache refresh failed", zap.Error(err))
	}
}

// record writes one entry of a group's history. The change itself is made by then, so a history that cannot be written is logged rather than turned into an error the caller would read as "nothing happened".
func (s *Service) record(ctx context.Context, caller Caller, c common.Change) {
	c.Actor = caller.Actor
	c.OnBehalfOf = caller.OnBehalfOf
	if err := s.store.AddChange(ctx, &c); err != nil {
		s.log.Errorw("group history not written", "group", c.GroupID, "action", c.Action, "actor", c.Actor, zap.Error(err))
	}
}

// sourceNames maps every source to its name, for the name shown next to a group.
func (s *Service) sourceNames(ctx context.Context) map[uuid.UUID]string {
	names := map[uuid.UUID]string{}
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		s.log.Warnw("source names unavailable", zap.Error(err))
		return names
	}
	for _, src := range sources {
		names[src.ID] = src.Name
	}
	return names
}

// withSourceNames fills SourceName in each group.
func (s *Service) withSourceNames(ctx context.Context, groups []common.Group) []common.Group {
	names := s.sourceNames(ctx)
	for i := range groups {
		if groups[i].SourceID != nil {
			groups[i].SourceName = names[*groups[i].SourceID]
		}
	}
	return groups
}

// ── Groups ────────────────────────────────────────────────────────────────────

// CreateGroup creates a group of the local source. Admins only; managers, if given, may maintain it from then on.
func (s *Service) CreateGroup(ctx context.Context, caller Caller, id, displayName, description string, managers []string) (*common.Group, error) {
	if !caller.Admin {
		return nil, fmt.Errorf("%w: only admins create groups", ErrForbidden)
	}
	// Strip prefix if caller accidentally sent "group:foo".
	id = common.NormalizeID(strings.TrimPrefix(strings.TrimSpace(id), common.GroupPrefix))
	if err := common.ValidateGroupID(id); err != nil {
		return nil, err
	}

	ctx, cancel := s.ctx(ctx)
	defer cancel()

	if err := s.checkNotReserved(ctx, id); err != nil {
		return nil, err
	}
	managerTokens := make([]string, 0, len(managers))
	for _, m := range managers {
		token, err := s.checkManagerToken(ctx, m)
		if err != nil {
			return nil, err
		}
		managerTokens = append(managerTokens, token)
	}

	localID := s.localID
	g := &common.Group{
		ID:          id,
		Token:       common.GroupPrefix + id,
		DisplayName: ifEmpty(displayName, id),
		Description: description,
		SourceID:    &localID,
	}
	if err := s.store.CreateGroup(ctx, g); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "already exists") {
			return nil, fmt.Errorf("%w: group '%s'", ErrAlreadyExists, id)
		}
		return nil, err
	}
	s.record(ctx, caller, common.Change{GroupID: id, Action: common.ChangeGroupCreated, Detail: g.DisplayName})
	for _, token := range managerTokens {
		if err := s.store.AddManager(ctx, id, common.Manager{Token: token, CreatedBy: caller.Actor}); err != nil {
			return nil, err
		}
		s.record(ctx, caller, common.Change{GroupID: id, Action: common.ChangeManagerAdded, Member: token})
	}
	s.refreshCache(ctx)
	g.SourceName = s.sourceNames(ctx)[localID]
	return g, nil
}

// checkNotReserved refuses an id another source has reserved, naming the source.
func (s *Service) checkNotReserved(ctx context.Context, id string) error {
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.ID == s.localID {
			continue
		}
		for _, glob := range src.ReservedNames {
			if common.MatchGlob(glob, id) {
				return fmt.Errorf("%w: group id %q is reserved for source %q (%s)", ErrReserved, id, src.Name, glob)
			}
		}
	}
	return nil
}

func (s *Service) GetGroup(ctx context.Context, token string) (*common.Group, error) {
	_, id, err := parseGroupToken(token)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.getGroup(ctx, id, token)
	if err != nil {
		return nil, err
	}
	return &s.withSourceNames(ctx, []common.Group{*g})[0], nil
}

func (s *Service) getGroup(ctx context.Context, id, token string) (*common.Group, error) {
	g, err := s.store.GetGroup(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: group '%s'", ErrNotFound, token)
		}
		return nil, err
	}
	return g, nil
}

func (s *Service) ListGroups(ctx context.Context, query string, sourceIDStr string, limit int) ([]common.Group, error) {
	// Group search (query, no source filter) is served from the in-memory cache
	// so type-ahead lookups never hit the store. The source-scoped variant keeps
	// the store's tuple-aware semantics ("groups with a tuple from this source").
	if sourceIDStr == "" && s.cache != nil {
		return s.cache.Search(query, limit), nil
	}

	ctx, cancel := s.ctx(ctx)
	defer cancel()

	var sourceID *uuid.UUID
	if sourceIDStr != "" {
		id, err := uuid.Parse(sourceIDStr)
		if err != nil {
			return nil, fmt.Errorf("invalid source_id: %w", err)
		}
		sourceID = &id
	}
	groups, err := s.store.ListGroups(ctx, query, sourceID, limit)
	if err != nil {
		return nil, err
	}
	return s.withSourceNames(ctx, groups), nil
}

// localGroup returns a group of the local source that the caller may maintain: ErrNotFound, ErrOwnedBySource naming the source, or ErrForbidden otherwise.
func (s *Service) localGroup(ctx context.Context, caller Caller, token string) (*common.Group, error) {
	_, id, err := parseGroupToken(token)
	if err != nil {
		return nil, err
	}
	g, err := s.getGroup(ctx, id, token)
	if err != nil {
		return nil, err
	}
	if g.SourceID == nil || *g.SourceID != s.localID {
		name := "another source"
		if g.SourceID != nil {
			if n := s.sourceNames(ctx)[*g.SourceID]; n != "" {
				name = fmt.Sprintf("source %q", n)
			}
		}
		return nil, fmt.Errorf("%w: group '%s' is maintained by %s; change it there", ErrOwnedBySource, token, name)
	}
	ok, err := s.mayManage(ctx, caller, g.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: group '%s' is not one you maintain", ErrForbidden, token)
	}
	return g, nil
}

// mayManage reports whether the caller may maintain the group: an admin, or one of the caller's tokens is among its managers.
func (s *Service) mayManage(ctx context.Context, caller Caller, groupID string) (bool, error) {
	if caller.Admin {
		return true, nil
	}
	managers, err := s.store.ListManagers(ctx, groupID)
	if err != nil {
		return false, err
	}
	for _, m := range managers {
		if slices.Contains(caller.Tokens, m.Token) {
			return true, nil
		}
	}
	return false, nil
}

// UpdateGroup changes the display name and description of a group the caller maintains.
func (s *Service) UpdateGroup(ctx context.Context, caller Caller, token, displayName, description string) (*common.Group, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, token)
	if err != nil {
		return nil, err
	}
	displayName = ifEmpty(displayName, g.ID)
	if err := s.store.UpdateGroup(ctx, g.ID, displayName, description); err != nil {
		return nil, err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeGroupUpdated, Detail: displayName + " · " + description})
	s.refreshCache(ctx)
	updated, err := s.store.GetGroup(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	return &s.withSourceNames(ctx, []common.Group{*updated})[0], nil
}

// DeleteGroup deletes a group of the local source, with its members, its memberships in other groups and its managers. Admins only: other groups and every consumer's rules may name it.
func (s *Service) DeleteGroup(ctx context.Context, caller Caller, token string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	// A source's group is exactly its latest data: deleted here, the next run would bring it back. It goes away by leaving the source.
	g, err := s.localGroup(ctx, caller, token)
	if err != nil {
		return err
	}
	if !caller.Admin {
		return fmt.Errorf("%w: only admins delete groups", ErrForbidden)
	}
	if err := s.store.DeleteGroup(ctx, g.ID); err != nil {
		return err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeGroupDeleted, Detail: g.DisplayName})
	s.refreshCache(ctx)
	return nil
}

// ManagedGroups lists the local groups the caller may maintain — every one for an admin — optionally narrowed by a search query.
func (s *Service) ManagedGroups(ctx context.Context, caller Caller, query string) ([]common.Group, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()

	managed := map[string]bool{}
	if !caller.Admin {
		ids, err := s.store.ManagedGroups(ctx, caller.Tokens)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []common.Group{}, nil
		}
		for _, id := range ids {
			managed[id] = true
		}
	}

	var candidates []common.Group
	if s.cache != nil {
		candidates = s.cache.Search(query, 0)
	} else {
		var err error
		if candidates, err = s.store.ListGroups(ctx, query, &s.localID, 0); err != nil {
			return nil, err
		}
		candidates = s.withSourceNames(ctx, candidates)
	}
	out := []common.Group{}
	for _, g := range candidates {
		// The search also offers each relation as an entry of its own; maintaining is per group.
		if strings.Contains(g.ID, common.RelationSeparator) || g.SourceID == nil || *g.SourceID != s.localID {
			continue
		}
		if caller.Admin || managed[g.ID] {
			out = append(out, g)
		}
	}
	return out, nil
}

// Adopt hands every group and membership of another source — from nil: those of no source — to the local source, records it in each group's history, and returns the groups moved.
func (s *Service) Adopt(ctx context.Context, caller Caller, from *uuid.UUID, detail string) ([]string, error) {
	ids, err := s.store.MoveToSource(ctx, from, s.localID)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		s.record(ctx, caller, common.Change{GroupID: id, Action: common.ChangeGroupAdopted, Detail: detail})
	}
	if len(ids) > 0 {
		s.refreshCache(ctx)
	}
	return ids, nil
}

// ── Members ───────────────────────────────────────────────────────────────────

// AddMember adds a member (user, group or pattern token) to a group the caller maintains, with the given relation ("" = member). expiresAt, if set, must lie in the future; adding a member that exists sets its expiry.
func (s *Service) AddMember(ctx context.Context, caller Caller, groupToken, relation, memberToken string, expiresAt *time.Time) error {
	relation, err := s.checkRelation(relation)
	if err != nil {
		return err
	}
	memberType, memberID, err := parseMemberToken(memberToken)
	if err != nil {
		return err
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return fmt.Errorf("%w: %s is not in the future", ErrInvalidExpiry, expiresAt.Format(time.RFC3339))
	}

	ctx, cancel := s.ctx(ctx)
	defer cancel()
	// The group must exist and belong to the local source: a source's groups
	// are exactly its latest data, and a member added here would either be
	// swept away by its next run or outlive it unnoticed.
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return err
	}
	if memberType == "group" {
		if err := s.checkNoCycle(ctx, g.ID, memberID); err != nil {
			return err
		}
	}
	localID := s.localID
	if err := s.store.AddMember(ctx, g.ID, relation, memberType, memberID, &localID, expiresAt); err != nil {
		return err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeMemberAdded, Member: common.BuildToken(memberType, memberID), Relation: relation, ExpiresAt: expiresAt})
	return nil
}

// checkNoCycle refuses a group as member of a group it already contains, directly or further down: the two would then contain each other.
func (s *Service) checkNoCycle(ctx context.Context, groupID, memberID string) error {
	if groupID == memberID {
		return fmt.Errorf("%w: group '%s' cannot contain itself", ErrCycle, groupID)
	}
	below, err := s.store.GetAllMembers(ctx, memberID, "")
	if err != nil {
		return err
	}
	if slices.Contains(below, common.GroupPrefix+groupID) {
		return fmt.Errorf("%w: group '%s' already contains group '%s'", ErrCycle, memberID, groupID)
	}
	return nil
}

// RemoveMember removes a member's relation ("" = member) from a group the caller maintains.
func (s *Service) RemoveMember(ctx context.Context, caller Caller, groupToken, relation, memberToken string) error {
	relation, err := s.checkRelation(relation)
	if err != nil {
		return err
	}
	memberType, memberID, err := parseMemberToken(memberToken)
	if err != nil {
		return err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return err
	}
	if err := s.store.RemoveMember(ctx, g.ID, relation, memberType, memberID); err != nil {
		return err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeMemberRemoved, Member: common.BuildToken(memberType, memberID), Relation: relation})
	return nil
}

// ChangeRelation gives an entry of a group the caller maintains another relation, in one step: the person never holds both or neither. Expiry and source stay; if the member holds the new relation already, the two entries become one.
func (s *Service) ChangeRelation(ctx context.Context, caller Caller, groupToken, memberToken, from, to string) error {
	from, err := s.checkRelation(from)
	if err != nil {
		return err
	}
	if to, err = s.checkRelation(to); err != nil {
		return err
	}
	memberType, memberID, err := parseMemberToken(memberToken)
	if err != nil {
		return err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if err := s.store.ChangeRelation(ctx, g.ID, memberType, memberID, from, to); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: %s holds no relation %q in group '%s'", ErrNotFound, memberToken, from, g.ID)
		}
		return err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeRelationChanged, Member: common.BuildToken(memberType, memberID), Relation: to, Detail: from})
	return nil
}

// GetAllMembers returns the user+group members holding relation ("" = member,
// i.e. everyone in the group), transitively or direct only.
func (s *Service) GetAllMembers(ctx context.Context, groupToken, relation string, recursive bool) ([]string, error) {
	_, groupID, err := parseGroupToken(groupToken)
	if err != nil {
		return nil, err
	}
	relation, err = s.checkRelation(relation)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if recursive {
		return s.store.GetAllMembers(ctx, groupID, relation)
	}
	return s.store.GetDirectMembers(ctx, groupID, relation)
}

// GroupDetail is a group as the people maintaining it see it.
type GroupDetail struct {
	Group       common.Group        `json:"group"`
	Memberships []common.Membership `json:"memberships"`
	Managers    []common.Manager    `json:"managers"`
	// MayDelete: the caller may delete the group (admins).
	MayDelete bool `json:"may_delete"`
}

// Detail returns a group the caller maintains with its direct entries and its managers.
func (s *Service) Detail(ctx context.Context, caller Caller, groupToken string) (*GroupDetail, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return nil, err
	}
	memberships, err := s.store.ListMemberships(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	managers, err := s.store.ListManagers(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	return &GroupDetail{
		Group:       s.withSourceNames(ctx, []common.Group{*g})[0],
		Memberships: memberships,
		Managers:    managers,
		MayDelete:   caller.Admin,
	}, nil
}

// EffectiveMembers returns everyone a group the caller maintains counts in the end, sub-groups resolved and expired entries left out. Pattern rules are not people and not listed.
func (s *Service) EffectiveMembers(ctx context.Context, caller Caller, groupToken string) ([]string, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return nil, err
	}
	members, err := s.store.GetAllMembers(ctx, g.ID, "")
	if err != nil {
		return nil, err
	}
	users := []string{}
	for _, m := range members {
		if strings.HasPrefix(m, common.UserPrefix) {
			users = append(users, m)
		}
	}
	return users, nil
}

// History returns the most recent changes of a group the caller maintains.
func (s *Service) History(ctx context.Context, caller Caller, groupToken string, limit int) ([]common.Change, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return nil, err
	}
	return s.store.ListChanges(ctx, g.ID, limit)
}

// ── Managers ──────────────────────────────────────────────────────────────────

// checkManagerToken normalizes a manager token and checks it names a person or an existing group, optionally with a configured relation.
func (s *Service) checkManagerToken(ctx context.Context, token string) (string, error) {
	typ, id, err := parseMemberToken(token)
	if err != nil {
		// parseMemberToken does not know relations; a group token with one is checked below.
		t, raw := common.ParseToken(strings.TrimSpace(token))
		if t != "group" {
			return "", err
		}
		typ, id = t, common.NormalizeID(raw)
	}
	switch typ {
	case "user":
		return common.BuildToken(typ, id), nil
	case "group":
		group, relation := common.SplitGroupRelation(id)
		if err := common.ValidateGroupID(group); err != nil {
			return "", err
		}
		if relation, err = s.checkRelation(relation); err != nil {
			return "", err
		}
		if _, err := s.getGroup(ctx, group, common.GroupPrefix+group); err != nil {
			return "", err
		}
		return common.GroupToken(group, relation), nil
	default:
		return "", fmt.Errorf("%w: a manager is a person (user:<email>) or a group (group:<id>, group:<id>#<relation>)", common.ErrInvalidID)
	}
}

// AddManager lets another person or group maintain a group the caller maintains.
func (s *Service) AddManager(ctx context.Context, caller Caller, groupToken, managerToken string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return err
	}
	token, err := s.checkManagerToken(ctx, managerToken)
	if err != nil {
		return err
	}
	if err := s.store.AddManager(ctx, g.ID, common.Manager{Token: token, CreatedBy: caller.Actor}); err != nil {
		return err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeManagerAdded, Member: token})
	return nil
}

// RemoveManager takes the right to maintain a group away from a token. Removing the caller's own last way in is allowed; an admin can always give it back.
func (s *Service) RemoveManager(ctx context.Context, caller Caller, groupToken, managerToken string) error {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.localGroup(ctx, caller, groupToken)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(managerToken)
	if t, id := common.ParseToken(token); t != "" {
		token = common.BuildToken(t, common.NormalizeID(id))
	}
	if err := s.store.RemoveManager(ctx, g.ID, token); err != nil {
		return err
	}
	s.record(ctx, caller, common.Change{GroupID: g.ID, Action: common.ChangeManagerRemoved, Member: token})
	return nil
}

// ── Search ────────────────────────────────────────────────────────────────────

// Principals is what a member or manager field may be filled with.
type Principals struct {
	Groups []common.Group `json:"groups"`
	Users  []string       `json:"users"`
}

// SearchPrincipals searches groups of every source (each relation as an entry of its own) and addresses, the latter only once the query is not empty — the directory is not browsable. For people who maintain at least one group, or admins.
func (s *Service) SearchPrincipals(ctx context.Context, caller Caller, query string, limit int) (*Principals, error) {
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if !caller.Admin {
		ids, err := s.store.ManagedGroups(ctx, caller.Tokens)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%w: searching is for people who maintain a group", ErrForbidden)
		}
	}
	var groups []common.Group
	if s.cache != nil {
		groups = s.cache.Search(query, limit)
	} else {
		var err error
		if groups, err = s.store.ListGroups(ctx, query, nil, limit); err != nil {
			return nil, err
		}
		groups = s.withSourceNames(ctx, groups)
	}
	users := []string{}
	if strings.TrimSpace(query) != "" {
		var err error
		if users, err = s.store.SearchUsers(ctx, query, limit); err != nil {
			return nil, err
		}
	}
	if groups == nil {
		groups = []common.Group{}
	}
	return &Principals{Groups: groups, Users: users}, nil
}

// ── User token resolution (RoleProvider) ─────────────────────────────────────

// SetUserLookup registers a function run before a user's tokens are resolved,
// which may add memberships for someone no source knew yet (the Keycloak
// source, at a person's first sign-in).
func (s *Service) SetUserLookup(fn func(ctx context.Context, email string)) {
	s.userLookup = fn
}

// GetUserTokens returns all tokens (user: + group:) for a given email.
func (s *Service) GetUserTokens(ctx context.Context, email string) ([]string, error) {
	email = common.NormalizeID(email)
	if email == "" {
		return nil, fmt.Errorf("email must not be empty")
	}
	if s.userLookup != nil {
		s.userLookup(ctx, email)
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	return s.store.GetUserTokens(ctx, email)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func parseGroupToken(token string) (prefix, id string, err error) {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, common.GroupPrefix) {
		return common.GroupPrefix, common.NormalizeID(strings.TrimPrefix(token, common.GroupPrefix)), nil
	}
	// Accept bare ID without prefix for URL path params.
	if token != "" && !strings.Contains(token, ":") {
		return common.GroupPrefix, common.NormalizeID(token), nil
	}
	return "", "", fmt.Errorf("%w: got '%s'", ErrInvalidToken, token)
}

func parseMemberToken(token string) (typ, id string, err error) {
	token = strings.TrimSpace(token)
	t, i := common.ParseToken(token)
	if t == "" {
		return "", "", fmt.Errorf("%w: got '%s'", ErrInvalidToken, token)
	}
	i = common.NormalizeID(i)
	if err := common.ValidateMember(t, i); err != nil {
		return "", "", err
	}
	return t, i, nil
}

func ifEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
