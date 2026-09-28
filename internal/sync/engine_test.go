package sync

import (
	"context"
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
