package catalog

import (
	"context"
	"slices"
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

func tokens(groups []common.Group) []string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = g.Token
	}
	return out
}

// The search box reads like a search box: words OR, phrases and UND/+ AND,
// wildcards, umlauts folded, and each relation held in a group offered as an
// entry of its own.
func TestSearchSyntax(t *testing.T) {
	c := testCache(t, []common.Group{
		{ID: "standort-ma", Token: "group:standort-ma", Description: "DHBW Mannheim (aus bwIDM)", Relations: []string{"beschaeftigte", "studierende"}},
		{ID: "standort-ka", Token: "group:standort-ka", Description: "DHBW Karlsruhe (aus bwIDM)", Relations: []string{"beschaeftigte"}},
		{ID: "dhbw-ma", Token: "group:dhbw-ma", Description: "DHBW Mannheim"},
		{ID: "fak-technik", Token: "group:fak-technik", Description: "Fakultät Technik"},
		{ID: "studierende-dhbw-ma", Token: "group:studierende-dhbw-ma", Description: "Studierende der DHBW Mannheim"},
	})

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"words are OR", "karlsruhe technik", []string{"group:fak-technik", "group:standort-ka", "group:standort-ka#beschaeftigte"}},
		{"UND makes both required, and the role is a term",
			"mannheim UND beschäftigte", []string{"group:standort-ma#beschaeftigte"}},
		{"plus is AND too", "mannheim +beschaeftigte", []string{"group:standort-ma#beschaeftigte"}},
		{"phrase", `"fakultät technik"`, []string{"group:fak-technik"}},
		{"wildcard on a whole id, with the group's roles", "*-ka", []string{"group:standort-ka", "group:standort-ka#beschaeftigte"}},
		{"wildcard prefix", "stud*", []string{"group:standort-ma#studierende", "group:studierende-dhbw-ma"}},
		{"an exact id comes first", "group:dhbw-ma",
			[]string{"group:dhbw-ma", "group:studierende-dhbw-ma"}},
		{"more matching terms rank higher", "mannheim studierende",
			[]string{"group:standort-ma#studierende", "group:studierende-dhbw-ma", "group:dhbw-ma", "group:standort-ma", "group:standort-ma#beschaeftigte"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokens(c.Search(tt.query, 0))
			if !slices.Equal(got, tt.want) {
				t.Errorf("query %q:\n got %v\nwant %v", tt.query, got, tt.want)
			}
		})
	}

	// A relation entry says which role it is.
	got := c.Search("mannheim UND beschaeftigte", 0)
	if len(got) != 1 || got[0].Description != "DHBW Mannheim (aus bwIDM) · Rolle: beschaeftigte" {
		t.Errorf("relation entry = %+v", got)
	}
}
