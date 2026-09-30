package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// ── GORM models ──────────────────────────────────────────────────────────────

type DBGroup struct {
	ID          string     `gorm:"primaryKey"`
	DisplayName string     `gorm:"not null;default:''"`
	Description string     `gorm:"not null;default:''"`
	SourceID    *uuid.UUID `gorm:"type:uuid;index"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (DBGroup) TableName() string { return "groups" }

type DBSource struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name          string    `gorm:"not null"`
	Type          string    `gorm:"not null"`
	Schedule      string    `gorm:"not null;default:''"`
	DNEmailRegexp string    `gorm:"not null;default:''"`
	// Added with group relations; AutoMigrate adds the column with its default.
	GroupRelationRegexp string `gorm:"not null;default:''"`
	FilePath            string `gorm:"not null;default:''"`
	LastSyncedAt        *time.Time
	LastSyncStatus      string `gorm:"not null;default:''"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (DBSource) TableName() string { return "sources" }

type DBSyncLog struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	SourceID      uuid.UUID `gorm:"type:uuid;not null;index"`
	StartedAt     time.Time `gorm:"not null"`
	FinishedAt    *time.Time
	TuplesAdded   int `gorm:"not null;default:0"`
	TuplesRemoved int `gorm:"not null;default:0"`
	ErrorMessage  string
	// Notes, one per line; AutoMigrate adds the column with its default.
	Notes     string `gorm:"not null;default:''"`
	CreatedAt time.Time
}

func (DBSyncLog) TableName() string { return "sync_logs" }

// DBTuple is a single Zanzibar-style relationship tuple.
// obj_type is always "group". relation is "member" or a configured relation
// (e.g. "dozent"), each of which implies membership. subj_rel is "member" when
// subj_type is "group", else "".
type DBTuple struct {
	ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ObjID     string     `gorm:"not null;uniqueIndex:idx_tuple_uniq"`
	Relation  string     `gorm:"not null;default:'member';uniqueIndex:idx_tuple_uniq"`
	SubjType  string     `gorm:"not null;uniqueIndex:idx_tuple_uniq"` // "user" | "group"
	SubjID    string     `gorm:"not null;uniqueIndex:idx_tuple_uniq"`
	SubjRel   string     `gorm:"not null;default:'';uniqueIndex:idx_tuple_uniq"` // "" | "member"
	SourceID  *uuid.UUID `gorm:"type:uuid;index"`
	CreatedAt time.Time
}

func (DBTuple) TableName() string { return "tuples" }

// ── Store interface ───────────────────────────────────────────────────────────

type Store interface {
	// Groups
	CreateGroup(ctx context.Context, group *common.Group) error
	GetGroup(ctx context.Context, id string) (*common.Group, error)
	ListGroups(ctx context.Context, query string, sourceID *uuid.UUID, limit int) ([]common.Group, error)
	UpdateGroup(ctx context.Context, id, displayName, description string) error
	DeleteGroup(ctx context.Context, id string) error

	// Members (tuples)
	// relation is "member" (or "") or a configured relation; asking for
	// "member" in the listings means everyone in the group, whatever their
	// relation, since every relation implies membership.
	AddMember(ctx context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID) error
	RemoveMember(ctx context.Context, groupID, relation, memberType, memberID string) error
	GetDirectMembers(ctx context.Context, groupID, relation string) ([]string, error)
	GetAllMembers(ctx context.Context, groupID, relation string) ([]string, error)
	GetUserTokens(ctx context.Context, email string) ([]string, error)
	SearchUsers(ctx context.Context, query string, limit int) ([]string, error)

	// Sources
	CreateSource(ctx context.Context, s *common.Source) error
	GetSource(ctx context.Context, id uuid.UUID) (*common.Source, error)
	ListSources(ctx context.Context) ([]common.Source, error)
	UpdateSource(ctx context.Context, id uuid.UUID, name, schedule, dnEmailRegexp, groupRelationRegexp, filePath string) error
	DeleteSource(ctx context.Context, id uuid.UUID) error
	UpdateSourceSyncStatus(ctx context.Context, id uuid.UUID, status string, syncedAt time.Time) error

	// Sync logs
	CreateSyncLog(ctx context.Context, l *common.SyncLog) error
	UpdateSyncLog(ctx context.Context, l *common.SyncLog) error
	ListSyncLogs(ctx context.Context, sourceID uuid.UUID, limit int) ([]common.SyncLog, error)

	// Atomic tuple replacement for a source (used during sync)
	// ReplaceTuples replaces this source's tuples. descriptions maps group ID →
	// description as carried by the import; entries update the matching group.
	ReplaceTuples(ctx context.Context, sourceID uuid.UUID, newTuples []common.TuplePair, descriptions map[string]string) (added, removed int, err error)
}

