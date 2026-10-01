package groupmgmt

import (
	"context"
	"errors"
	"fmt"
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

// ErrInvalidToken is returned when a token string has an invalid format.
var ErrInvalidToken = fmt.Errorf("invalid token: must start with '%s' or '%s'", common.GroupPrefix, common.UserPrefix)

// ErrInvalidRelation is returned for a relation that is not configured.
var ErrInvalidRelation = errors.New("invalid relation")

// Service handles all group and member management operations.
type Service struct {
	store     storage.Store
	cache     *catalog.GroupCache
	relations common.Relations
	timeout   time.Duration
	log       *zap.SugaredLogger
	// userLookup, if set, runs before a user's tokens are resolved.
	userLookup func(ctx context.Context, email string)
}

func NewService(store storage.Store, cache *catalog.GroupCache, relations common.Relations, timeout time.Duration, log *zap.SugaredLogger) *Service {
	return &Service{store: store, cache: cache, relations: relations, timeout: timeout, log: log}
}

// Relations lists the relations a group can carry, "member" first.
func (s *Service) Relations() []string { return s.relations.List() }

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

// ── Groups ────────────────────────────────────────────────────────────────────

func (s *Service) CreateGroup(ctx context.Context, id, displayName, description string) (*common.Group, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("group id must not be empty")
	}
	// Strip prefix if caller accidentally sent "group:foo".
	id = common.NormalizeID(strings.TrimPrefix(id, common.GroupPrefix))

	ctx, cancel := s.ctx(ctx)
	defer cancel()

	g := &common.Group{
		ID:          id,
		Token:       common.GroupPrefix + id,
		DisplayName: ifEmpty(displayName, id),
		Description: description,
	}
	if err := s.store.CreateGroup(ctx, g); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return nil, fmt.Errorf("%w: group '%s'", ErrAlreadyExists, id)
		}
		return nil, err
	}
	s.refreshCache(ctx)
	return g, nil
}

func (s *Service) GetGroup(ctx context.Context, token string) (*common.Group, error) {
	_, id, err := parseGroupToken(token)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
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
	return s.store.ListGroups(ctx, query, sourceID, limit)
}

func (s *Service) UpdateGroup(ctx context.Context, token, displayName, description string) (*common.Group, error) {
	_, id, err := parseGroupToken(token)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()

	if err := s.store.UpdateGroup(ctx, id, displayName, description); err != nil {
		return nil, err
	}
	s.refreshCache(ctx)
	return s.store.GetGroup(ctx, id)
}

func (s *Service) DeleteGroup(ctx context.Context, token string) error {
	_, id, err := parseGroupToken(token)
	if err != nil {
		return err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if err := s.store.DeleteGroup(ctx, id); err != nil {
		return err
	}
	s.refreshCache(ctx)
	return nil
}

// ── Members ───────────────────────────────────────────────────────────────────

// AddMember adds a member (user or group token) to the target group with the
// given relation ("" = member). memberToken must be "user:<email>" or "group:<name>".
func (s *Service) AddMember(ctx context.Context, groupToken, relation, memberToken string) error {
	_, groupID, err := parseGroupToken(groupToken)
	if err != nil {
		return err
	}
	relation, err = s.checkRelation(relation)
	if err != nil {
		return err
	}
	memberType, memberID, err := parseMemberToken(memberToken)
	if err != nil {
		return err
	}

	// Ensure the group exists, and that no source owns it: a source's groups
	// are exactly its latest data, and a member added here would either be
	// swept away by its next run or outlive it unnoticed.
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	g, err := s.store.GetGroup(ctx, groupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: group '%s'", ErrNotFound, groupToken)
		}
		return err
	}
	if g.SourceID != nil {
		return fmt.Errorf("%w: group '%s' is maintained by a sync source; change it there", ErrOwnedBySource, groupToken)
	}
	return s.store.AddMember(ctx, groupID, relation, memberType, memberID, nil)
}

// RemoveMember removes a member's relation ("" = member) from the target group.
func (s *Service) RemoveMember(ctx context.Context, groupToken, relation, memberToken string) error {
	_, groupID, err := parseGroupToken(groupToken)
	if err != nil {
		return err
	}
	relation, err = s.checkRelation(relation)
	if err != nil {
		return err
	}
	memberType, memberID, err := parseMemberToken(memberToken)
	if err != nil {
		return err
	}
	ctx, cancel := s.ctx(ctx)
	defer cancel()
	if g, err := s.store.GetGroup(ctx, groupID); err == nil && g.SourceID != nil {
		return fmt.Errorf("%w: group '%s' is maintained by a sync source; change it there", ErrOwnedBySource, groupToken)
	}
	return s.store.RemoveMember(ctx, groupID, relation, memberType, memberID)
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
	return t, common.NormalizeID(i), nil
}

func ifEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
