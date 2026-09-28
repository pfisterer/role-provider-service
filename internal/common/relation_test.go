package common

import "testing"

func TestGroupRelationTokens(t *testing.T) {
	cases := []struct {
		id, group, relation, token string
	}{
		{"wwi23seb#dozent", "wwi23seb", "dozent", "group:wwi23seb#dozent"},
		{"wwi23seb", "wwi23seb", "member", "group:wwi23seb"},
	}
	for _, c := range cases {
		g, rel := SplitGroupRelation(c.id)
		if g != c.group || rel != c.relation {
			t.Errorf("SplitGroupRelation(%q) = (%q, %q), want (%q, %q)", c.id, g, rel, c.group, c.relation)
		}
		if got := GroupToken(g, rel); got != c.token {
			t.Errorf("GroupToken(%q, %q) = %q, want %q", g, rel, got, c.token)
		}
	}
	// Membership never carries "#member": the plain token is what every
	// existing rule compares against.
	if got := GroupToken("x", "member"); got != "group:x" {
		t.Errorf("member token = %q", got)
	}
	if got := GroupToken("x", ""); got != "group:x" {
		t.Errorf("empty relation token = %q", got)
	}
}

func TestRelations(t *testing.T) {
	r, err := NewRelations([]string{"dozent", " studierende ", ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"", "member", "dozent", "studierende"} {
		if _, err := r.Check(ok); err != nil {
			t.Errorf("Check(%q): %v", ok, err)
		}
	}
	if _, err := r.Check("tutor"); err == nil {
		t.Error("an unconfigured relation must be refused")
	}
	if got := r.List(); len(got) != 3 || got[0] != "member" {
		t.Errorf("List() = %v", got)
	}
	if _, err := NewRelations([]string{"Dozent#1"}); err == nil {
		t.Error("a relation name with '#' or capitals must be refused")
	}
}
