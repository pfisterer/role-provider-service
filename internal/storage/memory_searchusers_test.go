package storage

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestMemorySearchUsers(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore(zap.NewNop().Sugar())

	must(t, s.AddMember(ctx, "sg-dski", "user", "dennis.pfisterer@dhbw.de", nil))
	must(t, s.AddMember(ctx, "leitung-sg-dski", "user", "dennis.pfisterer@dhbw.de", nil))
	must(t, s.AddMember(ctx, "sg-wi", "user", "clemens.martin@dhbw.de", nil))
	must(t, s.AddMember(ctx, "sg-wi", "group", "leitung-sg-wi", nil))
	must(t, s.AddMember(ctx, "studierende", "pattern", "*@student.dhbw-mannheim.de", nil))

	all, err := s.SearchUsers(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Deduped across groups, and neither the subgroup nor the pattern rule is a user.
	if len(all) != 2 {
		t.Errorf("expected 2 distinct users, got %v", all)
	}

	hits, err := s.SearchUsers(ctx, "PFIST", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != "dennis.pfisterer@dhbw.de" {
		t.Errorf("case-insensitive substring search returned %v", hits)
	}

	limited, err := s.SearchUsers(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("limit=1 returned %v", limited)
	}
}
