package groupmgmt

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
)

// viewerFixture is a study programme maintained here with a synced course in it:
//
//	eltern (local)               ← group:programm
//	programm (local)             ← group:wwi24#studierende, group:kurs-local, group:alt (expired)
//	  viewers: user:v@x, group:pruefungsamt, group:standort#lehrende; manager: user:m@x
//	kurs-local (local)           ← user:k@x
//	wwi24 (dualis)               ← user:s@x as studierende, user:d@x as dozent
//	alt (dualis)                 ← user:a@x
//	fremd (dualis)               ← user:f@x
func viewerFixture(t *testing.T) *Service {
	t.Helper()
	ctx := context.Background()
	svc, store := newTestService(t, "studierende", "dozent", "lehrende")
	dualis := &common.Source{ID: uuid.New(), Name: "dualis", Type: common.SourceTypeCSV}
	must(t, store.CreateSource(ctx, dualis))
	_, _, err := store.ReplaceTuples(ctx, dualis.ID, []common.TuplePair{
		{GroupID: "wwi24", Relation: "studierende", MemberType: "user", MemberID: "s@x"},
		{GroupID: "wwi24", Relation: "dozent", MemberType: "user", MemberID: "d@x"},
		{GroupID: "alt", MemberType: "user", MemberID: "a@x"},
		{GroupID: "fremd", MemberType: "user", MemberID: "f@x"},
	}, nil)
	must(t, err)
	for _, id := range []string{"eltern", "programm", "kurs-local", "pruefungsamt", "standort"} {
		_, err := svc.CreateGroup(ctx, admin, id, "", "", nil)
		must(t, err)
	}
	must(t, svc.AddMember(ctx, admin, "group:eltern", "", "group:programm", nil))
	must(t, svc.AddMember(ctx, admin, "group:programm", "", "group:wwi24#studierende", nil))
	must(t, svc.AddMember(ctx, admin, "group:programm", "", "group:kurs-local", nil))
	must(t, svc.AddMember(ctx, admin, "group:kurs-local", "", "user:k@x", nil))
	past := time.Now().Add(-time.Hour)
	local := svc.LocalSourceID()
	must(t, store.AddMember(ctx, "programm", "member", "group", "alt", &local, &past))
	must(t, svc.AddManager(ctx, admin, "group:programm", "user:m@x"))
	for _, v := range []string{"user:V@x", "group:pruefungsamt", "group:standort#lehrende"} {
		must(t, svc.AddViewer(ctx, admin, "group:programm", v))
	}
	return svc
}

