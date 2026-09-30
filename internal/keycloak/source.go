package keycloak

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
)

// Users is what a Source needs from Keycloak; *Client satisfies it.
type Users interface {
	ListUsers(ctx context.Context) ([]User, error)
	FindUserByEmail(ctx context.Context, email string) (*User, error)
}

// MemberAdder writes one tuple; the store satisfies it.
type MemberAdder interface {
	AddMember(ctx context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID) error
}

// Source is the Keycloak source: a full read on a schedule (Fetch, run by the
// sync engine like any import), and a single-user read for someone the last
// full read did not know yet (EnsureUser).
//
// The second exists because Keycloak creates a user at their first sign-in,
// and that same sign-in asks for their groups: without it a new person would
// hold none until the next scheduled run.
type Source struct {
	users   Users
	mapping Mapping
	store   MemberAdder
	log     *zap.SugaredLogger

	// lookupTimeout bounds a single-user read: it sits in the path of a
	// request, and a slow Keycloak must not slow every sign-in down.
	lookupTimeout time.Duration
	// retryAfter is how long an address that was looked up is not looked up
	// again — found without groups, not found, or failed.
	retryAfter time.Duration

	mu       sync.Mutex
	sourceID *uuid.UUID
	known    map[string]bool
	tried    map[string]time.Time
}

// NewSource builds the source. SetSourceID must be called before EnsureUser
// writes anything.
func NewSource(users Users, mapping Mapping, store MemberAdder, log *zap.SugaredLogger) *Source {
	return &Source{
		users:         users,
		mapping:       mapping,
		store:         store,
		log:           log,
		lookupTimeout: 3 * time.Second,
		retryAfter:    10 * time.Minute,
		known:         map[string]bool{},
		tried:         map[string]time.Time{},
	}
}

// SetSourceID names the source the single-user writes belong to, so the next
// full run owns — and may replace — them like its own.
func (s *Source) SetSourceID(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceID = &id
}

// Fetch reads every user and returns the tuples and group descriptions the
// mapping derives, plus notes on what it could not place.
func (s *Source) Fetch(ctx context.Context) ([]common.TuplePair, map[string]string, []string, error) {
	users, err := s.users.ListUsers(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	unmapped := newUnmapped()
	known := make(map[string]bool, len(users))
	var tuples []common.TuplePair
	for _, u := range users {
		email := common.NormalizeID(u.Email)
		if email == "" || !u.Enabled {
			continue
		}
		known[email] = true
		tuples = append(tuples, s.mapping.Derive(email, u.Attributes[s.mapping.Attribute], unmapped)...)
	}

	s.mu.Lock()
	s.known = known
	s.tried = map[string]time.Time{}
	s.mu.Unlock()

	notes := unmapped.Notes()
	if len(notes) > 0 {
		s.log.Warnw("keycloak sync: attribute values no rule maps", "notes", notes)
	}
	return tuples, s.mapping.Descriptions(), notes, nil
}

// EnsureUser reads one user the last full run did not know and writes their
// memberships. Best effort: a failure only means the next full run adds them.
func (s *Source) EnsureUser(ctx context.Context, email string) {
	email = common.NormalizeID(email)
	if email == "" {
		return
	}
	s.mu.Lock()
	sourceID := s.sourceID
	if sourceID == nil || s.known[email] || time.Since(s.tried[email]) < s.retryAfter {
		s.mu.Unlock()
		return
	}
	s.tried[email] = time.Now()
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, s.lookupTimeout)
	defer cancel()
	u, err := s.users.FindUserByEmail(ctx, email)
	if err != nil {
		s.log.Warnw("keycloak lookup failed", "email", email, "error", err)
		return
	}
	if u == nil || !u.Enabled {
		return
	}
	tuples := s.mapping.Derive(email, u.Attributes[s.mapping.Attribute], newUnmapped())
	for _, t := range tuples {
		if err := s.store.AddMember(ctx, t.GroupID, t.Relation, t.MemberType, t.MemberID, sourceID); err != nil {
			s.log.Warnw("keycloak lookup: write failed", "email", email, "group", t.GroupID, "error", err)
			return
		}
	}
	s.mu.Lock()
	s.known[email] = true
	s.mu.Unlock()
	if len(tuples) > 0 {
		s.log.Infow("keycloak lookup: added a user the last sync did not know", "email", email, "tuples", len(tuples))
	}
}
