package storage

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// A database from before keeps its file_path column: new sources are still stored (the column takes its default), and the sources that still have a path are named in the log at startup.
func TestPostgres_LegacyFilePathColumn(t *testing.T) {
	pg := postgresUnderTest(t)
	ctx := context.Background()
	if err := pg.DB().Exec(`ALTER TABLE sources ADD COLUMN IF NOT EXISTS file_path text NOT NULL DEFAULT ''`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.DB().Exec(`ALTER TABLE sources DROP COLUMN IF EXISTS file_path`) })

	old := &common.Source{ID: uuid.New(), Name: "alt", Type: common.SourceTypeCSV}
	if err := pg.CreateSource(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := pg.DB().Exec(`UPDATE sources SET file_path = '/data/kurse.csv' WHERE id = ?`, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := pg.CreateSource(ctx, &common.Source{ID: uuid.New(), Name: "neu", Type: common.SourceTypeCSV}); err != nil {
		t.Fatalf("a new source on a database with the old column: %v", err)
	}

	core, logs := observer.New(zap.WarnLevel)
	warnLegacyFilePaths(pg.DB(), zap.New(core).Sugar())
	entries := logs.All()
	if len(entries) != 1 || entries[0].ContextMap()["count"] != int64(1) {
		t.Fatalf("log = %+v", entries)
	}
}
