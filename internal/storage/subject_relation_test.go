package storage

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
)

// seedDualisTree builds the shape of the Dualis tree with a second relation in the course, nested by relation ("group:x#rel"), next to a group that nests the course without one:
//
//	kurs#studierende      ← user stud, pattern *@student.example
//	kurs#dozierende       ← user doz
//	orga#studierende      ← group:kurs#studierende
//	orga#dozierende       ← group:kurs#dozierende
//	standort#studierende  ← group:orga#studierende
//	dhbw#studierende      ← group:standort#studierende
//	alle                  ← group:kurs (everyone in it, whatever their relation)
func seedDualisTree(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	must(t, s.AddMember(ctx, "kurs", "studierende", "user", "stud@example", nil, nil))
	must(t, s.AddMember(ctx, "kurs", "studierende", "pattern", "*@student.example", nil, nil))
	must(t, s.AddMember(ctx, "kurs", "dozierende", "user", "doz@example", nil, nil))
	must(t, s.AddMember(ctx, "orga", "studierende", "group", "kurs#studierende", nil, nil))
	must(t, s.AddMember(ctx, "orga", "dozierende", "group", "kurs#dozierende", nil, nil))
	must(t, s.AddMember(ctx, "standort", "studierende", "group", "orga#studierende", nil, nil))
	must(t, s.AddMember(ctx, "dhbw", "studierende", "group", "standort#studierende", nil, nil))
	must(t, s.AddMember(ctx, "alle", "member", "group", "kurs", nil, nil))
}

// A group entered with a relation passes on only the people holding that relation, all the way up; a plain group entry still passes on everyone.
func TestSubjectRelation_UserTokens(t *testing.T) {
	cases := map[string][]string{
		"stud@example": {"group:alle", "group:dhbw", "group:dhbw#studierende", "group:kurs", "group:kurs#studierende", "group:orga", "group:orga#studierende", "group:standort", "group:standort#studierende", "user:stud@example"},
		// The lecturer is in the course and, through its own relation, in the study programme — but no student anywhere: before group:x#rel every relation of the course went up as membership.
		"doz@example": {"group:alle", "group:kurs", "group:kurs#dozierende", "group:orga", "group:orga#dozierende", "user:doz@example"},
		// A pattern seeds the same chain.
		"eve@student.example": {"group:alle", "group:dhbw", "group:dhbw#studierende", "group:kurs", "group:kurs#studierende", "group:orga", "group:orga#studierende", "group:standort", "group:standort#studierende", "user:eve@student.example"},
	}
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			seedDualisTree(t, s)
			for email, want := range cases {
				got, err := s.GetUserTokens(context.Background(), email)
				must(t, err)
				if !slices.Equal(sorted(got), want) {
					t.Errorf("%s: tokens = %v, want %v", email, sorted(got), want)
				}
			}
		})
	}
}

func TestSubjectRelation_Members(t *testing.T) {
	type q struct{ group, relation string }
	all := map[q][]string{
		{"dhbw", "studierende"}: {"group:kurs#studierende", "group:orga#studierende", "group:standort#studierende", "user:stud@example"},
		{"dhbw", ""}:            {"group:kurs#studierende", "group:orga#studierende", "group:standort#studierende", "user:stud@example"},
		{"orga", ""}:            {"group:kurs#dozierende", "group:kurs#studierende", "user:doz@example", "user:stud@example"},
		{"orga", "dozierende"}:  {"group:kurs#dozierende", "user:doz@example"},
		{"alle", ""}:            {"group:kurs", "user:doz@example", "user:stud@example"},
	}
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			seedDualisTree(t, s)
			ctx := context.Background()
			for k, want := range all {
				got, err := s.GetAllMembers(ctx, k.group, k.relation)
				must(t, err)
				if !slices.Equal(sorted(got), want) {
					t.Errorf("all %v = %v, want %v", k, sorted(got), want)
				}
			}
			direct, err := s.GetDirectMembers(ctx, "orga", "")
			must(t, err)
			if want := []string{"group:kurs#dozierende", "group:kurs#studierende"}; !slices.Equal(sorted(direct), want) {
				t.Errorf("direct = %v, want %v", sorted(direct), want)
			}
			ms, err := s.ListMemberships(ctx, "orga")
			must(t, err)
			var listed []string
			for _, m := range ms {
				listed = append(listed, m.Relation+"="+m.Member)
			}
			if want := []string{"dozierende=group:kurs#dozierende", "studierende=group:kurs#studierende"}; !slices.Equal(sorted(listed), want) {
				t.Errorf("memberships = %v, want %v", sorted(listed), want)
			}
		})
	}
}

