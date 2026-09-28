package storage

import (
	"context"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
)

// The relation semantics are implemented twice — in Go for the memory store
// and in SQL for Postgres — so every case here runs against both. Postgres
// runs when TEST_POSTGRES_DSN points at an empty, throwaway database, e.g.
//
//	docker run --rm -d -p 55432:5432 -e POSTGRES_PASSWORD=pw postgres:18
//	TEST_POSTGRES_DSN="host=localhost port=55432 user=postgres password=pw dbname=postgres sslmode=disable" go test ./internal/storage/

func storesUnderTest(t *testing.T) map[string]Store {
	t.Helper()
	stores := map[string]Store{"memory": NewMemoryStore(zap.NewNop().Sugar())}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		return stores
	}
	pg, err := NewPostgresStore(dsn, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	// Each test starts from an empty graph.
	for _, table := range []string{"tuples", "groups", "sync_logs", "sources"} {
		if err := pg.DB().Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	stores["postgres"] = pg
	return stores
}

// seedCourse builds a course with roles, a nested group and a pattern:
//
//	wwi23seb#dozent       ← user anna
//	wwi23seb#studierende  ← group wwi23seb-kurs (members: ben, carla)
//	wwi23seb              ← user dora (plain member)
//	fakultaet-wi          ← group wwi23seb (all of the course)
//	tutoren#tutor         ← pattern *@tutor.example
func seedCourse(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	must(t, s.AddMember(ctx, "wwi23seb", "dozent", "user", "anna@example", nil))
	must(t, s.AddMember(ctx, "wwi23seb-kurs", "member", "user", "ben@example", nil))
	must(t, s.AddMember(ctx, "wwi23seb-kurs", "", "user", "carla@example", nil))
	must(t, s.AddMember(ctx, "wwi23seb", "studierende", "group", "wwi23seb-kurs", nil))
	must(t, s.AddMember(ctx, "wwi23seb", "member", "user", "dora@example", nil))
	must(t, s.AddMember(ctx, "fakultaet-wi", "member", "group", "wwi23seb", nil))
	must(t, s.AddMember(ctx, "tutoren", "tutor", "pattern", "*@tutor.example", nil))
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return out
}

func TestRelations_UserTokens(t *testing.T) {
	cases := map[string][]string{
		// A role implies membership, and membership carries on to the groups
		// the course is in — without a "#member" suffix anywhere.
		"anna@example": {"group:fakultaet-wi", "group:wwi23seb", "group:wwi23seb#dozent", "user:anna@example"},
		// A relation held through a nested group.
		"ben@example": {"group:fakultaet-wi", "group:wwi23seb", "group:wwi23seb#studierende", "group:wwi23seb-kurs", "user:ben@example"},
		// A plain member gets no relation token.
		"dora@example": {"group:fakultaet-wi", "group:wwi23seb", "user:dora@example"},
		// A pattern on a relation grants both.
		"eve@tutor.example": {"group:tutoren", "group:tutoren#tutor", "user:eve@tutor.example"},
	}
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			seedCourse(t, s)
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

func TestRelations_Members(t *testing.T) {
	type q struct{ group, relation string }
	all := map[q][]string{
		// "member" is everyone in the group, whatever their relation.
		{"wwi23seb", "member"}: {"group:wwi23seb-kurs", "user:anna@example", "user:ben@example", "user:carla@example", "user:dora@example"},
		{"wwi23seb", "dozent"}: {"user:anna@example"},
		// The sub-group contributes all of its members to the relation.
		{"wwi23seb", "studierende"}: {"group:wwi23seb-kurs", "user:ben@example", "user:carla@example"},
		{"fakultaet-wi", ""}:        {"group:wwi23seb", "group:wwi23seb-kurs", "user:anna@example", "user:ben@example", "user:carla@example", "user:dora@example"},
	}
	direct := map[q][]string{
		{"wwi23seb", "member"}: {"group:wwi23seb-kurs", "user:anna@example", "user:dora@example"},
		{"wwi23seb", "dozent"}: {"user:anna@example"},
	}
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			seedCourse(t, s)
			ctx := context.Background()
			for k, want := range all {
				got, err := s.GetAllMembers(ctx, k.group, k.relation)
				must(t, err)
				if !slices.Equal(sorted(got), want) {
					t.Errorf("all %v = %v, want %v", k, sorted(got), want)
				}
			}
			for k, want := range direct {
				got, err := s.GetDirectMembers(ctx, k.group, k.relation)
				must(t, err)
				if !slices.Equal(sorted(got), want) {
					t.Errorf("direct %v = %v, want %v", k, sorted(got), want)
				}
			}
		})
	}
}

// Removing one relation leaves the member's other relations in place.
func TestRelations_RemoveOneRelation(t *testing.T) {
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			must(t, s.AddMember(ctx, "g", "dozent", "user", "anna@example", nil))
			must(t, s.AddMember(ctx, "g", "member", "user", "anna@example", nil))
			must(t, s.RemoveMember(ctx, "g", "dozent", "user", "anna@example"))
			got, err := s.GetUserTokens(ctx, "anna@example")
			must(t, err)
			if want := []string{"group:g", "user:anna@example"}; !slices.Equal(sorted(got), want) {
				t.Errorf("tokens = %v, want %v", sorted(got), want)
			}
		})
	}
}

// A sync replaces a source's tuples including their relation: the same user
// moving from studierende to dozent is one removal and one addition.
func TestRelations_ReplaceTuples(t *testing.T) {
	for name, s := range storesUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			src := uuid.New()
			must(t, s.CreateSource(ctx, &common.Source{ID: src, Name: "test", Type: common.SourceTypeCSV}))
			first := []common.TuplePair{{GroupID: "c", Relation: "studierende", MemberType: "user", MemberID: "anna@example"}}
			if _, _, err := s.ReplaceTuples(ctx, src, first, nil); err != nil {
				t.Fatal(err)
			}
			second := []common.TuplePair{{GroupID: "c", Relation: "dozent", MemberType: "user", MemberID: "anna@example"}}
			added, removed, err := s.ReplaceTuples(ctx, src, second, nil)
			must(t, err)
			if added != 1 || removed != 1 {
				t.Errorf("added=%d removed=%d, want 1/1", added, removed)
			}
			got, err := s.GetUserTokens(ctx, "anna@example")
			must(t, err)
			if want := []string{"group:c", "group:c#dozent", "user:anna@example"}; !slices.Equal(sorted(got), want) {
				t.Errorf("tokens = %v, want %v", sorted(got), want)
			}
		})
	}
}
