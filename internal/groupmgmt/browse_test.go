package groupmgmt

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
)

func ids(page *GroupPage) []string {
	out := []string{}
	for _, g := range page.Items {
		out = append(out, g.ID)
	}
	return out
}

// Admins browse every source as a tree and read its groups; only local groups can be changed, and everybody else still sees just the groups they maintain.
func TestBrowseEverySource(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t, "studierende")
	dualis := &common.Source{ID: uuid.New(), Name: "dualis", Type: common.SourceTypeCSV}
	must(t, store.CreateSource(ctx, dualis))
	_, _, err := store.ReplaceTuples(ctx, dualis.ID, []common.TuplePair{
		{GroupID: "dualis-standort-ma", Relation: "studierende", MemberType: "group", MemberID: "dualis-orga-ma-t-mb"},
		{GroupID: "dualis-fakultaet-technik", Relation: "studierende", MemberType: "group", MemberID: "dualis-orga-ma-t-mb"},
		{GroupID: "dualis-orga-ma-t-mb", Relation: "studierende", MemberType: "group", MemberID: "dualis-ma-tmb24"},
		{GroupID: "dualis-ma-tmb24", Relation: "studierende", MemberType: "user", MemberID: "s@x"},
	}, nil)
	must(t, err)
	_, err = svc.CreateGroup(ctx, admin, "kurs-ma", "", "", []string{"user:m@x"})
	must(t, err)
	// A local group holding a Dualis course stays at the top of the local tree, and does not take the course out of the Dualis tree.
	must(t, svc.AddMember(ctx, admin, "group:kurs-ma", "studierende", "group:dualis-ma-tmb24", nil))

	sources, err := svc.Sources(ctx, admin)
	must(t, err)
	if len(sources) != 2 || sources[0].Name != "ZWR" || !sources[0].Local || sources[1].Name != "dualis" || sources[1].Groups != 4 {
		t.Errorf("sources %+v", sources)
	}

	top, err := svc.Browse(ctx, admin, BrowseQuery{Source: "dualis"})
	must(t, err)
	if got := ids(top); len(got) != 2 || got[0] != "dualis-fakultaet-technik" || got[1] != "dualis-standort-ma" {
		t.Errorf("top of dualis %v", got)
	}
	if top.Items[0].Editable || top.Items[0].ChildGroups != 1 || top.Items[0].SourceName != "dualis" {
		t.Errorf("a dualis group %+v", top.Items[0])
	}
	local, err := svc.Browse(ctx, admin, BrowseQuery{Source: "ZWR"})
	must(t, err)
	if got := ids(local); len(got) != 1 || got[0] != "kurs-ma" || !local.Items[0].Editable {
		t.Errorf("top of ZWR %+v", local.Items)
	}
	inside, err := svc.Browse(ctx, admin, BrowseQuery{Parent: "group:dualis-orga-ma-t-mb"})
	must(t, err)
	if got := ids(inside); len(got) != 1 || got[0] != "dualis-ma-tmb24" {
		t.Errorf("inside the Studiengang %v", got)
	}
	if _, err := svc.Browse(ctx, admin, BrowseQuery{Parent: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown parent: %v", err)
	}
	found, err := svc.Browse(ctx, admin, BrowseQuery{Query: "tmb24"})
	must(t, err)
	if got := ids(found); len(got) != 1 || got[0] != "dualis-ma-tmb24" {
		t.Errorf("search %v", got)
	}
	paged, err := svc.Browse(ctx, admin, BrowseQuery{Source: "dualis", Limit: 1, Offset: 1})
	must(t, err)
	if paged.Total != 2 || len(paged.Items) != 1 || paged.Items[0].ID != "dualis-standort-ma" {
		t.Errorf("second page %+v", paged)
	}

	// Read, not write: an admin sees a Dualis group, its people and history, and cannot change it.
	d, err := svc.Detail(ctx, admin, "group:dualis-ma-tmb24")
	must(t, err)
	if d.Editable || d.MayDelete || len(d.Memberships) != 1 {
		t.Errorf("detail of a dualis group %+v", d)
	}
	if people, err := svc.EffectiveMembers(ctx, admin, "group:dualis-orga-ma-t-mb"); err != nil || len(people) != 1 {
		t.Errorf("people of a dualis group %v %v", people, err)
	}
	if err := svc.AddMember(ctx, admin, "group:dualis-ma-tmb24", "", "user:x@x", nil); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("changing a dualis group: %v", err)
	}
	if d, err := svc.Detail(ctx, admin, "group:kurs-ma"); err != nil || !d.Editable || !d.MayDelete {
		t.Errorf("detail of a local group %+v %v", d, err)
	}

	// A manager: their groups only, whatever they ask for, and no other source.
	manager := person("m@x")
	mine, err := svc.Browse(ctx, manager, BrowseQuery{Source: "dualis"})
	must(t, err)
	if got := ids(mine); len(got) != 1 || got[0] != "kurs-ma" || !mine.Items[0].Editable || mine.Items[0].ChildGroups != 1 {
		t.Errorf("a manager's groups %+v", mine.Items)
	}
	if _, err := svc.Detail(ctx, manager, "group:dualis-ma-tmb24"); err == nil {
		t.Error("a manager read a dualis group")
	}
	if _, err := svc.Sources(ctx, manager); !errors.Is(err, ErrForbidden) {
		t.Errorf("a manager listed the sources: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
