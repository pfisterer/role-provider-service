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

	// Deliberately NO group → group nesting here.
	//
	// Nesting means "every member of the child is also a member of the parent", so
	// linking dept_bio into root_uni handed every biology member the root_uni
	// token — and root_uni is the admin scope of the root node in
	// openstack-management-api. The result: every faculty member was a root admin
	// and saw the whole tree, which makes the mock data useless for trying out the
	// delegation model (the earlier version did exactly this, one link per
	// department, plus students into the faculty pool).
	//
	// Delegation in openstack-management-api does not run through group nesting at
	// all: it runs through each node's AdminScope and the parent chain. The groups
	// here are just flat, distinct populations — which is what makes switching
	// roles in the UI show different things.

	log.Infow("Mock data seeded",
		"groups", len(groups),
		"user_memberships", len(userMemberships),
	)
	return nil
}
