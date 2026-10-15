package storage

import (
	"context"
	"os"
	"slices"
	"testing"

	"go.uber.org/zap"
)

// postgresUnderTest returns a store on the throwaway database TEST_POSTGRES_DSN points at (see storesUnderTest), emptied, or skips the test when it is unset.
func postgresUnderTest(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	pg, err := NewPostgresStore(dsn, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	for _, table := range []string{"tuples", "groups", "sync_logs", "sources"} {
		if err := pg.DB().Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("clean %s: %v", table, err)
		}
	}
	return pg
}

// The groups a pattern rule matched are handed to the SQL as one text[] parameter. They used to be joined with "," and split again in SQL, so a group id containing a comma — "root-admin,kurs-x" with a pattern member — gave every matching address group:root-admin. The id constraint now keeps such an id out of the database; it is lifted here to test the query on its own.
func TestPostgres_PatternGroupIDIsNotSplit(t *testing.T) {
	pg := postgresUnderTest(t)
	ctx := context.Background()
	if err := pg.DB().Exec(`ALTER TABLE tuples DROP CONSTRAINT tuples_obj_id_format`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pg.DB().Exec("DELETE FROM tuples")
		if err := addIDConstraints(pg.DB()); err != nil {
			t.Errorf("restore constraints: %v", err)
		}
	})

	must(t, pg.AddMember(ctx, "root-admin", "member", "user", "admin@dhbw.de", nil))
	must(t, pg.AddMember(ctx, "root-admin,kurs-x", "member", "pattern", "*@student.dhbw.de", nil))
	must(t, pg.AddMember(ctx, "kurs-y", "member", "pattern", "*@student.dhbw.de", nil))

	toks, err := pg.GetUserTokens(ctx, "eve@student.dhbw.de")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(toks, "group:root-admin") || slices.Contains(toks, "group:kurs-x") {
		t.Errorf("the pattern group's id was split: %v", toks)
	}
	if !slices.Contains(toks, "group:root-admin,kurs-x") || !slices.Contains(toks, "group:kurs-y") {
		t.Errorf("tokens = %v", toks)
	}

	// No pattern matches: the empty array seeds nothing.
	toks, err = pg.GetUserTokens(ctx, "admin@dhbw.de")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sorted(toks), []string{"group:root-admin", "user:admin@dhbw.de"}) {
		t.Errorf("tokens = %v", toks)
	}
}

// Whatever path writes to the database, a group id outside common.GroupIDPattern is refused by the CHECK constraints.
func TestPostgres_IDConstraints(t *testing.T) {
	pg := postgresUnderTest(t)
	ctx := context.Background()

	for _, id := range []string{"root-admin,kurs-x", "kurs#dozent", "Kurs", "a b", "grüppe", ""} {
		if err := pg.DB().Exec(`INSERT INTO groups (id) VALUES (?)`, id).Error; err == nil {
			t.Errorf("groups: %q stored", id)
		}
		if err := pg.AddMember(ctx, id, "member", "user", "a@x", nil); err == nil {
			t.Errorf("tuples.obj_id: %q stored", id)
		}
		if err := pg.AddMember(ctx, "kurs", "member", "group", id, nil); err == nil {
			t.Errorf("tuples.subj_id (group): %q stored", id)
		}
	}
	if err := pg.DB().Exec(`INSERT INTO groups (id) VALUES ('kurs_a.b-1')`).Error; err != nil {
		t.Errorf("valid id refused: %v", err)
	}
	// Addresses and patterns are checked by the service, not by this constraint.
	must(t, pg.AddMember(ctx, "kurs", "member", "pattern", "*@x.example", nil))

	// The constraints are added once and survive a restart.
	if _, err := NewPostgresStore(os.Getenv("TEST_POSTGRES_DSN"), zap.NewNop().Sugar()); err != nil {
		t.Fatalf("second start: %v", err)
	}
}