// ── PostgresStore ─────────────────────────────────────────────────────────────

type PostgresStore struct {
	db  *gorm.DB
	log *zap.SugaredLogger
}

func NewPostgresStore(dsn string, log *zap.SugaredLogger) (*PostgresStore, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Cap how many connections this service may hold open.
	//
	// GORM has no pool of its own — the driver hands gorm.Open a plain *sql.DB, so
	// the pool is database/sql's, and its default for MaxOpenConns is UNLIMITED.
	// Gin runs one goroutine per request and nothing here bounds concurrency, so a
	// burst or a hot loop could open as many connections as there are in-flight
	// requests.
	//
	// That matters because this is a single shared Postgres with max_connections
	// 100 (3 reserved), and PowerDNS reads its zones from it: exhausting the
	// instance would not read as "the API is slow", it would read as "DNS stopped
	// answering". Three services at 10 each leaves the rest of the cluster ample
	// room, and 10 is far above anything measured (the idle floor is 2).
	//
	// MaxIdleConns is raised from the default 2 so a handful of concurrent
	// requests does not pay for a new connection each.
	//
	// Deliberately NOT setting ConnMaxLifetime: the usual Kubernetes argument for
	// it — stale connections after a Postgres restart or a service-IP change — is
	// already handled by pgx, whose ResetSession discards closed connections and
	// pings any that sat idle for more than a second before reuse.
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to reach the underlying sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)

	if err := db.AutoMigrate(&DBGroup{}, &DBSource{}, &DBSyncLog{}, &DBTuple{}); err != nil {
		return nil, fmt.Errorf("failed to migrate database schema: %w", err)
	}
	if err := normalizeStoredIDs(db); err != nil {
		return nil, fmt.Errorf("failed to normalize stored ids: %w", err)
	}

	log.Info("PostgreSQL storage initialized and schema migrated")
	return &PostgresStore{db: db, log: log}, nil
}

