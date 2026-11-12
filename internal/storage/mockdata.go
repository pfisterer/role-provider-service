package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
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
		{ID: "wwi23seb", DisplayName: "WWI23SEB", Description: "Course with roles (dozent, studierende)"},
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
		if err := store.AddMember(ctx, m.groupID, common.RelationMember, "user", m.email, nil, nil); err != nil {
			return err
		}
	}

	// A course whose members hold roles, so the dev setup shows relation tokens
	// ("group:wwi23seb#dozent"). Run with GROUP_RELATIONS=dozent,studierende to
	// manage them through the API as well; the store itself does not check.
	courseRoles := []struct{ email, relation string }{
		{"faculty@cs.example", "dozent"},
		{"cs-student@cs.com", "studierende"},
	}
	for _, m := range courseRoles {
		if err := store.AddMember(ctx, "wwi23seb", m.relation, "user", m.email, nil, nil); err != nil {
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

	maintained, err := seedMaintainedGroups(ctx, store)
	if err != nil {
		return err
	}
	synced, err := seedSyncSources(ctx, store)
	if err != nil {
		return err
	}

	log.Infow("Mock data seeded",
		"groups", len(groups)+maintained+synced,
		"user_memberships", len(userMemberships),
		"course_roles", len(courseRoles),
	)
	return nil
}

// mockMember is one entry of a seeded group: a member token ("user:…", "group:…", "pattern:…"), its relation, and an expiry relative to now (0: none; negative: already expired, so the UI shows an entry that ran out).
type mockMember struct {
	token     string
	relation  string
	expiresIn time.Duration
}

// mockGroup is a seeded group with its members and, for local groups, the tokens that maintain it.
type mockGroup struct {
	group    common.Group
	members  []mockMember
	managers []string
}

// seedMaintainedGroups adds groups of the local source (they are created without a source and adopted into the local one at startup, like the groups above) that people maintain through /v1/manage, so the self-service UI's group pages have something to show with the development sign-in (DEV_AUTH_USER=root.admin@uni.example): root.admin maintains most of them directly — so they are listed even without ADMIN_TOKENS — and faculty@cs.example maintains others, which acting on their behalf shows. Nesting stays among these groups and the synced ones below; none of them is entered in root_uni or another admin scope of openstack-management-api (see the note above).
func seedMaintainedGroups(ctx context.Context, store Store) (int, error) {
	const dev = "user:root.admin@uni.example"
	groups := []mockGroup{
		{
			group:    common.Group{ID: "cloud_lab", DisplayName: "Cloud Lab", Description: "Everybody working in the cloud lab"},
			managers: []string{dev},
			members: []mockMember{
				{token: "group:cloud_lab_tutors", relation: common.RelationMember},
				{token: "group:staff-cs", relation: common.RelationMember},
				{token: "pattern:*@lab.uni.example", relation: common.RelationMember},
				{token: "user:guest@partner.example", relation: common.RelationMember, expiresIn: 30 * 24 * time.Hour},
				{token: "user:former.guest@partner.example", relation: common.RelationMember, expiresIn: -7 * 24 * time.Hour},
			},
		},
		{
			group:    common.Group{ID: "cloud_lab_tutors", DisplayName: "Cloud Lab Tutors", Description: "Student tutors of the cloud lab"},
			managers: []string{dev, "group:cloud_lab_leads"},
			members: []mockMember{
				{token: "user:tutor.one@uni.example", relation: common.RelationMember},
				{token: "user:tutor.two@uni.example", relation: common.RelationMember},
				{token: "user:cs-student@cs.com", relation: common.RelationMember, expiresIn: 90 * 24 * time.Hour},
			},
		},
		{
			group:    common.Group{ID: "cloud_lab_leads", DisplayName: "Cloud Lab Leads", Description: "Who runs the cloud lab"},
			managers: []string{dev},
			members:  []mockMember{{token: "user:faculty@cs.example", relation: common.RelationMember}},
		},
		{
			group:    common.Group{ID: "thesis_2026", DisplayName: "Theses 2026", Description: "Students writing their thesis in 2026"},
			managers: []string{dev, "user:faculty@cs.example"},
			members: []mockMember{
				{token: "user:cs-student@cs.com", relation: common.RelationMember},
				{token: "user:student.two@cs.com", relation: common.RelationMember},
				{token: "group:wwi24sea#studierende", relation: common.RelationMember},
			},
		},
		{
			group:    common.Group{ID: "cs_seminar", DisplayName: "CS Seminar", Description: "Maintained by the CS faculty pool, not by the dev user"},
			managers: []string{"group:dept_cs_faculty"},
			members:  []mockMember{{token: "user:cs-student@cs.com", relation: common.RelationMember}},
		},
	}
	for _, g := range groups {
		if err := createMockGroup(ctx, store, g, nil); err != nil {
			return 0, err
		}
	}
	return len(groups), nil
}

// seedSyncSources adds two sync sources — a CSV export of the courses and an LDIF export of the staff directory — each with a small tree of nested groups, so an admin browsing the group pages sees sources other than the local one, read-only. They have no schedule and no file, so nothing ever syncs them and the data stays as seeded.
func seedSyncSources(ctx context.Context, store Store) (int, error) {
	sources := []struct {
		source common.Source
		groups []mockGroup
	}{
		{
			source: common.Source{ID: uuid.New(), Name: "Dualis", Type: common.SourceTypeCSV},
			groups: []mockGroup{
				{
					group: common.Group{ID: "wwi", DisplayName: "WWI", Description: "Business informatics, every course"},
					members: []mockMember{
						{token: "group:wwi24sea", relation: common.RelationMember},
						{token: "group:wwi24seb", relation: common.RelationMember},
					},
				},
				{
					group: common.Group{ID: "wwi24sea", DisplayName: "WWI24SEA", Description: "Course with roles (dozent, studierende)"},
					members: []mockMember{
						{token: "user:faculty@cs.example", relation: "dozent"},
						{token: "user:cs-student@cs.com", relation: "studierende"},
						{token: "user:student.two@cs.com", relation: "studierende"},
					},
				},
				{
					group: common.Group{ID: "wwi24seb", DisplayName: "WWI24SEB", Description: "Course with roles (dozent, studierende)"},
					members: []mockMember{
						{token: "user:faculty@bio.example", relation: "dozent"},
						{token: "user:student.three@cs.com", relation: "studierende"},
					},
				},
			},
		},
		{
			source: common.Source{ID: uuid.New(), Name: "LDAP", Type: common.SourceTypeLDIF},
			groups: []mockGroup{
				{
					group: common.Group{ID: "staff", DisplayName: "Staff", Description: "All staff"},
					members: []mockMember{
						{token: "group:staff-cs", relation: common.RelationMember},
						{token: "group:staff-bio", relation: common.RelationMember},
					},
				},
				{
					group: common.Group{ID: "staff-cs", DisplayName: "Staff CS", Description: "Computer science staff"},
					members: []mockMember{
						{token: "user:admin@cs.example", relation: common.RelationMember},
						{token: "user:faculty@cs.example", relation: common.RelationMember},
					},
				},
				{
					group:   common.Group{ID: "staff-bio", DisplayName: "Staff Biology", Description: "Biology staff"},
					members: []mockMember{{token: "user:faculty@bio.example", relation: common.RelationMember}},
				},
			},
		},
	}
	n := 0
	for i := range sources {
		src := &sources[i].source
		if err := store.CreateSource(ctx, src); err != nil {
			return 0, err
		}
		for _, g := range sources[i].groups {
			if err := createMockGroup(ctx, store, g, &src.ID); err != nil {
				return 0, err
			}
			n++
		}
		if err := store.UpdateSourceSyncStatus(ctx, src.ID, common.SyncStatusOK, time.Now().UTC()); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// createMockGroup creates a group of the given source (nil: none, adopted into the local source at startup) with its members and managers.
func createMockGroup(ctx context.Context, store Store, g mockGroup, sourceID *uuid.UUID) error {
	grp := g.group
	grp.SourceID = sourceID
	if err := store.CreateGroup(ctx, &grp); err != nil {
		return err
	}
	for _, m := range g.members {
		typ, id := common.ParseToken(m.token)
		var expires *time.Time
		if m.expiresIn != 0 {
			t := time.Now().Add(m.expiresIn).UTC()
			expires = &t
		}
		if err := store.AddMember(ctx, grp.ID, m.relation, typ, id, sourceID, expires); err != nil {
			return err
		}
	}
	for _, token := range g.managers {
		if err := store.AddManager(ctx, grp.ID, common.Manager{Token: token, CreatedBy: "mock data"}); err != nil {
			return err
		}
	}
	return nil
}
