package groupmgmt

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
)

// A group entered with a relation ("group:kurs#studierende") is maintained like any entry: added, listed with its relation, removed on its own; the relation must be configured.
func TestMemberGroupWithRelation(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t, "studierende", "dozierende")
	for _, id := range []string{"kurs", "orga"} {
		_, err := svc.CreateGroup(ctx, admin, id, "", "", nil)
		must(t, err)
	}
	must(t, svc.AddMember(ctx, admin, "group:kurs", "studierende", "user:stud@x", nil))
	must(t, svc.AddMember(ctx, admin, "group:kurs", "dozierende", "user:doz@x", nil))
	must(t, svc.AddMember(ctx, admin, "group:orga", "studierende", "group:Kurs#studierende", nil))

	if err := svc.AddMember(ctx, admin, "group:orga", "", "group:kurs#tutor", nil); !errors.Is(err, ErrInvalidRelation) {
		t.Errorf("unknown member relation: got %v", err)
	}
	if err := svc.AddMember(ctx, admin, "group:orga", "", "group:kurs#Bad Rel", nil); !errors.Is(err, common.ErrInvalidID) {
		t.Errorf("malformed member relation: got %v", err)
	}

	toks, err := svc.GetUserTokens(ctx, "doz@x")
	must(t, err)
	if slices.Contains(toks, "group:orga") {
		t.Errorf("the lecturer went up as a student: %v", toks)
	}
	toks, err = svc.GetUserTokens(ctx, "stud@x")
	must(t, err)
	if !slices.Contains(toks, "group:orga#studierende") {
		t.Errorf("the student did not go up: %v", toks)
	}

	detail, err := svc.Detail(ctx, admin, "group:orga")
	must(t, err)
	if len(detail.Memberships) != 1 || detail.Memberships[0].Member != "group:kurs#studierende" || detail.Memberships[0].Relation != "studierende" {
		t.Errorf("memberships %+v", detail.Memberships)
	}
	people, err := svc.EffectiveMembers(ctx, admin, "group:orga")
	must(t, err)
	if !slices.Equal(people, []string{"user:stud@x"}) {
		t.Errorf("effective members %v", people)
	}
	changes, err := svc.History(ctx, admin, "group:orga", 1)
	must(t, err)
	if len(changes) != 1 || changes[0].Member != "group:kurs#studierende" {
		t.Errorf("history %+v", changes)
	}

	// "#member" is the plain entry, a second one next to the relation entry.
	must(t, svc.AddMember(ctx, admin, "group:orga", "studierende", "group:kurs#member", nil))
	must(t, svc.RemoveMember(ctx, admin, "group:orga", "studierende", "group:kurs#studierende"))
	ms, err := store.ListMemberships(ctx, "orga")
	must(t, err)
	if len(ms) != 1 || ms[0].Member != "group:kurs" {
		t.Errorf("after removing the relation entry %+v", ms)
	}
	must(t, svc.ChangeRelation(ctx, admin, "group:orga", "group:kurs", "studierende", "dozierende"))
	toks, err = svc.GetUserTokens(ctx, "stud@x")
	must(t, err)
	if !slices.Contains(toks, "group:orga#dozierende") {
		t.Errorf("relation of the plain entry not changed: %v", toks)
	}

	// A relation of a group can also maintain one.
	must(t, svc.AddManager(ctx, admin, "group:orga", "group:kurs#Dozierende"))
	managers, err := store.ListManagers(ctx, "orga")
	must(t, err)
	if len(managers) != 1 || managers[0].Token != "group:kurs#dozierende" {
		t.Errorf("managers %+v", managers)
	}
}

// A cycle through an entry with a relation is a cycle all the same: the tree would loop.
func TestCyclesThroughARelationAreRefused(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, "studierende", "dozierende")
	for _, id := range []string{"a", "b"} {
		_, err := svc.CreateGroup(ctx, admin, id, "", "", nil)
		must(t, err)
	}
	must(t, svc.AddMember(ctx, admin, "group:a", "studierende", "group:b#dozierende", nil))
	for _, member := range []string{"group:a", "group:a#studierende", "group:a#dozierende"} {
		if err := svc.AddMember(ctx, admin, "group:b", "", member, nil); !errors.Is(err, ErrCycle) {
			t.Errorf("%s into b: got %v", member, err)
		}
	}
	if err := svc.AddMember(ctx, admin, "group:a", "", "group:a#studierende", nil); !errors.Is(err, ErrCycle) {
		t.Errorf("a into itself: got %v", err)
	}
}

// The tree shows a group once under its parent, however many relations of it the parent holds, and finds the way down through entries with a relation.
func TestBrowseThroughRelationEntries(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t, "studierende", "dozierende")
	dualis := &common.Source{ID: uuid.New(), Name: "dualis", Type: common.SourceTypeCSV}
	must(t, store.CreateSource(ctx, dualis))
	_, _, err := store.ReplaceTuples(ctx, dualis.ID, []common.TuplePair{
		{GroupID: "dualis-standort-ma", Relation: "studierende", MemberType: "group", MemberID: "dualis-orga-x#studierende"},
		{GroupID: "dualis-orga-x", Relation: "studierende", MemberType: "group", MemberID: "dualis-kurs#studierende"},
		{GroupID: "dualis-orga-x", Relation: "dozierende", MemberType: "group", MemberID: "dualis-kurs#dozierende"},
		{GroupID: "dualis-kurs", Relation: "studierende", MemberType: "user", MemberID: "s@x"},
		{GroupID: "dualis-kurs", Relation: "dozierende", MemberType: "user", MemberID: "d@x"},
	}, nil)
	must(t, err)

	top, err := svc.Browse(ctx, admin, BrowseQuery{Source: "dualis"})
	must(t, err)
	if got := ids(top); !slices.Equal(got, []string{"dualis-standort-ma"}) || top.Items[0].ChildGroups != 1 {
		t.Errorf("top of dualis %+v", top.Items)
	}
	inside, err := svc.Browse(ctx, admin, BrowseQuery{Parent: "group:dualis-orga-x"})
	must(t, err)
	if got := ids(inside); !slices.Equal(got, []string{"dualis-kurs"}) {
		t.Errorf("inside the Studiengang %v", got)
	}
	orga, err := svc.Browse(ctx, admin, BrowseQuery{Parent: "group:dualis-standort-ma"})
	must(t, err)
	if len(orga.Items) != 1 || orga.Items[0].ChildGroups != 1 {
		t.Errorf("the Studiengang counts its course once %+v", orga.Items)
	}
	path, err := svc.Browse(ctx, admin, BrowseQuery{PathTo: "group:dualis-kurs"})
	must(t, err)
	if got := ids(path); !slices.Equal(got, []string{"dualis-standort-ma", "dualis-orga-x", "dualis-kurs"}) {
		t.Errorf("path %v", got)
	}
}