// normalizeStoredIDs brings rows written before ids were lowercased into the one
// spelling the service now writes and looks up (common.NormalizeID). Two
// spellings of one row collapse into one: for a group the lowercase row wins if
// there is one, otherwise any. A no-op once the data is clean, so it runs on
// every start instead of being tracked as a one-off.
func normalizeStoredIDs(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, stmt := range []string{
			`DELETE FROM tuples a USING tuples b
			 WHERE a.id > b.id
			   AND lower(a.obj_id) = lower(b.obj_id) AND a.relation = b.relation
			   AND a.subj_type = b.subj_type AND lower(a.subj_id) = lower(b.subj_id) AND a.subj_rel = b.subj_rel`,
			`UPDATE tuples SET obj_id = lower(obj_id), subj_id = lower(subj_id)
			 WHERE obj_id <> lower(obj_id) OR subj_id <> lower(subj_id)`,
			`DELETE FROM groups a USING groups b
			 WHERE a.id <> b.id AND lower(a.id) = lower(b.id) AND a.id <> lower(a.id)
			   AND (b.id = lower(b.id) OR a.id > b.id)`,
			`UPDATE groups SET id = lower(id) WHERE id <> lower(id)`,
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ── Groups ────────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateGroup(ctx context.Context, g *common.Group) error {
	row := DBGroup{
		ID:          g.ID,
		DisplayName: g.DisplayName,
		Description: g.Description,
		SourceID:    g.SourceID,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	g.CreatedAt = row.CreatedAt
	g.UpdatedAt = row.UpdatedAt
	g.Token = common.GroupPrefix + g.ID
	return nil
}

// DB exposes the underlying gorm.DB for low-level queries (e.g. admin stats).
func (s *PostgresStore) DB() *gorm.DB { return s.db }

func (s *PostgresStore) GetGroup(ctx context.Context, id string) (*common.Group, error) {
	var row DBGroup
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return dbGroupToCommon(&row), nil
}

func (s *PostgresStore) ListGroups(ctx context.Context, query string, sourceID *uuid.UUID, limit int) ([]common.Group, error) {
	q := s.db.WithContext(ctx).Model(&DBGroup{})
	if query != "" {
		like := "%" + query + "%"
		q = q.Where("id ILIKE ? OR display_name ILIKE ? OR description ILIKE ?", like, like, like)
	}
	if sourceID != nil {
		// Groups whose primary source matches OR that have at least one tuple from this source.
		q = q.Where(
			"source_id = ? OR id IN (SELECT DISTINCT obj_id FROM tuples WHERE source_id = ?)",
			sourceID, sourceID,
		)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	q = q.Order("id ASC")

	var rows []DBGroup
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]common.Group, len(rows))
	for i, r := range rows {
		out[i] = *dbGroupToCommon(&r)
	}
	return out, nil
}

func (s *PostgresStore) UpdateGroup(ctx context.Context, id, displayName, description string) error {
	return s.db.WithContext(ctx).Model(&DBGroup{}).Where("id = ?", id).
		Updates(map[string]any{"display_name": displayName, "description": description}).Error
}

func (s *PostgresStore) DeleteGroup(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&DBGroup{}).Error
}

func dbGroupToCommon(r *DBGroup) *common.Group {
	return &common.Group{
		ID:          r.ID,
		Token:       common.GroupPrefix + r.ID,
		DisplayName: r.DisplayName,
		Description: r.Description,
		SourceID:    r.SourceID,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// ── Members / Tuples ──────────────────────────────────────────────────────────

func (s *PostgresStore) AddMember(ctx context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID) error {
	relation = common.NormalizeRelation(relation)
	subjRel := subjRelFor(memberType)
	tuple := DBTuple{
		ObjID:    groupID,
		Relation: relation,
		SubjType: memberType,
		SubjID:   memberID,
		SubjRel:  subjRel,
		SourceID: sourceID,
	}
	// ON CONFLICT DO NOTHING — idempotent
	return s.db.WithContext(ctx).
		Where("obj_id = ? AND relation = ? AND subj_type = ? AND subj_id = ? AND subj_rel = ?",
			groupID, relation, memberType, memberID, subjRel).
		FirstOrCreate(&tuple).Error
}

func (s *PostgresStore) RemoveMember(ctx context.Context, groupID, relation, memberType, memberID string) error {
	return s.db.WithContext(ctx).
		Where("obj_id = ? AND relation = ? AND subj_type = ? AND subj_id = ? AND subj_rel = ?",
			groupID, common.NormalizeRelation(relation), memberType, memberID, subjRelFor(memberType)).
		Delete(&DBTuple{}).Error
}

// GetDirectMembers returns members that are directly in groupID (no recursion)
// with the given relation; "member" means any relation.
func (s *PostgresStore) GetDirectMembers(ctx context.Context, groupID, relation string) ([]string, error) {
	var rows []DBTuple
	// Skip pattern rules — they are membership rules, not concrete members.
	q := s.db.WithContext(ctx).Where("obj_id = ? AND subj_type <> 'pattern'", groupID)
	if filter := filterFor(common.NormalizeRelation(relation)); filter != "" {
		q = q.Where("relation = ?", filter)
	}
	if err := q.Order("subj_type, subj_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		token := common.BuildToken(r.SubjType, r.SubjID)
		if _, dup := seen[token]; dup {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out, nil
}

// GetAllMembers resolves the transitive members holding relation on a group
// using a recursive CTE. The relation filters only the first level: a
// sub-group contributes all of its members (group:A in B#dozent makes every
// member of A a dozent of B). $2 = ” means any relation, i.e. membership.
func (s *PostgresStore) GetAllMembers(ctx context.Context, groupID, relation string) ([]string, error) {
	const query = `
WITH RECURSIVE expand AS (
    SELECT subj_type, subj_id
    FROM tuples
    WHERE obj_id = $1 AND ($2 = '' OR relation = $2)
  UNION
    SELECT t.subj_type, t.subj_id
    FROM tuples t
    INNER JOIN expand e ON e.subj_type = 'group' AND t.obj_id = e.subj_id
)
SELECT DISTINCT subj_type, subj_id FROM expand WHERE subj_type <> 'pattern'
ORDER BY subj_type, subj_id`

	type row struct {
		SubjType string `gorm:"column:subj_type"`
		SubjID   string `gorm:"column:subj_id"`
	}
	var rows []row
	filter := filterFor(common.NormalizeRelation(relation))
	if err := s.db.WithContext(ctx).Raw(query, groupID, filter).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = common.BuildToken(r.SubjType, r.SubjID)
	}
	return out, nil
}

// GetUserTokens returns the user token, a "group:<id>" token for every group
// the user is a member of (transitively; any relation counts), and a
// "group:<id>#<relation>" token for every other relation the user holds.
func (s *PostgresStore) GetUserTokens(ctx context.Context, email string) ([]string, error) {
	// Pattern rules matching the email. Glob matching is done in Go (for parity
	// with the in-memory store and to keep the recursive SQL simple); the
	// matched group IDs are injected into the CTE seed via $2.
	patterns, err := s.patternMatches(ctx, email)
	if err != nil {
		return nil, err
	}
	patternGroups := make([]string, 0, len(patterns))
	for _, p := range patterns {
		patternGroups = append(patternGroups, p.ObjID)
	}
	// Join group IDs (which never contain commas — enforced by the CSV format)
	// into a plain string param; string_to_array avoids driver-specific []string
	// array binding. The seed branch is skipped entirely when there are no matches.
	patternCSV := strings.Join(patternGroups, ",")

	// user_groups: membership, any relation. The final SELECT adds the
	// non-member relations held directly or through one of those groups.
	const query = `
WITH RECURSIVE user_groups AS (
    SELECT obj_id
    FROM tuples
    WHERE subj_type = 'user' AND subj_id = $1
  UNION
    SELECT unnest(string_to_array($2, ',')) WHERE $2 <> ''
  UNION
    SELECT t.obj_id
    FROM tuples t
    INNER JOIN user_groups ug ON t.subj_type = 'group' AND t.subj_id = ug.obj_id
        AND t.subj_rel = 'member'
)
SELECT obj_id, 'member' AS relation FROM user_groups
UNION
SELECT t.obj_id, t.relation
FROM tuples t
WHERE t.relation <> 'member' AND (
    (t.subj_type = 'user' AND t.subj_id = $1)
    OR (t.subj_type = 'group' AND t.subj_rel = 'member' AND t.subj_id IN (SELECT obj_id FROM user_groups))
)
ORDER BY relation, obj_id`

	type row struct {
		ObjID    string `gorm:"column:obj_id"`
		Relation string `gorm:"column:relation"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).Raw(query, email, patternCSV).Scan(&rows).Error; err != nil {
		return nil, err
	}
	tokens := make([]string, 0, len(rows)+1)
	tokens = append(tokens, common.UserPrefix+email) // always include own user token
	seen := map[string]struct{}{}
	for _, r := range rows {
		token := common.GroupToken(r.ObjID, r.Relation)
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	// A pattern rule on a non-member relation grants that relation directly.
	for _, p := range patterns {
		if p.Relation == common.RelationMember {
			continue
		}
		token := common.GroupToken(p.ObjID, p.Relation)
		if _, dup := seen[token]; !dup {
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
	}
	return tokens, nil
}

// SearchUsers returns email addresses that contain query, case-insensitively.
//
// Deliberately email-only: the store knows no names, and the callers must not be
// able to browse the directory by person. Pattern members (a glob rule such as
// "*@student.…") have no row here and are therefore not findable — by design,
// since a glob describes an unbounded set, not people.
func (s *PostgresStore) SearchUsers(ctx context.Context, query string, limit int) ([]string, error) {
	q := s.db.WithContext(ctx).Model(&DBTuple{}).
		Distinct("subj_id").
		Where("subj_type = 'user'")
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		q = q.Where("subj_id ILIKE ?", "%"+trimmed+"%")
	}
	if limit > 0 {
		q = q.Limit(limit)
	}

	var emails []string
	if err := q.Order("subj_id ASC").Pluck("subj_id", &emails).Error; err != nil {
		return nil, err
	}
	return emails, nil
}

// patternMatch is a pattern rule that matched an email: the group and the
// relation it grants.
type patternMatch struct {
	ObjID    string `gorm:"column:obj_id"`
	Relation string `gorm:"column:relation"`
	SubjID   string `gorm:"column:subj_id"`
}

// patternMatches returns the pattern rules whose glob matches the email.
func (s *PostgresStore) patternMatches(ctx context.Context, email string) ([]patternMatch, error) {
	var rows []patternMatch
	if err := s.db.WithContext(ctx).
		Raw(`SELECT obj_id, relation, subj_id FROM tuples WHERE subj_type = 'pattern'`).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := []patternMatch{}
	for _, r := range rows {
		if common.MatchEmailPattern(r.SubjID, email) {
			out = append(out, r)
		}
	}
	return out, nil
}

// ── Sources ───────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateSource(ctx context.Context, src *common.Source) error {
	row := DBSource{
		ID:                  src.ID,
		Name:                src.Name,
		Type:                src.Type,
		Schedule:            src.Schedule,
		DNEmailRegexp:       src.DNEmailRegexp,
		GroupRelationRegexp: src.GroupRelationRegexp,
		FilePath:            src.FilePath,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create source: %w", err)
	}
	src.CreatedAt = row.CreatedAt
	src.UpdatedAt = row.UpdatedAt
	return nil
}

func (s *PostgresStore) GetSource(ctx context.Context, id uuid.UUID) (*common.Source, error) {
	var row DBSource
	if err := s.db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return dbSourceToCommon(&row), nil
}

func (s *PostgresStore) ListSources(ctx context.Context) ([]common.Source, error) {
	var rows []DBSource
	if err := s.db.WithContext(ctx).Order("name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]common.Source, len(rows))
	for i, r := range rows {
		out[i] = *dbSourceToCommon(&r)
	}
	return out, nil
}

func (s *PostgresStore) UpdateSource(ctx context.Context, id uuid.UUID, name, schedule, dnEmailRegexp, groupRelationRegexp, filePath string) error {
	return s.db.WithContext(ctx).Model(&DBSource{}).Where("id = ?", id).
		Updates(map[string]any{
			"name":                  name,
			"schedule":              schedule,
			"dn_email_regexp":       dnEmailRegexp,
			"group_relation_regexp": groupRelationRegexp,
			"file_path":             filePath,
		}).Error
}

func (s *PostgresStore) DeleteSource(ctx context.Context, id uuid.UUID) error {
	// Tuples owned by this source are deleted first to maintain consistency.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ?", id).Delete(&DBTuple{}).Error; err != nil {
			return err
		}
		// Orphan groups that belonged to this source (set source_id to NULL).
		if err := tx.Model(&DBGroup{}).Where("source_id = ?", id).
			Update("source_id", nil).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&DBSource{}).Error
	})
}

func (s *PostgresStore) UpdateSourceSyncStatus(ctx context.Context, id uuid.UUID, status string, syncedAt time.Time) error {
	return s.db.WithContext(ctx).Model(&DBSource{}).Where("id = ?", id).
		Updates(map[string]any{"last_sync_status": status, "last_synced_at": syncedAt}).Error
}

func dbSourceToCommon(r *DBSource) *common.Source {
	return &common.Source{
		ID:                  r.ID,
		Name:                r.Name,
		Type:                r.Type,
		Schedule:            r.Schedule,
		DNEmailRegexp:       r.DNEmailRegexp,
		GroupRelationRegexp: r.GroupRelationRegexp,
		FilePath:            r.FilePath,
		LastSyncedAt:        r.LastSyncedAt,
		LastSyncStatus:      r.LastSyncStatus,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

// ── Sync Logs ─────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateSyncLog(ctx context.Context, l *common.SyncLog) error {
	row := DBSyncLog{
		ID:        l.ID,
		SourceID:  l.SourceID,
		StartedAt: l.StartedAt,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create sync log: %w", err)
	}
	return nil
}

func (s *PostgresStore) UpdateSyncLog(ctx context.Context, l *common.SyncLog) error {
	return s.db.WithContext(ctx).Model(&DBSyncLog{}).Where("id = ?", l.ID).
		Updates(map[string]any{
			"finished_at":    l.FinishedAt,
			"tuples_added":   l.TuplesAdded,
			"tuples_removed": l.TuplesRemoved,
			"error_message":  l.ErrorMessage,
			"notes":          strings.Join(l.Notes, "\n"),
		}).Error
}

func (s *PostgresStore) ListSyncLogs(ctx context.Context, sourceID uuid.UUID, limit int) ([]common.SyncLog, error) {
	q := s.db.WithContext(ctx).Model(&DBSyncLog{}).Where("source_id = ?", sourceID).Order("started_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []DBSyncLog
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]common.SyncLog, len(rows))
	for i, r := range rows {
		out[i] = common.SyncLog{
			ID:            r.ID,
			SourceID:      r.SourceID,
			StartedAt:     r.StartedAt,
			FinishedAt:    r.FinishedAt,
			TuplesAdded:   r.TuplesAdded,
			TuplesRemoved: r.TuplesRemoved,
			ErrorMessage:  r.ErrorMessage,
		}
		if r.Notes != "" {
			out[i].Notes = strings.Split(r.Notes, "\n")
		}
	}
	return out, nil
}

// ── ReplaceTuples ─────────────────────────────────────────────────────────────

// ReplaceTuples atomically replaces all tuples owned by sourceID with newTuples
// and applies the group descriptions carried by the import.
// It uses batch operations throughout to stay efficient with large imports.
func (s *PostgresStore) ReplaceTuples(ctx context.Context, sourceID uuid.UUID, newTuples []common.TuplePair, descriptions map[string]string) (added, removed int, err error) {
	const batchSize = 500

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Load only the three identifying columns — not the full row.
		type minTuple struct {
			ID       uuid.UUID `gorm:"column:id"`
			ObjID    string    `gorm:"column:obj_id"`
			Relation string    `gorm:"column:relation"`
			SubjType string    `gorm:"column:subj_type"`
			SubjID   string    `gorm:"column:subj_id"`
		}
		var existing []minTuple
		if err := tx.Model(&DBTuple{}).
			Select("id, obj_id, relation, subj_type, subj_id").
			Where("source_id = ?", sourceID).
			Find(&existing).Error; err != nil {
			return err
		}

		type key struct{ groupID, relation, memberType, memberID string }

		existingMap := make(map[key]uuid.UUID, len(existing))
		for _, t := range existing {
			existingMap[key{t.ObjID, t.Relation, t.SubjType, t.SubjID}] = t.ID
		}

		// Deduplicate incoming tuples and build the insert list + set of needed groups.
		newSet := make(map[key]struct{}, len(newTuples))
		var toInsert []DBTuple
		groupsNeeded := map[string]struct{}{}

		for _, p := range newTuples {
			k := key{p.GroupID, common.NormalizeRelation(p.Relation), p.MemberType, p.MemberID}
			if _, dup := newSet[k]; dup {
				continue
			}
			newSet[k] = struct{}{}
			groupsNeeded[p.GroupID] = struct{}{}

			if _, exists := existingMap[k]; !exists {
				toInsert = append(toInsert, DBTuple{
					ObjID:    p.GroupID,
					Relation: k.relation,
					SubjType: p.MemberType,
					SubjID:   p.MemberID,
					SubjRel:  subjRelFor(p.MemberType),
					SourceID: &sourceID,
				})
			}
		}

		// Collect IDs to delete (present in old, absent in new).
		toDeleteIDs := make([]uuid.UUID, 0, len(existingMap))
		for k, id := range existingMap {
			if _, ok := newSet[k]; !ok {
				toDeleteIDs = append(toDeleteIDs, id)
			}
		}

		// Batch-delete in chunks to avoid oversized IN clauses.
		for i := 0; i < len(toDeleteIDs); i += batchSize {
			end := i + batchSize
			if end > len(toDeleteIDs) {
				end = len(toDeleteIDs)
			}
			if err := tx.Where("id IN ?", toDeleteIDs[i:end]).Delete(&DBTuple{}).Error; err != nil {
				return err
			}
		}
		removed = len(toDeleteIDs)

		// Auto-create missing group rows in one query + one batch insert.
		if len(groupsNeeded) > 0 {
			allGroupIDs := make([]string, 0, len(groupsNeeded))
			for id := range groupsNeeded {
				allGroupIDs = append(allGroupIDs, id)
			}
			var existingGroupIDs []string
			if err := tx.Model(&DBGroup{}).Where("id IN ?", allGroupIDs).Pluck("id", &existingGroupIDs).Error; err != nil {
				return err
			}
			existingGroupSet := make(map[string]struct{}, len(existingGroupIDs))
			for _, id := range existingGroupIDs {
				existingGroupSet[id] = struct{}{}
			}
			var newGroups []DBGroup
			for _, id := range allGroupIDs {
				if _, ok := existingGroupSet[id]; !ok {
					newGroups = append(newGroups, DBGroup{
						ID:          id,
						DisplayName: id,
						Description: descriptions[id],
						SourceID:    &sourceID,
					})
				}
			}
			if len(newGroups) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
					CreateInBatches(newGroups, batchSize).Error; err != nil {
					return err
				}
			}

			// Keep the description of groups that already existed in sync with the
			// import — a changed description in the source has to land in the DB.
			for _, id := range allGroupIDs {
				if _, existed := existingGroupSet[id]; !existed {
					continue
				}
				desc, ok := descriptions[id]
				if !ok {
					continue
				}
				if err := tx.Model(&DBGroup{}).Where("id = ? AND description <> ?", id, desc).
					Update("description", desc).Error; err != nil {
					return err
				}
			}
		}

		// Batch-insert new tuples.
		if len(toInsert) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
				CreateInBatches(toInsert, batchSize).Error; err != nil {
				return err
			}
		}
		added = len(toInsert)

		return nil
	})
	return
}
