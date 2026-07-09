package catalog

import (
	"context"
	"testing"

	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
)

func testCache(t *testing.T, groups []common.Group) *GroupCache {
	t.Helper()
	c := New(func(context.Context) ([]common.Group, error) { return groups, nil }, zap.NewNop().Sugar())
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	return c
}

func ids(groups []common.Group) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.ID
	}
	return out
}

func TestSearch(t *testing.T) {
	c := testCache(t, []common.Group{
		{ID: "dept_cs_faculty", DisplayName: "CS Faculty"},
		{ID: "dept_cs_admin", DisplayName: "CS Admins"},
		{ID: "dept_bio", DisplayName: "Biology"},
		{ID: "root_uni", DisplayName: "University Root"},
	})

	tests := []struct {
		name  string
		query string
		limit int
		want  []string
	}{
		// Empty query returns all, sorted by ID.
		{"all sorted", "", 0, []string{"dept_bio", "dept_cs_admin", "dept_cs_faculty", "root_uni"}},
		// Substring over ID, case-insensitive.
		{"by id substring", "CS", 0, []string{"dept_cs_admin", "dept_cs_faculty"}},
		// Substring over display name.
		{"by display name", "biolog", 0, []string{"dept_bio"}},
		// Limit truncates after sorting (stable, lowest IDs first).
		{"limit", "dept", 2, []string{"dept_bio", "dept_cs_admin"}},
		// No match.
		{"no match", "zzz", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(c.Search(tt.query, tt.limit))
			if len(got) != len(tt.want) {
				t.Fatalf("query %q limit %d: got %v, want %v", tt.query, tt.limit, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("query %q limit %d: got %v, want %v", tt.query, tt.limit, got, tt.want)
				}
			}
		})
	}
}

func TestRefreshReplacesSnapshot(t *testing.T) {
	groups := []common.Group{{ID: "a"}}
	c := New(func(context.Context) ([]common.Group, error) { return groups, nil }, zap.NewNop().Sugar())
	_ = c.Refresh(context.Background())
	if c.Size() != 1 {
		t.Fatalf("size after first refresh = %d, want 1", c.Size())
	}
	groups = []common.Group{{ID: "a"}, {ID: "b"}}
	_ = c.Refresh(context.Background())
	if c.Size() != 2 {
		t.Fatalf("size after second refresh = %d, want 2", c.Size())
	}
}
