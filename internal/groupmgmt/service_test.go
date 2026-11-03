package groupmgmt

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

// admin is the caller behind a write token.
var admin = APITokenCaller()

// newTestService returns a service on an empty in-memory store with a local source named "ZWR".
func newTestService(t *testing.T, relationNames ...string) (*Service, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, err := common.NewRelations(relationNames)
	if err != nil {
		t.Fatal(err)
	}
	local := &common.Source{ID: uuid.New(), Name: "ZWR", Type: common.SourceTypeLocal}
	if err := store.CreateSource(context.Background(), local); err != nil {
		t.Fatal(err)
	}
	return NewService(store, nil, relations, local.ID, 5*time.Second, zap.NewNop().Sugar()), store
}

// person is a signed-in caller holding tokens; their own user token comes first, as from GetUserTokens.
func person(email string, tokens ...string) Caller {
	return Caller{Actor: email, Tokens: append([]string{"user:" + email}, tokens...)}
}

// A sync source's groups are exactly its latest data: the API may not add
// or remove members there, only in groups of the local source.
func TestMembersOfASourceGroupCannotBeChangedThroughTheAPI(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)

	src := &common.Source{ID: uuid.New(), Name: "dhbw-mannheim", Type: common.SourceTypeCSV}
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGroup(ctx, &common.Group{ID: "imported", SourceID: &src.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGroup(ctx, admin, "manual", "", "", nil); err != nil {
		t.Fatal(err)
	}

	err := svc.AddMember(ctx, admin, "group:imported", "", "user:a@x", nil)
	if !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("add to a source's group: got %v", err)
	} else if want := `source "dhbw-mannheim"`; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not name the source: %v", err)
	}
	if err := svc.RemoveMember(ctx, admin, "group:imported", "", "user:a@x"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("remove from a source's group: got %v", err)
	}
	if _, err := svc.UpdateGroup(ctx, admin, "group:imported", "x", "y"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("update a source's group: got %v", err)
	}
	if err := svc.AddMember(ctx, admin, "group:manual", "", "user:a@x", nil); err != nil {
		t.Errorf("add to a local group: %v", err)
	}
}

// Ids enter through the API only in the one safe shape: a comma or "#" in a group id would turn one group into several, or into a relation, wherever ids are listed or tokenized.
func TestIDsAreValidatedAtTheAPI(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)

	for _, id := range []string{"", "root-admin,kurs-x", "wwi23seb#dozent", "a b", "grüppe", "group:"} {
		if _, err := svc.CreateGroup(ctx, admin, id, "", "", nil); !errors.Is(err, common.ErrInvalidID) {
			t.Errorf("create %q: got %v", id, err)
		}
	}
	// Normalized before it is checked: prefix, case and surrounding space are not an error.
	g, err := svc.CreateGroup(ctx, admin, " group:Manual ", "", "", nil)
	if err != nil || g.ID != "manual" {
		t.Fatalf("create: %v, %+v", err, g)
	}
	if g.SourceName != "ZWR" {
		t.Errorf("a created group shows source %q, want ZWR", g.SourceName)
	}

	for _, member := range []string{"group:root-admin,kurs-x", "user:not-an-address", "user:a,b@x", "pattern:*", "pattern:*@*", "pattern:*dhbw.de"} {
		if err := svc.AddMember(ctx, admin, "group:manual", "", member, nil); !errors.Is(err, common.ErrInvalidID) {
			t.Errorf("add %q: got %v", member, err)
		}
	}
	for _, member := range []string{"user:Anna@DHBW.de", "group:other", "pattern:*@student.dhbw.de"} {
		if err := svc.AddMember(ctx, admin, "group:manual", "", member, nil); err != nil {
			t.Errorf("add %q: %v", member, err)
		}
	}
	members, _ := svc.GetAllMembers(ctx, "group:manual", "", false)
	if !slices.Contains(members, "user:anna@dhbw.de") {
		t.Errorf("members = %v", members)
	}
}

