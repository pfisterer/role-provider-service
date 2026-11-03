package storage

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
)

// An expired entry grants nothing anywhere membership is resolved, stays listed as expired, and adding it again sets a new expiry.
func TestExpiredTuplesDoNotCount(t *testing.T) {
	ctx := context.Background()
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			must(t, s.CreateGroup(ctx, &common.Group{ID: "kurs"}))
			must(t, s.AddMember(ctx, "kurs", "dozent", "user", "anna@x", nil, &past))
			must(t, s.AddMember(ctx, "kurs", "member", "group", "hiwis", nil, &past))
			must(t, s.AddMember(ctx, "hiwis", "member", "user", "ben@x", nil, nil))
			must(t, s.AddMember(ctx, "kurs", "member", "pattern", "*@student.x", nil, &past))
			must(t, s.AddMember(ctx, "kurs", "member", "user", "carla@x", nil, &future))

			for _, email := range []string{"anna@x", "ben@x", "dora@student.x"} {
				toks, err := s.GetUserTokens(ctx, email)
				must(t, err)
				if slices.Contains(toks, "group:kurs") || slices.Contains(toks, "group:kurs#dozent") {
					t.Errorf("%s still holds the expired entry: %v", email, toks)
				}
			}
			if toks, _ := s.GetUserTokens(ctx, "carla@x"); !slices.Contains(toks, "group:kurs") {
				t.Errorf("an entry not yet expired does not count: %v", toks)
			}
			all, err := s.GetAllMembers(ctx, "kurs", "member")
			must(t, err)
			if !slices.Equal(all, []string{"user:carla@x"}) {
				t.Errorf("resolved members = %v", all)
			}
			direct, err := s.GetDirectMembers(ctx, "kurs", "member")
			must(t, err)
			if !slices.Equal(direct, []string{"user:carla@x"}) {
				t.Errorf("direct members = %v", direct)
			}
			if rels, _ := s.GroupRelations(ctx); len(rels["kurs"]) != 0 {
				t.Errorf("relations held only by expired entries: %v", rels)
			}

			listed, err := s.ListMemberships(ctx, "kurs")
			must(t, err)
			expired := 0
			for _, m := range listed {
				if m.Expired {
					expired++
				}
			}
			if len(listed) != 4 || expired != 3 {
				t.Errorf("listed %d entries, %d expired: %+v", len(listed), expired, listed)
			}

			// Adding again extends; nil makes it permanent.
			must(t, s.AddMember(ctx, "kurs", "dozent", "user", "anna@x", nil, nil))
			if toks, _ := s.GetUserTokens(ctx, "anna@x"); !slices.Contains(toks, "group:kurs#dozent") {
				t.Errorf("the renewed entry does not count: %v", toks)
			}
		})
	}
}

// Moving hands groups and tuples from one source to another; nil moves those of no source.
func TestMoveToSource(t *testing.T) {
	ctx := context.Background()
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			csv, local := uuid.New(), uuid.New()
			_, _, err := s.ReplaceTuples(ctx, csv, []common.TuplePair{
				{GroupID: "root-admin", MemberType: "user", MemberID: "a@x"},
				{GroupID: "sg-inf", MemberType: "group", MemberID: "root-admin"},
			}, nil)
			must(t, err)
			must(t, s.CreateGroup(ctx, &common.Group{ID: "old-api-group"}))
			must(t, s.AddMember(ctx, "old-api-group", "", "user", "b@x", nil, nil))

			moved, err := s.MoveToSource(ctx, &csv, local)
			must(t, err)
			if !slices.Equal(moved, []string{"root-admin", "sg-inf"}) {
				t.Errorf("moved %v", moved)
			}
			moved, err = s.MoveToSource(ctx, nil, local)
			must(t, err)
			if !slices.Equal(moved, []string{"old-api-group"}) {
				t.Errorf("moved of no source %v", moved)
			}
			owners, _ := s.GroupOwners(ctx, []string{"root-admin", "sg-inf", "old-api-group"})
			for id, owner := range owners {
				if owner == nil || *owner != local {
					t.Errorf("%s belongs to %v", id, owner)
				}
			}
			// The old source owns nothing any more: replacing its tuples with none removes nothing.
			_, removed, err := s.ReplaceTuples(ctx, csv, nil, nil)
			must(t, err)
			if removed != 0 {
				t.Errorf("the old source still owned %d tuples", removed)
			}
			if toks, _ := s.GetUserTokens(ctx, "a@x"); !slices.Contains(toks, "group:sg-inf") {
				t.Errorf("tokens lost in the move: %v", toks)
			}
		})
	}
}

