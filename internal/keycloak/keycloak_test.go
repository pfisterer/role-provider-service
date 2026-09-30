package keycloak

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

const testMapping = `{
  "attribute": "edu_person_affiliation",
  "all_group": {"group": "dhbw", "description": "Alle DHBW-Standorte"},
  "locations": {
    "dhbw-mannheim.de": {"group": "standort-ma", "description": "DHBW Mannheim"},
    "cas.dhbw.de": {"group": "Standort-CAS"}
  },
  "roles": {"student": "studierende", "staff": "beschaeftigte", "employee": "beschaeftigte", "faculty": "lehrende"},
  "ignored_scopes": ["kit.edu"],
  "ignored_values": ["member", "affiliate"]
}`

func testRelations(t *testing.T) common.Relations {
	t.Helper()
	r, err := common.NewRelations([]string{"studierende", "beschaeftigte", "lehrende"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustMapping(t *testing.T) Mapping {
	t.Helper()
	m, err := ParseMapping(testMapping, testRelations(t))
	if err != nil {
		t.Fatalf("parse mapping: %v", err)
	}
	return m
}

func tuplesAsTokens(tuples []common.TuplePair) []string {
	var out []string
	for _, tp := range tuples {
		out = append(out, common.GroupToken(tp.GroupID, tp.Relation))
	}
	slices.Sort(out)
	return out
}

// A role mapped to a relation the service does not know would fail every
// sync; it fails the start instead.
func TestParseMappingRefusesUnknownRelation(t *testing.T) {
	r, _ := common.NewRelations([]string{"studierende"})
	if _, err := ParseMapping(testMapping, r); err == nil || !strings.Contains(err.Error(), "beschaeftigte") {
		t.Fatalf("expected the unknown relation to be named, got %v", err)
	}
	if _, err := ParseMapping(`{"attribute":"x","locations":{"a.de":{"group":"g"}},"typo":1}`, r); err == nil {
		t.Fatal("an unknown field in the mapping must be refused, not ignored")
	}
}

func TestDerive(t *testing.T) {
	m := mustMapping(t)
	cases := []struct {
		name   string
		values []string
		want   []string
	}{
		{
			name:   "staff at one location: the role there and across all",
			values: []string{"staff@dhbw-mannheim.de", "member@dhbw-mannheim.de"},
			want:   []string{"group:dhbw#beschaeftigte", "group:standort-ma#beschaeftigte"},
		},
		{
			name:   "staff and employee are one role",
			values: []string{"staff@cas.dhbw.de", "employee@cas.dhbw.de", "affiliate@cas.dhbw.de"},
			want:   []string{"group:dhbw#beschaeftigte", "group:standort-cas#beschaeftigte"},
		},
		{
			name:   "no role value: plain member of the location",
			values: []string{"member@dhbw-mannheim.de"},
			want:   []string{"group:dhbw", "group:standort-ma"},
		},
		{
			name:   "case in the attribute does not matter",
			values: []string{"Student@DHBW-Mannheim.de"},
			want:   []string{"group:dhbw#studierende", "group:standort-ma#studierende"},
		},
		{
			name:   "an ignored institution grants nothing",
			values: []string{"employee@kit.edu", "faculty@kit.edu"},
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tuplesAsTokens(m.Derive("a@x", c.values, newUnmapped()))
			if !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// What no rule places is counted, not guessed: an unknown value keeps the
// location but grants no role, an unknown scope grants nothing.
func TestDeriveReportsWhatItCannotPlace(t *testing.T) {
	m := mustMapping(t)
	unmapped := newUnmapped()
	got := tuplesAsTokens(m.Derive("a@x", []string{"alum@dhbw-mannheim.de", "student@mosbach.dhbw.de"}, unmapped))
	if want := []string{"group:dhbw", "group:standort-ma"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	m.Derive("b@x", []string{"student@mosbach.dhbw.de"}, unmapped)
	notes := unmapped.Notes()
	want := []string{`unknown scope "mosbach.dhbw.de": 2 user(s)`, `unmapped value "alum@dhbw-mannheim.de": 1 user(s)`}
	if !slices.Equal(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
}

type fakeUsers struct {
	all     []User
	lookups int
}

func (f *fakeUsers) ListUsers(context.Context) ([]User, error) { return f.all, nil }

func (f *fakeUsers) FindUserByEmail(_ context.Context, email string) (*User, error) {
	f.lookups++
	for _, u := range f.all {
		if strings.EqualFold(u.Email, email) {
			return &u, nil
		}
	}
	return nil, nil
}

func affiliated(email string, values ...string) User {
	return User{Email: email, Enabled: true, Attributes: map[string][]string{"edu_person_affiliation": values}}
}

// Someone who signed in after the last full read gets their groups at once,
// and is looked up once, not on every request.
func TestEnsureUserAddsSomeoneNew(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	users := &fakeUsers{all: []User{affiliated("known@x", "staff@dhbw-mannheim.de")}}
	src := NewSource(users, mustMapping(t), store, zap.NewNop().Sugar())
	src.SetSourceID(uuid.New())

	if _, _, _, err := src.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	users.all = append(users.all, affiliated("New.Person@X", "student@cas.dhbw.de"))

	src.EnsureUser(ctx, "known@x")
	if users.lookups != 0 {
		t.Errorf("a user the full read knew was looked up again")
	}
	src.EnsureUser(ctx, "new.person@x")
	src.EnsureUser(ctx, "new.person@x")
	if users.lookups != 1 {
		t.Errorf("lookups = %d, want 1", users.lookups)
	}
	toks, _ := store.GetUserTokens(ctx, "new.person@x")
	if !slices.Contains(toks, "group:standort-cas#studierende") {
		t.Errorf("tokens = %v", toks)
	}

	// Nobody by that address: not asked again until the retry window passes.
	src.EnsureUser(ctx, "ghost@x")
	src.EnsureUser(ctx, "ghost@x")
	if users.lookups != 2 {
		t.Errorf("lookups = %d, want 2", users.lookups)
	}
}

// Disabled accounts hold no groups.
func TestFetchSkipsDisabledUsers(t *testing.T) {
	users := &fakeUsers{all: []User{
		affiliated("on@x", "staff@dhbw-mannheim.de"),
		{Email: "off@x", Enabled: false, Attributes: map[string][]string{"edu_person_affiliation": {"staff@dhbw-mannheim.de"}}},
	}}
	src := NewSource(users, mustMapping(t), storage.NewMemoryStore(zap.NewNop().Sugar()), zap.NewNop().Sugar())
	tuples, descriptions, _, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range tuples {
		if tp.MemberID == "off@x" {
			t.Errorf("a disabled account got %v", tp)
		}
	}
	if descriptions["standort-ma"] != "DHBW Mannheim" || descriptions["dhbw"] != "Alle DHBW-Standorte" {
		t.Errorf("descriptions = %v", descriptions)
	}
}
