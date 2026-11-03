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
	// ReservedNames, comma-separated globs; AutoMigrate adds the column with its default.
	ReservedNames string `gorm:"not null;default:''"`
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
	// ExpiresAt, if set, is when the tuple stops counting. Only tuples maintained here carry one; every query that resolves membership skips expired tuples (see liveTuple).
	ExpiresAt *time.Time
}

func (DBTuple) TableName() string { return "tuples" }

// liveTuple is the condition every membership query puts on a tuple: not expired.
const liveTuple = "(expires_at IS NULL OR expires_at > now())"

// DBManager is a token allowed to maintain a group. Kept apart from the tuples on purpose: every relation in the tuples implies membership, and maintaining a group must not hand out what the group grants.
type DBManager struct {
	GroupID   string `gorm:"primaryKey"`
	Token     string `gorm:"primaryKey"`
	CreatedBy string `gorm:"not null;default:''"`
	CreatedAt time.Time
}

func (DBManager) TableName() string { return "group_managers" }

// DBChange is one entry in the history of a group maintained here.
type DBChange struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	GroupID    string    `gorm:"not null;index"`
	At         time.Time `gorm:"not null;index"`
	Actor      string    `gorm:"not null"`
	OnBehalfOf string    `gorm:"not null;default:''"`
	Action     string    `gorm:"not null"`
	Member     string    `gorm:"not null;default:''"`
	Relation   string    `gorm:"not null;default:''"`
	ExpiresAt  *time.Time
	Detail     string `gorm:"not null;default:''"`
}

func (DBChange) TableName() string { return "group_changes" }

// ── Store interface ───────────────────────────────────────────────────────────

type Store interface {
	// Groups
	CreateGroup(ctx context.Context, group *common.Group) error
	GetGroup(ctx context.Context, id string) (*common.Group, error)
	ListGroups(ctx context.Context, query string, sourceID *uuid.UUID, limit int) ([]common.Group, error)
	UpdateGroup(ctx context.Context, id, displayName, description string) error
	DeleteGroup(ctx context.Context, id string) error
	// GroupRelations returns, per group, the relations other than member that
	// someone holds there directly.
	GroupRelations(ctx context.Context) (map[string][]string, error)
	// GroupOwners returns the owning source of each of the given groups that
	// exists; a nil value means the group was created through the API.
	GroupOwners(ctx context.Context, ids []string) (map[string]*uuid.UUID, error)

	// Members (tuples)
	// relation is "member" (or "") or a configured relation; asking for
	// "member" in the listings means everyone in the group, whatever their
	// relation, since every relation implies membership.
	// AddMember adds a tuple, or sets the expiry of the one that exists; expiresAt nil means it does not expire.
	AddMember(ctx context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID, expiresAt *time.Time) error
	RemoveMember(ctx context.Context, groupID, relation, memberType, memberID string) error
	GetDirectMembers(ctx context.Context, groupID, relation string) ([]string, error)
	// ListMemberships returns every direct entry of a group with relation and expiry, patterns and expired entries included.
	ListMemberships(ctx context.Context, groupID string) ([]common.Membership, error)
	GetAllMembers(ctx context.Context, groupID, relation string) ([]string, error)
	GetUserTokens(ctx context.Context, email string) ([]string, error)
	SearchUsers(ctx context.Context, query string, limit int) ([]string, error)

	// Sources
	CreateSource(ctx context.Context, s *common.Source) error
	GetSource(ctx context.Context, id uuid.UUID) (*common.Source, error)
	ListSources(ctx context.Context) ([]common.Source, error)
	// UpdateSource writes the source's settings: name, schedule, regexps, file path and reserved names.
	UpdateSource(ctx context.Context, src *common.Source) error
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
	// MoveToSource hands every group and tuple of one source to another, in one transaction, and returns the ids of the groups moved. from nil means the groups and tuples that belong to no source.
	MoveToSource(ctx context.Context, from *uuid.UUID, to uuid.UUID) ([]string, error)

	// Managers
	ListManagers(ctx context.Context, groupID string) ([]common.Manager, error)
	AddManager(ctx context.Context, groupID string, m common.Manager) error
	RemoveManager(ctx context.Context, groupID, token string) error
	// ManagedGroups returns the groups any of the tokens may maintain.
	ManagedGroups(ctx context.Context, tokens []string) ([]string, error)

	// History
	AddChange(ctx context.Context, c *common.Change) error
	ListChanges(ctx context.Context, groupID string, limit int) ([]common.Change, error)
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

	if err := db.AutoMigrate(&DBGroup{}, &DBSource{}, &DBSyncLog{}, &DBTuple{}, &DBManager{}, &DBChange{}); err != nil {
		return nil, fmt.Errorf("failed to migrate database schema: %w", err)
	}
	if err := normalizeStoredIDs(db); err != nil {
		return nil, fmt.Errorf("failed to normalize stored ids: %w", err)
	}
	if err := addIDConstraints(db); err != nil {
		return nil, fmt.Errorf("failed to add id constraints: %w", err)
	}

	log.Info("PostgreSQL storage initialized and schema migrated")
	return &PostgresStore{db: db, log: log}, nil
}