// A viewer — as a person, through a group or through a relation of a group — may look at the group's entries and everyone it counts, and at those of every group nested in it, of any source; not at the groups it is nested in, not at its history, and changes nothing. Being a viewer is not being a member.
func TestViewersMayOnlyLook(t *testing.T) {
	ctx := context.Background()
	svc := viewerFixture(t)

	viewer := person("v@x")
	viaGroup := person("p@x", "group:pruefungsamt")
	viaRelation := person("l@x", "group:standort", "group:standort#lehrende")
	plainInRelationGroup := person("o@x", "group:standort")
	manager := person("m@x")

	cases := []struct {
		caller Caller
		group  string
		may    bool
	}{
		{viewer, "programm", true}, {viaGroup, "programm", true}, {viaRelation, "programm", true}, {plainInRelationGroup, "programm", false},
		// Down: a synced course entered with a relation, a local group, also for the manager of the parent.
		{viewer, "wwi24", true}, {viewer, "kurs-local", true}, {viaRelation, "wwi24", true}, {manager, "wwi24", true}, {manager, "kurs-local", true},
		// Not up, not beside, not through an expired entry.
		{viewer, "eltern", false}, {viewer, "fremd", false}, {viewer, "alt", false}, {manager, "eltern", false}, {plainInRelationGroup, "wwi24", false},
	}
	for _, tc := range cases {
		d, err := svc.Detail(ctx, tc.caller, "group:"+tc.group)
		_, effErr := svc.EffectiveMembers(ctx, tc.caller, "group:"+tc.group)
		if tc.may {
			if err != nil || effErr != nil {
				t.Errorf("%s may look at %s: %v / %v", tc.caller.Actor, tc.group, err, effErr)
				continue
			}
			if d.Editable || d.MayDelete || !d.ViewOnly {
				t.Errorf("%s looks at %s read-only, got editable=%v may_delete=%v view_only=%v", tc.caller.Actor, tc.group, d.Editable, d.MayDelete, d.ViewOnly)
			}
			continue
		}
		if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrOwnedBySource) {
			t.Errorf("%s may not look at %s: got %v", tc.caller.Actor, tc.group, err)
		}
		if !errors.Is(effErr, ErrForbidden) && !errors.Is(effErr, ErrOwnedBySource) {
			t.Errorf("%s may not resolve %s: got %v", tc.caller.Actor, tc.group, effErr)
		}
	}

	// The members are those of the group: the course's people through the relation entered, the local group's people, the expired entry's not.
	people, err := svc.EffectiveMembers(ctx, viewer, "group:programm")
	must(t, err)
	slices.Sort(people)
	if !slices.Equal(people, []string{"user:k@x", "user:s@x"}) {
		t.Errorf("people of programm for a viewer: %v", people)
	}
	if people, err := svc.EffectiveMembers(ctx, viewer, "group:wwi24"); err != nil || len(people) != 2 {
		t.Errorf("people of the course for a viewer: %v %v", people, err)
	}

	// Nothing to change, no history, nobody to hand rights to.
	for _, group := range []string{"programm", "kurs-local"} {
		if err := svc.AddMember(ctx, viewer, "group:"+group, "", "user:new@x", nil); !errors.Is(err, ErrForbidden) {
			t.Errorf("a viewer added to %s: %v", group, err)
		}
		if _, err := svc.History(ctx, viewer, "group:"+group, 10); !errors.Is(err, ErrForbidden) {
			t.Errorf("a viewer read the history of %s: %v", group, err)
		}
	}
	if err := svc.RemoveMember(ctx, viewer, "group:programm", "", "group:kurs-local"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer removed an entry: %v", err)
	}
	if _, err := svc.UpdateGroup(ctx, viewer, "group:programm", "x", "y"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer renamed the group: %v", err)
	}
	if err := svc.AddViewer(ctx, viewer, "group:programm", "user:friend@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer added a viewer: %v", err)
	}
	if err := svc.AddManager(ctx, viewer, "group:programm", "user:v@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer made themselves manager: %v", err)
	}
	if _, err := svc.SearchPrincipals(ctx, viewer, "x", 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer searched the directory: %v", err)
	}

	// Managing is not passed down: the manager of programm reads kurs-local but does not maintain it.
	if err := svc.AddMember(ctx, manager, "group:kurs-local", "", "user:new@x", nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("the parent's manager changed a nested group: %v", err)
	}
	if _, err := svc.History(ctx, manager, "group:kurs-local", 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("the parent's manager read a nested group's history: %v", err)
	}
	if d, err := svc.Detail(ctx, manager, "group:programm"); err != nil || !d.Editable || d.ViewOnly {
		t.Errorf("the manager's own group: %+v %v", d, err)
	}

	// No membership: viewing a group gives none of its tokens.
	for _, email := range []string{"v@x", "p@x", "l@x"} {
		toks, err := svc.GetUserTokens(ctx, email)
		must(t, err)
		if slices.Contains(toks, "group:programm") || slices.Contains(toks, "group:wwi24") {
			t.Errorf("%s holds a viewed group's token: %v", email, toks)
		}
	}

	// The group pages list the viewed group and what is nested in it, all read-only; /me says there is something to show.
	page, err := svc.Browse(ctx, viewer, BrowseQuery{})
	must(t, err)
	if got := ids(page); !slices.Equal(got, []string{"kurs-local", "programm", "wwi24"}) {
		t.Errorf("a viewer's groups %v", got)
	}
	for _, g := range page.Items {
		if g.Editable {
			t.Errorf("a viewer's group %s is editable", g.ID)
		}
	}
	if found, _ := svc.Browse(ctx, viewer, BrowseQuery{Query: "wwi24"}); !slices.Equal(ids(found), []string{"wwi24"}) {
		t.Errorf("a viewer's search %v", ids(found))
	}
	if views, err := svc.ViewsAny(ctx, viewer); err != nil || !views {
		t.Errorf("views any: %v %v", views, err)
	}
	if views, _ := svc.ViewsAny(ctx, manager); views {
		t.Error("a manager who views nothing views any")
	}
	if mine, _ := svc.ManagedGroups(ctx, viewer, ""); len(mine) != 0 {
		t.Errorf("a viewer maintains %v", mine)
	}
	page, err = svc.Browse(ctx, manager, BrowseQuery{})
	must(t, err)
	for _, g := range page.Items {
		if g.Editable != (g.ID == "programm") {
			t.Errorf("for the manager, %s editable = %v", g.ID, g.Editable)
		}
	}
}