// A source's group comes back with the next sync, so the API refuses to delete it; a local group is deleted with its memberships.
func TestDeleteGroup(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)

	src := uuid.New()
	if err := store.CreateGroup(ctx, &common.Group{ID: "imported", SourceID: &src}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(ctx, "imported", "", "user", "a@x", &src, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, admin, "group:imported"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("delete a source's group: got %v", err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "a@x"); !slices.Contains(toks, "group:imported") {
		t.Errorf("the refused delete changed something: %v", toks)
	}

	if _, err := svc.CreateGroup(ctx, admin, "manual", "", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, admin, "group:manual", "", "user:b@x", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, admin, "group:manual"); err != nil {
		t.Fatal(err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "b@x"); slices.Contains(toks, "group:manual") {
		t.Errorf("former member still has the group: %v", toks)
	}
	if err := svc.DeleteGroup(ctx, admin, "group:manual"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete a missing group: got %v", err)
	}
}

// A manager maintains the groups they are named for — as a person, through a group, or through a relation of a group — and nothing else; creating and deleting is for admins. Maintaining a group grants none of its rights.
func TestManagersMaintainTheirGroupsOnly(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, "lehrende")

	for _, id := range []string{"wwi23seb", "wwi23seb-kurs", "other", "studiendekane", "standort-ma"} {
		if _, err := svc.CreateGroup(ctx, admin, id, "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.AddManager(ctx, admin, "group:wwi23seb", "user:Dekan@x"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddManager(ctx, admin, "group:wwi23seb-kurs", "group:studiendekane"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddManager(ctx, admin, "group:other", "group:standort-ma#lehrende"); err != nil {
		t.Fatal(err)
	}

	dekan := person("dekan@x")
	viaGroup := person("b@x", "group:studiendekane")
	viaRelation := person("c@x", "group:standort-ma", "group:standort-ma#lehrende")
	nobody := person("d@x", "group:standort-ma")

	cases := []struct {
		caller Caller
		group  string
		may    bool
	}{
		{dekan, "wwi23seb", true}, {dekan, "wwi23seb-kurs", false},
		{viaGroup, "wwi23seb-kurs", true}, {viaGroup, "wwi23seb", false},
		{viaRelation, "other", true}, {nobody, "other", false},
	}
	for _, tc := range cases {
		err := svc.AddMember(ctx, tc.caller, "group:"+tc.group, "", "user:new@x", nil)
		if tc.may && err != nil {
			t.Errorf("%s may maintain %s: %v", tc.caller.Actor, tc.group, err)
		}
		if !tc.may && !errors.Is(err, ErrForbidden) {
			t.Errorf("%s may not maintain %s: got %v", tc.caller.Actor, tc.group, err)
		}
	}

	if _, err := svc.CreateGroup(ctx, dekan, "mine", "", "", nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("a manager created a group: %v", err)
	}
	if err := svc.DeleteGroup(ctx, dekan, "group:wwi23seb"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a manager deleted a group: %v", err)
	}

	// Being a manager is not being a member.
	if toks, _ := svc.GetUserTokens(ctx, "dekan@x"); slices.Contains(toks, "group:wwi23seb") {
		t.Errorf("maintaining a group made the manager a member: %v", toks)
	}

	groups, err := svc.ManagedGroups(ctx, dekan, "")
	if err != nil || len(groups) != 1 || groups[0].ID != "wwi23seb" {
		t.Errorf("the dekan's groups = %v, %v", groups, err)
	}
	if all, _ := svc.ManagedGroups(ctx, admin, ""); len(all) != 5 {
		t.Errorf("an admin maintains every local group, got %d", len(all))
	}

	// A manager may hand on the right and search for whom to hand it to; somebody maintaining nothing may not search.
	if err := svc.AddManager(ctx, dekan, "group:wwi23seb", "user:deputy@x"); err != nil {
		t.Errorf("a manager added a co-manager: %v", err)
	}
	if _, err := svc.SearchPrincipals(ctx, dekan, "x", 10); err != nil {
		t.Errorf("a manager searched: %v", err)
	}
	if _, err := svc.SearchPrincipals(ctx, person("e@x"), "x", 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("somebody maintaining nothing searched: %v", err)
	}
	if err := svc.AddManager(ctx, dekan, "group:wwi23seb", "group:does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a manager naming a missing group: %v", err)
	}
	if err := svc.AddManager(ctx, dekan, "group:wwi23seb", "group:standort-ma#unknown"); !errors.Is(err, ErrInvalidRelation) {
		t.Errorf("a manager naming an unknown relation: %v", err)
	}
}

// An entry with an expiry counts until then and grants nothing afterwards, but stays listed — marked — until somebody removes it.
func TestExpiringMemberships(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	if _, err := svc.CreateGroup(ctx, admin, "hiwis", "", "", nil); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Hour)
	if err := svc.AddMember(ctx, admin, "group:hiwis", "", "user:a@x", &past); !errors.Is(err, ErrInvalidExpiry) {
		t.Errorf("an expiry in the past: got %v", err)
	}

	soon := time.Now().Add(time.Hour)
	if err := svc.AddMember(ctx, admin, "group:hiwis", "", "user:a@x", &soon); err != nil {
		t.Fatal(err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "a@x"); !slices.Contains(toks, "group:hiwis") {
		t.Errorf("a membership not yet expired does not count: %v", toks)
	}

	// Let it run out, as time would.
	if err := store.AddMember(ctx, "hiwis", "", "user", "a@x", nil, &past); err != nil {
		t.Fatal(err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "a@x"); slices.Contains(toks, "group:hiwis") {
		t.Errorf("an expired membership still counts: %v", toks)
	}
	if members, _ := svc.GetAllMembers(ctx, "group:hiwis", "", true); len(members) != 0 {
		t.Errorf("an expired member is still resolved: %v", members)
	}
	d, err := svc.Detail(ctx, admin, "group:hiwis")
	if err != nil || len(d.Memberships) != 1 || !d.Memberships[0].Expired {
		t.Errorf("the expired entry is not listed as expired: %+v, %v", d, err)
	}

	// Adding it again extends it.
	if err := svc.AddMember(ctx, admin, "group:hiwis", "", "user:a@x", &soon); err != nil {
		t.Fatal(err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "a@x"); !slices.Contains(toks, "group:hiwis") {
		t.Errorf("an extended membership does not count: %v", toks)
	}
}

// A group cannot end up containing itself, directly or further down.
func TestCyclesAreRefused(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := svc.CreateGroup(ctx, admin, id, "", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.AddMember(ctx, admin, "group:a", "", "group:b", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, admin, "group:b", "", "group:c", nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ group, member string }{{"a", "group:a"}, {"c", "group:a"}, {"b", "group:a"}} {
		if err := svc.AddMember(ctx, admin, "group:"+tc.group, "", tc.member, nil); !errors.Is(err, ErrCycle) {
			t.Errorf("%s into %s: got %v", tc.member, tc.group, err)
		}
	}
}

// A name a source reserves cannot be created here, even before the source brings it.
func TestReservedNames(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	kc := &common.Source{ID: uuid.New(), Name: "keycloak", Type: common.SourceTypeKeycloak, ReservedNames: []string{"dhbw", "standort-*"}}
	if err := store.CreateSource(ctx, kc); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"dhbw", "standort-fn"} {
		if _, err := svc.CreateGroup(ctx, admin, id, "", "", nil); !errors.Is(err, ErrReserved) {
			t.Errorf("create %q: got %v", id, err)
		}
	}
	if _, err := svc.CreateGroup(ctx, admin, "dhbw-ma", "", "", nil); err != nil {
		t.Errorf("a name the glob does not cover: %v", err)
	}
}

// Adopting a source moves its groups with their members to the local source, where they can be maintained, and every person keeps every token.
func TestAdoptSource(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	csv := &common.Source{ID: uuid.New(), Name: "dhbw-mannheim", Type: common.SourceTypeCSV}
	if err := store.CreateSource(ctx, csv); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReplaceTuples(ctx, csv.ID, []common.TuplePair{
		{GroupID: "root-admin", MemberType: "user", MemberID: "admin@x"},
		{GroupID: "sg-inf", MemberType: "group", MemberID: "root-admin"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := svc.GetUserTokens(ctx, "admin@x")

	ids, err := svc.Adopt(ctx, Caller{Actor: "system"}, &csv.ID, "taken over")
	if err != nil || !slices.Equal(ids, []string{"root-admin", "sg-inf"}) {
		t.Fatalf("adopted %v, %v", ids, err)
	}
	if err := store.DeleteSource(ctx, csv.ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := svc.GetUserTokens(ctx, "admin@x"); !slices.Equal(before, after) {
		t.Errorf("tokens changed by the adoption: %v → %v", before, after)
	}
	if err := svc.AddMember(ctx, admin, "group:root-admin", "", "user:second@x", nil); err != nil {
		t.Errorf("an adopted group cannot be maintained: %v", err)
	}
	if changes, _ := store.ListChanges(ctx, "root-admin", 0); len(changes) != 2 || changes[1].Action != common.ChangeGroupAdopted {
		t.Errorf("history = %+v", changes)
	}
}

// Every change through the service is recorded with who made it and for whom.
func TestHistory(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateGroup(ctx, admin, "kurs", "", "", []string{"user:dekan@x"}); err != nil {
		t.Fatal(err)
	}
	onBehalf := person("dekan@x")
	onBehalf.Actor, onBehalf.OnBehalfOf = "root@x", "dekan@x"
	if err := svc.AddMember(ctx, onBehalf, "group:kurs", "", "user:a@x", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveMember(ctx, person("dekan@x"), "group:kurs", "", "user:a@x"); err != nil {
		t.Fatal(err)
	}
	changes, err := svc.History(ctx, person("dekan@x"), "group:kurs", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		got = append(got, c.Action+" "+c.Actor+" "+c.OnBehalfOf+" "+c.Member)
	}
	want := []string{
		"member_removed dekan@x  user:a@x",
		"member_added root@x dekan@x user:a@x",
		"manager_added api-token  user:dekan@x",
		"group_created api-token  ",
	}
	if !slices.Equal(got, want) {
		t.Errorf("history\n got %q\nwant %q", got, want)
	}
}

// A role change is one step and one line in the history; a manager of another group may not make it.
func TestChangeRelation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, "dozent")
	if _, err := svc.CreateGroup(ctx, admin, "kurs", "", "", []string{"user:dekan@x"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, admin, "group:kurs", "", "user:anna@x", nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.ChangeRelation(ctx, person("other@x"), "group:kurs", "user:anna@x", "member", "dozent"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a non-manager changed a role: %v", err)
	}
	if err := svc.ChangeRelation(ctx, person("dekan@x"), "group:kurs", "user:anna@x", "member", "unknown"); !errors.Is(err, ErrInvalidRelation) {
		t.Errorf("an unknown relation: %v", err)
	}
	if err := svc.ChangeRelation(ctx, person("dekan@x"), "group:kurs", "user:anna@x", "member", "dozent"); err != nil {
		t.Fatal(err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "anna@x"); !slices.Contains(toks, "group:kurs#dozent") {
		t.Errorf("tokens = %v", toks)
	}
	if err := svc.ChangeRelation(ctx, person("dekan@x"), "group:kurs", "user:ben@x", "member", "dozent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such entry: %v", err)
	}
	h, _ := svc.History(ctx, admin, "group:kurs", 1)
	if len(h) != 1 || h[0].Action != common.ChangeRelationChanged || h[0].Relation != "dozent" || h[0].Detail != "member" {
		t.Errorf("history = %+v", h)
	}
}
