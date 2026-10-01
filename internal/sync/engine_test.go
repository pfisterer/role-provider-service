package sync

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

// An import naming a relation that is not configured fails as a whole and says
// why in the sync log — it does not import the rest with less access than the
// source grants.
func TestEngine_RefusesUnknownRelation(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, err := common.NewRelations([]string{"dozent"})
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, relations, zap.NewNop().Sugar())
	src := &common.Source{ID: uuid.New(), Name: "kurse", Type: common.SourceTypeCSV}
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	csv := "group,member,relation\nwwi23seb,anna@example,dozent\nwwi23seb,ben@example,tutor\n"
	err = engine.RunSync(ctx, src.ID, []byte(csv))
	if err == nil || !strings.Contains(err.Error(), "tutor") {
		t.Fatalf("expected an error naming the relation, got %v", err)
	}
	logs, _ := store.ListSyncLogs(ctx, src.ID, 1)
	if len(logs) != 1 || !strings.Contains(logs[0].ErrorMessage, "tutor") {
		t.Errorf("sync log = %+v", logs)
	}
	if toks, _ := store.GetUserTokens(ctx, "anna@example"); len(toks) != 1 {
		t.Errorf("nothing may be imported from a refused file, got %v", toks)
	}

	// With the relation configured the same file imports.
	csv = "group,member,relation\nwwi23seb,anna@example,dozent\n"
	if err := engine.RunSync(ctx, src.ID, []byte(csv)); err != nil {
		t.Fatal(err)
	}
	toks, _ := store.GetUserTokens(ctx, "anna@example")
	if len(toks) != 3 {
		t.Errorf("tokens = %v", toks)
	}
}

// A file may spell a group or an address with capitals; the identity provider may
// spell it differently at login. Both are the same person, so an import stores
// the address in one spelling and a lookup in any spelling finds it.
func TestEngine_StoresEmailsLowercase(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, err := common.NewRelations(nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, relations, zap.NewNop().Sugar())
	src := &common.Source{ID: uuid.New(), Name: "kurse", Type: common.SourceTypeCSV}
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunSync(ctx, src.ID, []byte("group,member\nWWI23seb, Anna.Berg@Example.org\n")); err != nil {
		t.Fatal(err)
	}
	toks, _ := store.GetUserTokens(ctx, "anna.berg@example.org")
	if !slices.Contains(toks, "group:wwi23seb") {
		t.Errorf("tokens = %v", toks)
	}
}

// The Keycloak source runs through the same engine as a file import: its
// tuples replace the previous run's, and what it could not place is kept in
// the sync log. An upload to it is refused.
func TestEngine_KeycloakSource(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, err := common.NewRelations([]string{"studierende"})
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, relations, zap.NewNop().Sugar())
	src := &common.Source{ID: uuid.New(), Name: "keycloak", Type: common.SourceTypeKeycloak}
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	if err := engine.RunSync(ctx, src.ID, nil); err == nil {
		t.Fatal("a keycloak source without a connection must fail, not import nothing")
	}

	engine.SetKeycloak(func(context.Context) ([]common.TuplePair, map[string]string, []string, error) {
		return []common.TuplePair{{GroupID: "Standort-MA", Relation: "studierende", MemberType: "user", MemberID: "A@x"}},
			map[string]string{"standort-ma": "DHBW Mannheim"}, []string{`unknown scope "y": 1 user(s)`}, nil
	})
	if err := engine.RunSync(ctx, src.ID, []byte("group,member\n")); err == nil {
		t.Fatal("an upload to a keycloak source must be refused")
	}
	if err := engine.RunSync(ctx, src.ID, nil); err != nil {
		t.Fatal(err)
	}
	toks, _ := store.GetUserTokens(ctx, "a@x")
	if !slices.Contains(toks, "group:standort-ma#studierende") {
		t.Errorf("tokens = %v", toks)
	}
	logs, _ := store.ListSyncLogs(ctx, src.ID, 1)
	if len(logs) != 1 || len(logs[0].Notes) != 1 {
		t.Errorf("sync log = %+v", logs)
	}
}

// A group belongs to one writer. A second source filling it fails with a
// message saying whose it is; referring to it as a member is fine.
func TestEngine_GroupBelongsToOneSource(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	engine := NewEngine(store, relations, zap.NewNop().Sugar())
	csv := &common.Source{ID: uuid.New(), Name: "dhbw-mannheim", Type: common.SourceTypeCSV}
	other := &common.Source{ID: uuid.New(), Name: "other", Type: common.SourceTypeCSV}
	for _, s := range []*common.Source{csv, other} {
		if err := store.CreateSource(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	if err := engine.RunSync(ctx, csv.ID, []byte("group,member\ndhbw-ma,anna@x\n")); err != nil {
		t.Fatal(err)
	}
	// The same source again is fine: it owns the group.
	if err := engine.RunSync(ctx, csv.ID, []byte("group,member\ndhbw-ma,ben@x\n")); err != nil {
		t.Fatal(err)
	}

	err := engine.RunSync(ctx, other.ID, []byte("group,member\ndhbw-ma,carla@x\n"))
	if err == nil || !strings.Contains(err.Error(), `"dhbw-mannheim"`) {
		t.Fatalf("expected a collision naming the owner, got %v", err)
	}
	if toks, _ := store.GetUserTokens(ctx, "carla@x"); slices.Contains(toks, "group:dhbw-ma") {
		t.Error("the refused sync wrote into the other source's group")
	}

	// Referring to it is how two sources are combined on purpose.
	if err := engine.RunSync(ctx, other.ID, []byte("group,member\nbwidm-alle,group:dhbw-ma\n")); err != nil {
		t.Fatalf("referring to another source's group: %v", err)
	}
	if toks, _ := store.GetUserTokens(ctx, "ben@x"); !slices.Contains(toks, "group:bwidm-alle") {
		t.Errorf("tokens = %v", toks)
	}
}