// Viewers are changed by whoever may change the managers, checked like managers, listed in the detail and recorded in the history.
func TestViewersAreMaintainedLikeManagers(t *testing.T) {
	ctx := context.Background()
	svc := viewerFixture(t)
	manager := person("m@x")

	if err := svc.AddViewer(ctx, manager, "group:programm", "user:Neu@x"); err != nil {
		t.Fatalf("the manager adds a viewer: %v", err)
	}
	d, err := svc.Detail(ctx, manager, "group:programm")
	must(t, err)
	var tokens []string
	for _, v := range d.Viewers {
		tokens = append(tokens, v.Token)
	}
	if !slices.Equal(tokens, []string{"group:pruefungsamt", "group:standort#lehrende", "user:neu@x", "user:v@x"}) {
		t.Errorf("viewers %v", tokens)
	}
	if len(d.Managers) != 1 || d.Managers[0].Token != "user:m@x" {
		t.Errorf("managers %+v", d.Managers)
	}
	if err := svc.RemoveViewer(ctx, manager, "group:programm", " user:NEU@x "); err != nil {
		t.Errorf("the manager removes a viewer: %v", err)
	}
	h, err := svc.History(ctx, manager, "group:programm", 2)
	must(t, err)
	if len(h) != 2 || h[0].Action != common.ChangeViewerRemoved || h[1].Action != common.ChangeViewerAdded || h[0].Member != "user:neu@x" || h[0].Actor != "m@x" {
		t.Errorf("history %+v", h)
	}

	if err := svc.AddViewer(ctx, manager, "group:programm", "group:does-not-exist"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a viewer naming a missing group: %v", err)
	}
	if err := svc.AddViewer(ctx, manager, "group:programm", "group:standort#unknown"); !errors.Is(err, ErrInvalidRelation) {
		t.Errorf("a viewer naming an unknown relation: %v", err)
	}
	if err := svc.AddViewer(ctx, manager, "group:programm", "pattern:*@x.de"); !errors.Is(err, common.ErrInvalidID) {
		t.Errorf("a pattern as viewer: %v", err)
	}
	if err := svc.AddViewer(ctx, manager, "group:kurs-local", "user:x@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("the parent's manager added a viewer to a nested group: %v", err)
	}
	if err := svc.AddViewer(ctx, manager, "group:wwi24", "user:x@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("the parent's manager added a viewer to a sync source's group: %v", err)
	}
	if err := svc.AddViewer(ctx, admin, "group:nope", "user:x@x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a viewer on a missing group: %v", err)
	}
}

