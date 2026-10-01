package groupmgmt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

// A sync source's groups are exactly its latest data: the API may not add
// or remove members there, only in groups it created itself.
func TestMembersOfASourceGroupCannotBeChangedThroughTheAPI(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	svc := NewService(store, nil, relations, 5*time.Second, zap.NewNop().Sugar())

	src := uuid.New()
	if err := store.CreateGroup(ctx, &common.Group{ID: "imported", SourceID: &src}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGroup(ctx, "manual", "", ""); err != nil {
		t.Fatal(err)
	}

	if err := svc.AddMember(ctx, "group:imported", "", "user:a@x"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("add to a source's group: got %v", err)
	}
	if err := svc.RemoveMember(ctx, "group:imported", "", "user:a@x"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("remove from a source's group: got %v", err)
	}
	if err := svc.AddMember(ctx, "group:manual", "", "user:a@x"); err != nil {
		t.Errorf("add to an API group: %v", err)
	}
}
