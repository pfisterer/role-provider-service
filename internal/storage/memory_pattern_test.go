package storage

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestMemoryGetUserTokens_Pattern(t *testing.T) {
	s := NewMemoryStore(zap.NewNop().Sugar())
	ctx := context.Background()

	// studierende-dhbw-ma has a pattern member (all @student.dhbw-mannheim.de) and
	// is itself nested under studierende-all, to test transitive expansion.
	must(t, s.AddMember(ctx, "studierende-dhbw-ma", "pattern", "*@student.dhbw-mannheim.de", nil))
	must(t, s.AddMember(ctx, "studierende-all", "group", "studierende-dhbw-ma", nil))
	must(t, s.AddMember(ctx, "root-admin", "user", "dennis.pfisterer@dhbw.de", nil))

	// A matching student gets the pattern group + its parent group, transitively.
	toks, err := s.GetUserTokens(ctx, "max.mustermann@student.dhbw-mannheim.de")
	must(t, err)
	assertHas(t, toks, "user:max.mustermann@student.dhbw-mannheim.de")
	assertHas(t, toks, "group:studierende-dhbw-ma")
	assertHas(t, toks, "group:studierende-all")

	// A non-matching user does NOT get the pattern group.
	toks, err = s.GetUserTokens(ctx, "clemens.martin@dhbw.de")
	must(t, err)
	assertNot(t, toks, "group:studierende-dhbw-ma")

	// The pattern rule must not surface as a concrete member.
	mem, err := s.GetAllMembers(ctx, "studierende-dhbw-ma")
	must(t, err)
	assertNot(t, mem, "pattern:*@student.dhbw-mannheim.de")
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertHas(t *testing.T, list []string, want string) {
	t.Helper()
	for _, x := range list {
		if x == want {
			return
		}
	}
	t.Errorf("expected %q in %v", want, list)
}

func assertNot(t *testing.T, list []string, unwanted string) {
	t.Helper()
	for _, x := range list {
		if x == unwanted {
			t.Errorf("did not expect %q in %v", unwanted, list)
		}
	}
}
