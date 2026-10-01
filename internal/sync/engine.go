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
	store     storage.Store
	relations common.Relations
	log       *zap.SugaredLogger
	// afterSync, if set, is called after a successful sync (e.g. to refresh the
	// group search cache so imported groups are searchable immediately).
	afterSync func(context.Context)
	// keycloak, if set, reads the Keycloak source; nil means none is configured.
	keycloak KeycloakFetcher
}

// KeycloakFetcher reads the Keycloak source: tuples, group descriptions and
// notes on attribute values it could not place.
type KeycloakFetcher func(ctx context.Context) ([]common.TuplePair, map[string]string, []string, error)

// SetKeycloak registers the reader for sources of type keycloak.
func (e *Engine) SetKeycloak(fn KeycloakFetcher) {
	e.keycloak = fn
}

func NewEngine(store storage.Store, relations common.Relations, log *zap.SugaredLogger) *Engine {
	return &Engine{store: store, relations: relations, log: log}
}

// SetAfterSync registers a callback invoked after each successful sync.
func (e *Engine) SetAfterSync(fn func(context.Context)) {
	e.afterSync = fn
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

	var tuples []common.TuplePair
	var descriptions map[string]string
	var parseErr error
	if src.Type == common.SourceTypeKeycloak {
		tuples, descriptions, logEntry.Notes, parseErr = e.fetchKeycloak(ctx, content)
	} else {
		tuples, descriptions, parseErr = e.parseTuples(src, content)
	}
	if parseErr == nil {
		tuples, descriptions = normalizeIDs(tuples, descriptions)
		parseErr = e.checkRelations(tuples)
	}
	if parseErr == nil {
		parseErr = CheckOwnership(ctx, e.store, src.ID, groupIDs(tuples))
	}
	if parseErr != nil {
		now := time.Now()
		logEntry.FinishedAt = &now
		logEntry.ErrorMessage = parseErr.Error()
		_ = e.store.UpdateSyncLog(ctx, logEntry)
		_ = e.store.UpdateSourceSyncStatus(ctx, sourceID, common.SyncStatusError, now)
		return parseErr
	}

	e.log.Infow("sync: parsed tuples", "source_id", sourceID, "count", len(tuples))

	added, removed, replaceErr := e.store.ReplaceTuples(ctx, sourceID, tuples, descriptions)
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

	// Make freshly imported groups searchable immediately (e.g. refresh the cache).
	if e.afterSync != nil {
		e.afterSync(ctx)
	}
	return nil
}

// checkRelations refuses the whole import when a tuple names a relation that is
// not configured: dropping it silently would hand out less access than the
// source says, with nothing to show why. The sync log records the error.
func (e *Engine) checkRelations(tuples []common.TuplePair) error {
	for i := range tuples {
		rel, err := e.relations.Check(tuples[i].Relation)
		if err != nil {
			return fmt.Errorf("group %q: %w", tuples[i].GroupID, err)
		}
		tuples[i].Relation = rel
	}
	return nil
}

// groupIDs returns the distinct groups the tuples write members into.
func groupIDs(tuples []common.TuplePair) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tuples {
		if !seen[t.GroupID] {
			seen[t.GroupID] = true
			out = append(out, t.GroupID)
		}
	}
	return out
}

// CheckOwnership refuses a source writing members into a group that belongs to
// someone else — another source, or the API. A group belongs to exactly one
// writer: two sources filling the same name silently merge two meanings (the
// CSV's staff tree "dhbw-ma" would have gained every student), and when one of
// them drops a member the other still lists, the member vanishes anyway.
// Referring to another source's group as a member is always allowed; that is
// how two sources are combined on purpose.
func CheckOwnership(ctx context.Context, store storage.Store, sourceID uuid.UUID, groups []string) error {
	owners, err := store.GroupOwners(ctx, groups)
	if err != nil {
		return fmt.Errorf("check group ownership: %w", err)
	}
	for _, id := range groups {
		owner, exists := owners[id]
		if !exists || (owner != nil && *owner == sourceID) {
			continue
		}
		who := "the API"
		if owner != nil {
			who = "source " + owner.String()
			if src, err := store.GetSource(ctx, *owner); err == nil && src != nil {
				who = fmt.Sprintf("source %q", src.Name)
			}
		}
		return fmt.Errorf("group %q belongs to %s; give this source's groups a prefix of their own, or refer to that group as a member", id, who)
	}
	return nil
}

// fetchKeycloak reads the Keycloak source. It has no file: an upload to it is
// refused rather than ignored, so nobody believes they replaced its data.
func (e *Engine) fetchKeycloak(ctx context.Context, content []byte) ([]common.TuplePair, map[string]string, []string, error) {
	if content != nil {
		return nil, nil, nil, fmt.Errorf("a keycloak source reads Keycloak and takes no upload")
	}
	if e.keycloak == nil {
		return nil, nil, nil, fmt.Errorf("no keycloak connection is configured")
	}
	return e.keycloak(ctx)
}

// parseTuples reads the data (from content bytes or file path) and parses it
// into tuples plus the group descriptions carried by the source.
func (e *Engine) parseTuples(src *common.Source, content []byte) ([]common.TuplePair, map[string]string, error) {
	var r io.Reader
	if content != nil {
		r = bytes.NewReader(content)
	} else if src.FilePath != "" {
		f, err := os.Open(src.FilePath)
		if err != nil {
			return nil, nil, fmt.Errorf("open file %s: %w", src.FilePath, err)
		}
		defer f.Close()
		r = f
	} else {
		return nil, nil, fmt.Errorf("source has neither uploaded content nor a file_path")
	}

	switch src.Type {
	case common.SourceTypeCSV:
		return ParseCSV(r)
	case common.SourceTypeLDIF:
		if src.DNEmailRegexp == "" {
			return nil, nil, fmt.Errorf("ldif source requires dn_email_regexp to be set")
		}
		return ParseLDIF(r, src.DNEmailRegexp, src.GroupRelationRegexp)
	default:
		return nil, nil, fmt.Errorf("unsupported source type %q", src.Type)
	}
}

// normalizeIDs puts every group and member id into the one spelling the store
// keeps (common.NormalizeID). A file may spell a group or an address with
// capitals; after this, two spellings are one row.
func normalizeIDs(tuples []common.TuplePair, descriptions map[string]string) ([]common.TuplePair, map[string]string) {
	for i := range tuples {
		tuples[i].GroupID = common.NormalizeID(tuples[i].GroupID)
		tuples[i].MemberID = common.NormalizeID(tuples[i].MemberID)
	}
	normalized := make(map[string]string, len(descriptions))
	for id, desc := range descriptions {
		normalized[common.NormalizeID(id)] = desc
	}
	return tuples, normalized
}