func (s *PostgresStore) GroupRelations(ctx context.Context) (map[string][]string, error) {
	var rows []struct {
		ObjID    string `gorm:"column:obj_id"`
		Relation string `gorm:"column:relation"`
	}
	if err := s.db.WithContext(ctx).Raw(
		`SELECT DISTINCT obj_id, relation FROM tuples WHERE relation <> 'member' AND ` + liveTuple + ` ORDER BY obj_id, relation`).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.ObjID] = append(out[r.ObjID], r.Relation)
	}
	return out, nil
}

func (s *PostgresStore) GroupOwners(ctx context.Context, ids []string) (map[string]*uuid.UUID, error) {
	out := map[string]*uuid.UUID{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []DBGroup
	if err := s.db.WithContext(ctx).Select("id, source_id").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.SourceID
	}
	return out, nil
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

// idConstraints are the CHECK constraints that hold group ids to common.GroupIDPattern in the database too, so no path that bypasses the service's validation can store an id that splits a token or a list.
var idConstraints = []struct{ table, name, check string }{
	{"groups", "groups_id_format", "id ~ '" + common.GroupIDPattern + "'"},
	{"tuples", "tuples_obj_id_format", "obj_id ~ '" + common.GroupIDPattern + "'"},
	{"tuples", "tuples_group_subj_id_format", "subj_type <> 'group' OR subj_id ~ '" + common.GroupIDPattern + "'"},
}

// addIDConstraints adds each constraint in idConstraints that does not exist yet. Adding one validates every existing row, so data that does not comply stops the start with the constraint's name instead of being kept silently.
func addIDConstraints(db *gorm.DB) error {
	for _, c := range idConstraints {
		var exists bool
		if err := db.Raw(`SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = ? AND conrelid = ?::regclass)`, c.name, c.table).
			Scan(&exists).Error; err != nil {
			return err
		}
		if exists {
			continue
		}
		if err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)`, c.table, c.name, c.check)).Error; err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
	}
	return nil
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

// DeleteGroup removes the group together with its tuples and manager entries, in one transaction: its members (obj_id), its memberships in other groups (a group subject), its managers and the places it manages. Left behind, its members would keep group:<id> in their tokens, and a group created later under the same id would inherit them.
func (s *PostgresStore) DeleteGroup(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("obj_id = ? OR (subj_type = 'group' AND subj_id = ?)", id, id).Delete(&DBTuple{}).Error; err != nil {
			return err
		}
		// A manager entry naming the group goes too: a group created later under the same id must not inherit who maintains what.
		if err := tx.Where("group_id = ? OR token = ? OR token LIKE ?", id, common.GroupPrefix+id, common.GroupPrefix+id+common.RelationSeparator+"%").Delete(&DBManager{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&DBGroup{}).Error
	})
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

func (s *PostgresStore) AddMember(ctx context.Context, groupID, relation, memberType, memberID string, sourceID *uuid.UUID, expiresAt *time.Time) error {
	tuple := DBTuple{
		ObjID:     groupID,
		Relation:  common.NormalizeRelation(relation),
		SubjType:  memberType,
		SubjID:    memberID,
		SubjRel:   subjRelFor(memberType),
		SourceID:  sourceID,
		ExpiresAt: expiresAt,
	}
	// Adding an entry that exists sets its expiry: extending a membership is adding it again with a later date.
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "obj_id"}, {Name: "relation"}, {Name: "subj_type"}, {Name: "subj_id"}, {Name: "subj_rel"}},
		DoUpdates: clause.AssignmentColumns([]string{"expires_at"}),
	}).Create(&tuple).Error
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
	q := s.db.WithContext(ctx).Where("obj_id = ? AND subj_type <> 'pattern' AND "+liveTuple, groupID)
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

func (s *PostgresStore) ListMemberships(ctx context.Context, groupID string) ([]common.Membership, error) {
	var rows []DBTuple
	if err := s.db.WithContext(ctx).Where("obj_id = ?", groupID).Order("subj_type, subj_id, relation").Find(&rows).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]common.Membership, len(rows))
	for i, r := range rows {
		out[i] = common.Membership{
			Member:    common.BuildToken(r.SubjType, r.SubjID),
			Relation:  r.Relation,
			ExpiresAt: r.ExpiresAt,
			Expired:   r.ExpiresAt != nil && !r.ExpiresAt.After(now),
			CreatedAt: r.CreatedAt,
		}
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
    WHERE obj_id = $1 AND ($2 = '' OR relation = $2) AND ` + liveTuple + `
  UNION
    SELECT t.subj_type, t.subj_id
    FROM tuples t
    INNER JOIN expand e ON e.subj_type = 'group' AND t.obj_id = e.subj_id
    WHERE (t.expires_at IS NULL OR t.expires_at > now())
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
	// matched group IDs are injected into the CTE seed via $2, bound as a real
	// text[] parameter: a list joined into a string would let one id split into
	// several.
	patterns, err := s.patternMatches(ctx, email)
	if err != nil {
		return nil, err
	}
	patternGroups := make([]string, 0, len(patterns))
	for _, p := range patterns {
		patternGroups = append(patternGroups, p.ObjID)
	}
	// user_groups: membership, any relation. The final SELECT adds the
	// non-member relations held directly or through one of those groups.
	const query = `
WITH RECURSIVE user_groups AS (
    SELECT obj_id
    FROM tuples
    WHERE subj_type = 'user' AND subj_id = $1 AND ` + liveTuple + `
  UNION
    SELECT unnest($2::text[])
  UNION
    SELECT t.obj_id
    FROM tuples t
    INNER JOIN user_groups ug ON t.subj_type = 'group' AND t.subj_id = ug.obj_id
        AND t.subj_rel = 'member'
    WHERE (t.expires_at IS NULL OR t.expires_at > now())
)
SELECT obj_id, 'member' AS relation FROM user_groups
UNION
SELECT t.obj_id, t.relation
FROM tuples t
WHERE t.relation <> 'member' AND (t.expires_at IS NULL OR t.expires_at > now()) AND (
    (t.subj_type = 'user' AND t.subj_id = $1)
    OR (t.subj_type = 'group' AND t.subj_rel = 'member' AND t.subj_id IN (SELECT obj_id FROM user_groups))
)
ORDER BY relation, obj_id`

	type row struct {
		ObjID    string `gorm:"column:obj_id"`
		Relation string `gorm:"column:relation"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).Raw(query, email, patternGroups).Scan(&rows).Error; err != nil {
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
		Where("subj_type = 'user' AND " + liveTuple)
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
		Raw(`SELECT obj_id, relation, subj_id FROM tuples WHERE subj_type = 'pattern' AND ` + liveTuple).
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
		ReservedNames:       strings.Join(src.ReservedNames, ","),
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

func (s *PostgresStore) UpdateSource(ctx context.Context, src *common.Source) error {
	return s.db.WithContext(ctx).Model(&DBSource{}).Where("id = ?", src.ID).
		Updates(map[string]any{
			"name":                  src.Name,
			"schedule":              src.Schedule,
			"dn_email_regexp":       src.DNEmailRegexp,
			"group_relation_regexp": src.GroupRelationRegexp,
			"file_path":             src.FilePath,
			"reserved_names":        strings.Join(src.ReservedNames, ","),
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
		ReservedNames:       splitList(r.ReservedNames),
		LastSyncedAt:        r.LastSyncedAt,
		LastSyncStatus:      r.LastSyncStatus,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

// splitList reads a comma-separated column; empty means none.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
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

// ── Moving between sources ───────────────────────────────────────────────────

func (s *PostgresStore) MoveToSource(ctx context.Context, from *uuid.UUID, to uuid.UUID) ([]string, error) {
	var moved []string
	owned := func(q *gorm.DB) *gorm.DB {
		if from == nil {
			return q.Where("source_id IS NULL")
		}
		return q.Where("source_id = ?", *from)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := owned(tx.Model(&DBGroup{})).Order("id").Pluck("id", &moved).Error; err != nil {
			return err
		}
		if err := owned(tx.Model(&DBGroup{})).Update("source_id", to).Error; err != nil {
			return err
		}
		return owned(tx.Model(&DBTuple{})).Update("source_id", to).Error
	})
	if err != nil {
		return nil, err
	}
	return moved, nil
}

// ── Managers ─────────────────────────────────────────────────────────────────

func (s *PostgresStore) ListManagers(ctx context.Context, groupID string) ([]common.Manager, error) {
	var rows []DBManager
	if err := s.db.WithContext(ctx).Where("group_id = ?", groupID).Order("token").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]common.Manager, len(rows))
	for i, r := range rows {
		out[i] = common.Manager{Token: r.Token, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

func (s *PostgresStore) AddManager(ctx context.Context, groupID string, m common.Manager) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&DBManager{GroupID: groupID, Token: m.Token, CreatedBy: m.CreatedBy}).Error
}

func (s *PostgresStore) RemoveManager(ctx context.Context, groupID, token string) error {
	return s.db.WithContext(ctx).Where("group_id = ? AND token = ?", groupID, token).Delete(&DBManager{}).Error
}

func (s *PostgresStore) ManagedGroups(ctx context.Context, tokens []string) ([]string, error) {
	var ids []string
	if len(tokens) == 0 {
		return ids, nil
	}
	err := s.db.WithContext(ctx).Model(&DBManager{}).Distinct("group_id").Where("token IN ?", tokens).Order("group_id").Pluck("group_id", &ids).Error
	return ids, err
}

// ── History ──────────────────────────────────────────────────────────────────

func (s *PostgresStore) AddChange(ctx context.Context, c *common.Change) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	return s.db.WithContext(ctx).Create(&DBChange{
		ID: c.ID, GroupID: c.GroupID, At: c.At, Actor: c.Actor, OnBehalfOf: c.OnBehalfOf,
		Action: c.Action, Member: c.Member, Relation: c.Relation, ExpiresAt: c.ExpiresAt, Detail: c.Detail,
	}).Error
}

func (s *PostgresStore) ListChanges(ctx context.Context, groupID string, limit int) ([]common.Change, error) {
	q := s.db.WithContext(ctx).Where("group_id = ?", groupID).Order("at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []DBChange
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]common.Change, len(rows))
	for i, r := range rows {
		out[i] = common.Change{
			ID: r.ID, GroupID: r.GroupID, At: r.At, Actor: r.Actor, OnBehalfOf: r.OnBehalfOf,
			Action: r.Action, Member: r.Member, Relation: r.Relation, ExpiresAt: r.ExpiresAt, Detail: r.Detail,
		}
	}
	return out, nil
}