// Managers are listed, found by token and gone with their group — also where the deleted group was itself a manager.
func TestManagersAndHistory(t *testing.T) {
	ctx := context.Background()
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			for _, id := range []string{"kurs", "dekane", "other"} {
				must(t, s.CreateGroup(ctx, &common.Group{ID: id}))
			}
			must(t, s.AddManager(ctx, "kurs", common.Manager{Token: "user:a@x", CreatedBy: "root@x"}))
			must(t, s.AddManager(ctx, "kurs", common.Manager{Token: "user:a@x"}))
			must(t, s.AddManager(ctx, "other", common.Manager{Token: "group:dekane#leitung"}))
			must(t, s.AddManager(ctx, "kurs", common.Manager{Token: "group:dekane"}))

			ms, err := s.ListManagers(ctx, "kurs")
			must(t, err)
			if len(ms) != 2 || ms[0].Token != "group:dekane" || ms[1].CreatedBy != "root@x" {
				t.Errorf("managers = %+v", ms)
			}
			ids, err := s.ManagedGroups(ctx, []string{"user:a@x", "group:dekane#leitung"})
			must(t, err)
			if !slices.Equal(ids, []string{"kurs", "other"}) {
				t.Errorf("managed = %v", ids)
			}

			must(t, s.DeleteGroup(ctx, "dekane"))
			if ids, _ := s.ManagedGroups(ctx, []string{"group:dekane", "group:dekane#leitung"}); len(ids) != 0 {
				t.Errorf("the deleted group still manages %v", ids)
			}
			must(t, s.RemoveManager(ctx, "kurs", "user:a@x"))
			if ms, _ := s.ListManagers(ctx, "kurs"); len(ms) != 0 {
				t.Errorf("managers after removal = %+v", ms)
			}

			must(t, s.AddChange(ctx, &common.Change{GroupID: "kurs", Actor: "a@x", Action: common.ChangeMemberAdded, Member: "user:b@x"}))
			must(t, s.AddChange(ctx, &common.Change{GroupID: "kurs", Actor: "a@x", Action: common.ChangeMemberRemoved, Member: "user:b@x"}))
			changes, err := s.ListChanges(ctx, "kurs", 1)
			must(t, err)
			if len(changes) != 1 || changes[0].Action != common.ChangeMemberRemoved {
				t.Errorf("latest change = %+v", changes)
			}
		})
	}
}

// Reserved names survive a round trip through the store.
func TestSourceReservedNames(t *testing.T) {
	ctx := context.Background()
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			src := &common.Source{ID: uuid.New(), Name: "keycloak", Type: common.SourceTypeKeycloak, ReservedNames: []string{"dhbw", "standort-*"}}
			must(t, s.CreateSource(ctx, src))
			got, err := s.GetSource(ctx, src.ID)
			must(t, err)
			if !slices.Equal(got.ReservedNames, src.ReservedNames) {
				t.Errorf("reserved = %v", got.ReservedNames)
			}
			got.ReservedNames = nil
			must(t, s.UpdateSource(ctx, got))
			if got, _ = s.GetSource(ctx, src.ID); len(got.ReservedNames) != 0 {
				t.Errorf("reserved after clearing = %v", got.ReservedNames)
			}
		})
	}
}
