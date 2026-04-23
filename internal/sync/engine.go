package sync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

// Engine orchestrates sync runs for a single source.
type Engine struct {
	store storage.Store
	log   *zap.SugaredLogger
}

func NewEngine(store storage.Store, log *zap.SugaredLogger) *Engine {
	return &Engine{store: store, log: log}
}

// RunSync executes a full sync for the given source using the provided file content.
// If content is nil, the source's FilePath is used.
func (e *Engine) RunSync(ctx context.Context, sourceID uuid.UUID, content []byte) error {
	src, err := e.store.GetSource(ctx, sourceID)
	if err != nil {
		return fmt.Errorf("source %s not found: %w", sourceID, err)
	}

	logEntry := &common.SyncLog{
		ID:        uuid.New(),
		SourceID:  sourceID,
		StartedAt: time.Now(),
	}
	if err := e.store.CreateSyncLog(ctx, logEntry); err != nil {
		e.log.Warnw("failed to create sync log", "source_id", sourceID, zap.Error(err))
	}

	tuples, parseErr := e.parseTuples(src, content)
	if parseErr != nil {
		now := time.Now()
		logEntry.FinishedAt = &now
		logEntry.ErrorMessage = parseErr.Error()
		_ = e.store.UpdateSyncLog(ctx, logEntry)
		_ = e.store.UpdateSourceSyncStatus(ctx, sourceID, common.SyncStatusError, now)
		return parseErr
	}

	e.log.Infow("sync: parsed tuples", "source_id", sourceID, "count", len(tuples))

	added, removed, replaceErr := e.store.ReplaceTuples(ctx, sourceID, tuples)
	now := time.Now()
	logEntry.FinishedAt = &now
	logEntry.TuplesAdded = added
	logEntry.TuplesRemoved = removed

	if replaceErr != nil {
		logEntry.ErrorMessage = replaceErr.Error()
		_ = e.store.UpdateSyncLog(ctx, logEntry)
		_ = e.store.UpdateSourceSyncStatus(ctx, sourceID, common.SyncStatusError, now)
		return replaceErr
	}

	_ = e.store.UpdateSyncLog(ctx, logEntry)
	_ = e.store.UpdateSourceSyncStatus(ctx, sourceID, common.SyncStatusOK, now)
	e.log.Infow("sync completed", "source_id", sourceID, "added", added, "removed", removed)
	return nil
}

// parseTuples reads the data (from content bytes or file path) and parses it.
func (e *Engine) parseTuples(src *common.Source, content []byte) ([]common.TuplePair, error) {
	var r io.Reader
	if content != nil {
		r = bytes.NewReader(content)
	} else if src.FilePath != "" {
		f, err := os.Open(src.FilePath)
		if err != nil {
			return nil, fmt.Errorf("open file %s: %w", src.FilePath, err)
		}
		defer f.Close()
		r = f
	} else {
		return nil, fmt.Errorf("source has neither uploaded content nor a file_path")
	}

	switch src.Type {
	case common.SourceTypeCSV:
		return ParseCSV(r)
	case common.SourceTypeLDIF:
		if src.DNEmailRegexp == "" {
			return nil, fmt.Errorf("ldif source requires dn_email_regexp to be set")
		}
		return ParseLDIF(r, src.DNEmailRegexp)
	default:
		return nil, fmt.Errorf("unsupported source type %q", src.Type)
	}
}
