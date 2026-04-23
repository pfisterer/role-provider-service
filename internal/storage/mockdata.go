package storage

import (
	"context"

	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
)

// SeedMockData populates the store with the same groups/users used by the
// openstack-management-api mock data, making both services work together
// out of the box for local development.
func SeedMockData(ctx context.Context, store Store, log *zap.SugaredLogger) error {
	log.Info("Seeding mock data into store")

	groups := []common.Group{
		{ID: "root_uni", DisplayName: "University Root", Description: "Top-level university group"},
		{ID: "dept_cs_admin", DisplayName: "Computer Science Dept", Description: "CS department administrators"},
		{ID: "dept_cs_faculty", DisplayName: "CS Faculty Pool", Description: "CS faculty members"},
		{ID: "cs-student", DisplayName: "CS Students", Description: "Computer science students"},
		{ID: "dept_bio", DisplayName: "Biology Dept", Description: "Biology department"},
	}
	for i := range groups {
		if err := store.CreateGroup(ctx, &groups[i]); err != nil {
			return err
		}
	}

	// Direct user → group memberships (mirrors openstack-management-api identities).
	userMemberships := []struct{ email, groupID string }{
		{"root.admin@uni.example", "root_uni"},
		{"admin@cs.example", "dept_cs_admin"},
		{"faculty@cs.example", "dept_cs_faculty"},
		{"faculty@bio.example", "dept_bio"},
		{"cs-student@cs.com", "cs-student"},
	}
	for _, m := range userMemberships {
		if err := store.AddMember(ctx, m.groupID, "user", m.email, nil); err != nil {
			return err
		}
	}

	// Group → group hierarchy (child is a member of parent).
	// This mirrors the delegation hierarchy in openstack-management-api.
	groupHierarchy := []struct{ child, parent string }{
		{"dept_cs_admin", "root_uni"},
		{"dept_bio", "root_uni"},
		{"dept_cs_faculty", "dept_cs_admin"},
		{"cs-student", "dept_cs_faculty"},
	}
	for _, h := range groupHierarchy {
		if err := store.AddMember(ctx, h.parent, "group", h.child, nil); err != nil {
			return err
		}
	}

	log.Infow("Mock data seeded",
		"groups", len(groups),
		"user_memberships", len(userMemberships),
		"group_hierarchy_links", len(groupHierarchy),
	)
	return nil
}
