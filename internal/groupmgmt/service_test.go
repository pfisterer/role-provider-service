package groupmgmt

import (
	"context"
	"errors"
	"slices"
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

// Ids enter through the API only in the one safe shape: a comma or "#" in a group id would turn one group into several, or into a relation, wherever ids are listed or tokenized.
func TestIDsAreValidatedAtTheAPI(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	svc := NewService(store, nil, relations, 5*time.Second, zap.NewNop().Sugar())

	for _, id := range []string{"", "root-admin,kurs-x", "wwi23seb#dozent", "a b", "grüppe", "group:"} {
		if _, err := svc.CreateGroup(ctx, id, "", ""); !errors.Is(err, common.ErrInvalidID) {
			t.Errorf("create %q: got %v", id, err)
		}
	}
	// Normalized before it is checked: prefix, case and surrounding space are not an error.
	g, err := svc.CreateGroup(ctx, " group:Manual ", "", "")
	if err != nil || g.ID != "manual" {
		t.Fatalf("create: %v, %+v", err, g)
	}

	for _, member := range []string{"group:root-admin,kurs-x", "user:not-an-address", "user:a,b@x", "pattern:*", "pattern:*@*", "pattern:*dhbw.de"} {
		if err := svc.AddMember(ctx, "group:manual", "", member); !errors.Is(err, common.ErrInvalidID) {
			t.Errorf("add %q: got %v", member, err)
		}
	}
	for _, member := range []string{"user:Anna@DHBW.de", "group:other", "pattern:*@student.dhbw.de"} {
		if err := svc.AddMember(ctx, "group:manual", "", member); err != nil {
			t.Errorf("add %q: %v", member, err)
		}
	}
	members, _ := svc.GetAllMembers(ctx, "group:manual", "", false)
	if !slices.Contains(members, "user:anna@dhbw.de") {
		t.Errorf("members = %v", members)
	}
}

// A source's group comes back with the next sync, so the API refuses to delete it; an API group is deleted with its memberships.
func TestDeleteGroup(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore(zap.NewNop().Sugar())
	relations, _ := common.NewRelations(nil)
	svc := NewService(store, nil, relations, 5*time.Second, zap.NewNop().Sugar())

	src := uuid.New()
	if err := store.CreateGroup(ctx, &common.Group{ID: "imported", SourceID: &src}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMember(ctx, "imported", "", "user", "a@x", &src); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, "group:imported"); !errors.Is(err, ErrOwnedBySource) {
		t.Errorf("delete a source's group: got %v", err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "a@x"); !slices.Contains(toks, "group:imported") {
		t.Errorf("the refused delete changed something: %v", toks)
	}

	if _, err := svc.CreateGroup(ctx, "manual", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddMember(ctx, "group:manual", "", "user:b@x"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, "group:manual"); err != nil {
		t.Fatal(err)
	}
	if toks, _ := svc.GetUserTokens(ctx, "b@x"); slices.Contains(toks, "group:manual") {
		t.Errorf("former member still has the group: %v", toks)
	}
	if err := svc.DeleteGroup(ctx, "group:manual"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete a missing group: got %v", err)
	}
}