// The entry with a subject relation is a tuple of its own: removing it leaves the plain entry of the same group, and "x#member" is the plain entry.
func TestSubjectRelation_AddRemove(t *testing.T) {
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			seedDualisTree(t, s)
			must(t, s.AddMember(ctx, "alle", "member", "group", "kurs#studierende", nil, nil))
			must(t, s.AddMember(ctx, "alle", "member", "group", "kurs#member", nil, nil)) // the plain entry, again
			ms, err := s.ListMemberships(ctx, "alle")
			must(t, err)
			if len(ms) != 2 {
				t.Fatalf("memberships = %v, want group:kurs and group:kurs#studierende", ms)
			}
			must(t, s.RemoveMember(ctx, "alle", "member", "group", "kurs"))
			got, err := s.GetUserTokens(ctx, "doz@example")
			must(t, err)
			if slices.Contains(got, "group:alle") {
				t.Errorf("the lecturer is still in alle through group:kurs#studierende: %v", got)
			}
			got, err = s.GetUserTokens(ctx, "stud@example")
			must(t, err)
			if !slices.Contains(got, "group:alle") {
				t.Errorf("the student lost alle: %v", got)
			}
			must(t, s.ChangeRelation(ctx, "alle", "group", "kurs#studierende", "member", "studierende"))
			got, err = s.GetUserTokens(ctx, "stud@example")
			must(t, err)
			if !slices.Contains(got, "group:alle#studierende") {
				t.Errorf("relation not changed: %v", got)
			}
		})
	}
}

// An expired entry along the chain cuts it from there up.
func TestSubjectRelation_Expired(t *testing.T) {
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			seedDualisTree(t, s)
			past := time.Now().Add(-time.Hour)
			must(t, s.AddMember(ctx, "standort", "studierende", "group", "orga#studierende", nil, &past))
			got, err := s.GetUserTokens(ctx, "stud@example")
			must(t, err)
			if slices.Contains(got, "group:standort") || slices.Contains(got, "group:dhbw#studierende") {
				t.Errorf("tokens through an expired entry: %v", got)
			}
		})
	}
}

// A sync tells group:x#rel from group:x: switching between them is one removal and one addition, the tree has the edge once, and a group named only as group:x#rel still counts as pointed at.
func TestSubjectRelation_ReplaceTuples(t *testing.T) {
	ctx := context.Background()
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			src := uuid.New()
			must(t, s.CreateSource(ctx, &common.Source{ID: src, Name: "dualis", Type: common.SourceTypeCSV}))
			kurs := []common.TuplePair{
				{GroupID: "kurs", Relation: "studierende", MemberType: "user", MemberID: "stud@example"},
				{GroupID: "kurs", Relation: "dozierende", MemberType: "user", MemberID: "doz@example"},
			}
			first := append(slices.Clone(kurs),
				common.TuplePair{GroupID: "orga", Relation: "studierende", MemberType: "group", MemberID: "kurs#studierende"},
				common.TuplePair{GroupID: "orga", Relation: "studierende", MemberType: "group", MemberID: "kurs#studierende"}, // a duplicate row
				common.TuplePair{GroupID: "orga", Relation: "dozierende", MemberType: "group", MemberID: "kurs#dozierende"},
			)
			added, _, err := s.ReplaceTuples(ctx, src, first, nil)
			must(t, err)
			if added != 4 {
				t.Errorf("added = %d, want 4", added)
			}
			// Same file again: nothing changes.
			added, removed, err := s.ReplaceTuples(ctx, src, first, nil)
			must(t, err)
			if added != 0 || removed != 0 {
				t.Errorf("re-import added=%d removed=%d, want 0/0", added, removed)
			}
			edges, err := s.GroupEdges(ctx)
			must(t, err)
			if !slices.Equal(edges, [][2]string{{"orga", "kurs"}}) {
				t.Errorf("edges = %v", edges)
			}
			if got, _ := s.GetUserTokens(ctx, "doz@example"); slices.Contains(got, "group:orga#studierende") {
				t.Errorf("lecturer became a student: %v", got)
			}

			// The plain entry instead of the relation one.
			second := append(slices.Clone(kurs),
				common.TuplePair{GroupID: "orga", Relation: "studierende", MemberType: "group", MemberID: "kurs"},
				common.TuplePair{GroupID: "orga", Relation: "dozierende", MemberType: "group", MemberID: "kurs#dozierende"},
			)
			added, removed, err = s.ReplaceTuples(ctx, src, second, nil)
			must(t, err)
			if added != 1 || removed != 1 {
				t.Errorf("added=%d removed=%d, want 1/1", added, removed)
			}
			if got, _ := s.GetUserTokens(ctx, "doz@example"); !slices.Contains(got, "group:orga#studierende") {
				t.Errorf("a plain group entry passes on everyone: %v", got)
			}

			// A group the import no longer fills stays while another source's group names it as group:x#rel.
			must(t, s.CreateGroup(ctx, &common.Group{ID: "zwr"}))
			must(t, s.AddMember(ctx, "zwr", "", "group", "orga#dozierende", nil, nil))
			_, _, err = s.ReplaceTuples(ctx, src, kurs, nil)
			must(t, err)
			if _, err := s.GetGroup(ctx, "orga"); err != nil {
				t.Errorf("orga dropped although zwr names group:orga#dozierende: %v", err)
			}
		})
	}
}
