package sync

import (
	"context"
	"errors"
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

// failingStore fails the one write a sync ends with, the way a database would.
type failingStore struct{ storage.Store }

func (failingStore) ReplaceTuples(context.Context, uuid.UUID, []common.TuplePair, map[string]string) (int, int, error) {
	return 0, 0, errors.New(`pq: duplicate key value violates unique constraint "idx_tuple_uniq" DETAIL: Key (obj_id)=(secret)`)
}

// What the source delivered is an ImportError, meant for its maintainer; a failure of the database is not, and its text stays out of the sync log the API serves.
func TestEngine_ImportErrorsAndInternalErrors(t *testing.T) {
	ctx := context.Background()
	mem := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	src := &common.Source{ID: uuid.New(), Name: "kurse", Type: common.SourceTypeCSV}
	if err := mem.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	var importErr *ImportError
	err := NewEngine(mem, relations, zap.NewNop().Sugar()).RunSync(ctx, src.ID, []byte("group,member\n\"root-admin,kurs-x\",anna@x\n"))
	if !errors.As(err, &importErr) || !errors.Is(err, common.ErrInvalidID) {
		t.Fatalf("invalid id: got %v", err)
	}

	store := failingStore{mem}
	err = NewEngine(store, relations, zap.NewNop().Sugar()).RunSync(ctx, src.ID, []byte("group,member\nkurs,anna@x\n"))
	if err == nil || errors.As(err, &importErr) {
		t.Fatalf("database failure: got %v", err)
	}
	logs, _ := mem.ListSyncLogs(ctx, src.ID, 1)
	if len(logs) != 1 || strings.Contains(logs[0].ErrorMessage, "secret") || logs[0].ErrorMessage == "" {
		t.Errorf("sync log = %+v", logs)
	}
}

// A name another source reserves is refused even before that source has created it, and the local source has nothing to sync.
func TestEngine_ReservedNamesAndTheLocalSource(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	engine := NewEngine(store, relations, zap.NewNop().Sugar())
	kc := &common.Source{ID: uuid.New(), Name: "keycloak", Type: common.SourceTypeKeycloak, ReservedNames: []string{"standort-*"}}
	csv := &common.Source{ID: uuid.New(), Name: "dhbw-mannheim", Type: common.SourceTypeCSV}
	local := &common.Source{ID: uuid.New(), Name: "ZWR", Type: common.SourceTypeLocal}
	for _, s := range []*common.Source{kc, csv, local} {
		if err := store.CreateSource(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	err := engine.RunSync(ctx, csv.ID, []byte("group,member\nstandort-fn,anna@x\n"))
	var importErr *ImportError
	if !errors.As(err, &importErr) || !strings.Contains(err.Error(), `"keycloak"`) {
		t.Errorf("a reserved name: got %v", err)
	}

	err = engine.RunSync(ctx, local.ID, []byte("group,member\nx,anna@x\n"))
	if !errors.As(err, &importErr) {
		t.Errorf("syncing the local source: got %v", err)
	}
	if logs, _ := store.ListSyncLogs(ctx, local.ID, 0); len(logs) != 0 {
		t.Errorf("the refused local sync left a log: %v", logs)
	}

	// A reservation may not claim a group somebody else already has.
	if _, _, err := store.ReplaceTuples(ctx, csv.ID, []common.TuplePair{{GroupID: "ad-ma-x", MemberType: "user", MemberID: "a@x"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckReservation(ctx, store, uuid.New(), []string{"ad-ma-*"}); !errors.Is(err, ErrReservationConflict) {
		t.Errorf("reserving a taken name: got %v", err)
	}
	if err := CheckReservation(ctx, store, csv.ID, []string{"ad-ma-*"}); err != nil {
		t.Errorf("reserving one's own names: %v", err)
	}
	if _, err := NormalizeReservedNames([]string{"*"}); !errors.Is(err, common.ErrInvalidID) {
		t.Errorf("reserving everything: got %v", err)
	}
	if got, err := NormalizeReservedNames([]string{" group:Standort-* ", "standort-*", ""}); err != nil || !slices.Equal(got, []string{"standort-*"}) {
		t.Errorf("normalized = %v, %v", got, err)
	}
}

// A member group may name a relation, "group:kurs#studierende": only the people holding it there are meant. The relation must be configured like the row's own, "#member" is the plain group, and a malformed one refuses the file.
func TestEngine_MemberGroupRelation(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, err := common.NewRelations([]string{"studierende", "dozierende"})
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, relations, zap.NewNop().Sugar())
	src := &common.Source{ID: uuid.New(), Name: "dualis", Type: common.SourceTypeCSV}
	if err := store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"group:kurs#tutor", "group:kurs#", "group:kurs#a b"} {
		csv := "group,member,relation\nkurs,s@x,studierende\norga," + bad + ",studierende\n"
		if err := engine.RunSync(ctx, src.ID, []byte(csv)); err == nil {
			t.Errorf("%s: imported", bad)
		}
	}

	csv := "group,member,relation\nkurs,s@x,studierende\nkurs,d@x,dozierende\norga,group:KURS#studierende,studierende\nalle,group:kurs#member,\n"
	if err := engine.RunSync(ctx, src.ID, []byte(csv)); err != nil {
		t.Fatal(err)
	}
	toks, _ := store.GetUserTokens(ctx, "d@x")
	if want := []string{"user:d@x", "group:alle", "group:kurs", "group:kurs#dozierende"}; !slices.Equal(sortedCopy(toks), sortedCopy(want)) {
		t.Errorf("lecturer tokens = %v, want %v", toks, want)
	}
	toks, _ = store.GetUserTokens(ctx, "s@x")
	if !slices.Contains(toks, "group:orga#studierende") || !slices.Contains(toks, "group:alle") {
		t.Errorf("student tokens = %v", toks)
	}
	ms, _ := store.ListMemberships(ctx, "alle")
	if len(ms) != 1 || ms[0].Member != "group:kurs" {
		t.Errorf("alle %+v", ms)
	}
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}