// Admins set viewers on a sync source's groups too — a course a sync brings — and nobody else does; such a viewer reads the group and every group nested in it, and nothing else of it, and the next sync leaves the viewer alone. Members, managers and the group itself stay with the sync.
func TestViewersOnSyncedGroups(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t, "studierende")
	dualis := &common.Source{ID: uuid.New(), Name: "dualis", Type: common.SourceTypeCSV}
	must(t, store.CreateSource(ctx, dualis))
	tuples := []common.TuplePair{
		{GroupID: "wi", Relation: "studierende", MemberType: "group", MemberID: "wwi24"},
		{GroupID: "wwi24", Relation: "studierende", MemberType: "group", MemberID: "wwi24-a"},
		{GroupID: "wwi24-a", Relation: "studierende", MemberType: "user", MemberID: "s@x"},
		{GroupID: "wwi25", MemberType: "user", MemberID: "t@x"},
	}
	_, _, err := store.ReplaceTuples(ctx, dualis.ID, tuples, nil)
	must(t, err)
	_, err = svc.CreateGroup(ctx, admin, "programm", "", "", []string{"user:m@x"})
	must(t, err)
	must(t, svc.AddMember(ctx, admin, "group:programm", "", "group:wwi24", nil))

	// Only admins set them: not the manager of the local group the course is in, not a viewer of the course.
	if err := svc.AddViewer(ctx, person("m@x"), "group:wwi24", "user:v@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a manager of the parent set a viewer on the course: %v", err)
	}
	must(t, svc.AddViewer(ctx, admin, "group:wwi24", "user:V@x"))
	must(t, svc.AddViewer(ctx, admin, "group:wwi25", "user:gone@x"))
	viewer := person("v@x")
	if err := svc.AddViewer(ctx, viewer, "group:wwi24", "user:friend@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a viewer set a viewer: %v", err)
	}
	if err := svc.RemoveViewer(ctx, person("m@x"), "group:wwi24", "user:v@x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a manager of the parent removed a viewer of the course: %v", err)
	}
	// Everything else of the course stays with its sync, admins included.
	if err := svc.AddMember(ctx, admin, "group:wwi24", "", "user:x@x", nil); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("an admin added a member to the course: %v", err)
	}
	if err := svc.AddManager(ctx, admin, "group:wwi24", "user:x@x"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("an admin added a manager to the course: %v", err)
	}

	// The viewer reads the course and what is in it, read-only, and nothing above or beside it.
	for group, may := range map[string]bool{"wwi24": true, "wwi24-a": true, "wi": false, "programm": false, "wwi25": false} {
		d, err := svc.Detail(ctx, viewer, "group:"+group)
		if may != (err == nil) {
			t.Errorf("%s readable = %v: %v", group, err == nil, err)
			continue
		}
		if may && (d.Editable || !d.ViewOnly || d.ViewersEditable) {
			t.Errorf("%s for the viewer: %+v", group, d)
		}
	}
	if people, err := svc.EffectiveMembers(ctx, viewer, "group:wwi24"); err != nil || !slices.Equal(people, []string{"user:s@x"}) {
		t.Errorf("people of the course for its viewer: %v %v", people, err)
	}
	if _, err := svc.History(ctx, viewer, "group:wwi24", 10); !errors.Is(err, ErrForbidden) {
		t.Errorf("the viewer read the history: %v", err)
	}
	if views, err := svc.ViewsAny(ctx, viewer); err != nil || !views {
		t.Errorf("views any: %v %v", views, err)
	}
	page, err := svc.Browse(ctx, viewer, BrowseQuery{})
	must(t, err)
	if got := ids(page); !slices.Equal(got, []string{"wwi24", "wwi24-a"}) {
		t.Errorf("the viewer's groups %v", got)
	}

	// The admin's detail lists the viewers and lets them be changed, nothing else; the history records it.
	d, err := svc.Detail(ctx, admin, "group:wwi24")
	must(t, err)
	if d.Editable || !d.ViewersEditable || d.ViewOnly || len(d.Viewers) != 1 || d.Viewers[0].Token != "user:v@x" {
		t.Errorf("the admin's detail of the course: %+v", d)
	}
	if h, _ := svc.History(ctx, admin, "group:wwi24", 1); len(h) != 1 || h[0].Action != common.ChangeViewerAdded {
		t.Errorf("history %+v", h)
	}

	// A sync run keeps the viewers, and a course the source dropped stays, empty, while a viewer points at it.
	_, _, err = store.ReplaceTuples(ctx, dualis.ID, tuples[:3], nil)
	must(t, err)
	if d, err := svc.Detail(ctx, admin, "group:wwi24"); err != nil || len(d.Viewers) != 1 {
		t.Errorf("viewers after a sync: %+v %v", d, err)
	}
	if d, err := svc.Detail(ctx, admin, "group:wwi25"); err != nil || len(d.Viewers) != 1 || len(d.Memberships) != 0 {
		t.Errorf("the dropped course: %+v %v", d, err)
	}
	if _, err := svc.Detail(ctx, viewer, "group:wwi24-a"); err != nil {
		t.Errorf("reading down after a sync: %v", err)
	}
	// Removed, the viewer reads nothing, and the next run drops the empty course.
	must(t, svc.RemoveViewer(ctx, admin, "group:wwi25", "user:gone@x"))
	_, _, err = store.ReplaceTuples(ctx, dualis.ID, tuples[:3], nil)
	must(t, err)
	if _, err := svc.Detail(ctx, admin, "group:wwi25"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the dropped course without viewers: %v", err)
	}
	must(t, svc.RemoveViewer(ctx, admin, "group:wwi24", "user:v@x"))
	if _, err := svc.Detail(ctx, viewer, "group:wwi24-a"); err == nil {
		t.Error("a former viewer still reads down")
	}
}

// A cycle a sync brings, which the API would refuse, ends the walk instead of looping.
func TestViewingIsCycleSafe(t *testing.T) {
	ctx := context.Background()
	svc, store := newTestService(t)
	ldap := &common.Source{ID: uuid.New(), Name: "ldap", Type: common.SourceTypeLDIF}
	must(t, store.CreateSource(ctx, ldap))
	_, _, err := store.ReplaceTuples(ctx, ldap.ID, []common.TuplePair{
		{GroupID: "x", MemberType: "group", MemberID: "y"},
		{GroupID: "y", MemberType: "group", MemberID: "x"},
		{GroupID: "y", MemberType: "user", MemberID: "u@x"},
		{GroupID: "z", MemberType: "group", MemberID: "x"},
	}, nil)
	must(t, err)
	_, err = svc.CreateGroup(ctx, admin, "top", "", "", nil)
	must(t, err)
	must(t, svc.AddMember(ctx, admin, "group:top", "", "group:x", nil))
	must(t, svc.AddViewer(ctx, admin, "group:top", "user:v@x"))

	viewer := person("v@x")
	for group, may := range map[string]bool{"x": true, "y": true, "z": false} {
		_, err := svc.Detail(ctx, viewer, "group:"+group)
		if may != (err == nil) {
			t.Errorf("%s readable = %v: %v", group, err == nil, err)
		}
	}
	page, err := svc.Browse(ctx, viewer, BrowseQuery{})
	must(t, err)
	if got := ids(page); !slices.Equal(got, []string{"top", "x", "y"}) {
		t.Errorf("a viewer's groups in a cycle %v", got)
	}
}
