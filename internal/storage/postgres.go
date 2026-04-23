package storage

import (
	"context"
	"fmt"
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
	ID             uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name           string     `gorm:"not null"`
	Type           string     `gorm:"not null"`
	Schedule       string     `gorm:"not null;default:''"`
	DNEmailRegexp  string     `gorm:"not null;default:''"`
	FilePath       string     `gorm:"not null;default:''"`
	LastSyncedAt   *time.Time
	LastSyncStatus string `gorm:"not null;default:''"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (DBSource) TableName() string { return "sources" }

type DBSyncLog struct {
	ID            uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	SourceID      uuid.UUID  `gorm:"type:uuid;not null;index"`
	StartedAt     time.Time  `gorm:"not null"`
	FinishedAt    *time.Time
	TuplesAdded   int `gorm:"not null;default:0"`
	TuplesRemoved int `gorm:"not null;default:0"`
	ErrorMessage  string
	CreatedAt     time.Time
}

func (DBSyncLog) TableName() string { return "sync_logs" }

// DBTuple is a single Zanzibar-style relationship tuple.
// obj_type is always "group". subj_rel is "member" when subj_type is "group", else "".
type DBTuple struct {
	ID       uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ObjID    string     `gorm:"not null;uniqueIndex:idx_tuple_uniq"`
	Relation string     `gorm:"not null;default:'member';uniqueIndex:idx_tuple_uniq"`
	SubjType string     `gorm:"not null;uniqueIndex:idx_tuple_uniq"` // "user" | "group"
	SubjID   string     `gorm:"not null;uniqueIndex:idx_tuple_uniq"`
	SubjRel  string     `gorm:"not null;default:'';uniqueIndex:idx_tuple_uniq"` // "" | "member"
	SourceID *uuid.UUID `gorm:"type:uuid;index"`
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
	AddMember(ctx context.Context, groupID, memberType, memberID string, sourceID *uuid.UUID) error
	RemoveMember(ctx context.Context, groupID, memberType, memberID string) error
	GetDirectMembers(ctx context.Context, groupID string) ([]string, error)
	GetAllMembers(ctx context.Context, groupID string) ([]string, error)
	GetUserTokens(ctx context.Context, email string) ([]string, error)

	// Sources
	CreateSource(ctx context.Context, s *common.Source) error
	GetSource(ctx context.Context, id uuid.UUID) (*common.Source, error)
	ListSources(ctx context.Context) ([]common.Source, error)
	UpdateSource(ctx context.Context, id uuid.UUID, name, schedule, dnEmailRegexp, filePath string) error
	DeleteSource(ctx context.Context, id uuid.UUID) error
	UpdateSourceSyncStatus(ctx context.Context, id uuid.UUID, status string, syncedAt time.Time) error

	// Sync logs
	CreateSyncLog(ctx context.Context, l *common.SyncLog) error
	UpdateSyncLog(ctx context.Context, l *common.SyncLog) error
	ListSyncLogs(ctx context.Context, sourceID uuid.UUID, limit int) ([]common.SyncLog, error)

	// Atomic tuple replacement for a source (used during sync)
	ReplaceTuples(ctx context.Context, sourceID uuid.UUID, newTuples []common.TuplePair) (added, removed int, err error)
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

	if err := db.AutoMigrate(&DBGroup{}, &DBSource{}, &DBSyncLog{}, &DBTuple{}); err != nil {
		return nil, fmt.Errorf("failed to migrate database schema: %w", err)
	}

	log.Info("PostgreSQL storage initialized and schema migrated")
	return &PostgresStore{db: db, log: log}, nil
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
		q = q.Where("id ILIKE ? OR display_name ILIKE ?", "%"+query+"%", "%"+query+"%")
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

func (s *PostgresStore) AddMember(ctx context.Context, groupID, memberType, memberID string, sourceID *uuid.UUID) error {
	subjRel := ""
	if memberType == "group" {
		subjRel = "member"
	}
	tuple := DBTuple{
		ObjID:    groupID,
		Relation: "member",
		SubjType: memberType,
		SubjID:   memberID,
		SubjRel:  subjRel,
		SourceID: sourceID,
	}
	// ON CONFLICT DO NOTHING — idempotent
	return s.db.WithContext(ctx).
		Where("obj_id = ? AND relation = 'member' AND subj_type = ? AND subj_id = ? AND subj_rel = ?",
			groupID, memberType, memberID, subjRel).
		FirstOrCreate(&tuple).Error
}

func (s *PostgresStore) RemoveMember(ctx context.Context, groupID, memberType, memberID string) error {
	subjRel := ""
	if memberType == "group" {
		subjRel = "member"
	}
	return s.db.WithContext(ctx).
		Where("obj_id = ? AND relation = 'member' AND subj_type = ? AND subj_id = ? AND subj_rel = ?",
			groupID, memberType, memberID, subjRel).
		Delete(&DBTuple{}).Error
}

// GetDirectMembers returns members that are directly in groupID (no recursion).
func (s *PostgresStore) GetDirectMembers(ctx context.Context, groupID string) ([]string, error) {
	var rows []DBTuple
	if err := s.db.WithContext(ctx).
		Where("obj_id = ? AND relation = 'member'", groupID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = common.BuildToken(r.SubjType, r.SubjID)
	}
	return out, nil
}

// GetAllMembers resolves all transitive user members of a group using a recursive CTE.
func (s *PostgresStore) GetAllMembers(ctx context.Context, groupID string) ([]string, error) {
	const query = `
WITH RECURSIVE expand AS (
    SELECT subj_type, subj_id, subj_rel
    FROM tuples
    WHERE obj_id = $1 AND relation = 'member'
  UNION
    SELECT t.subj_type, t.subj_id, t.subj_rel
    FROM tuples t
    INNER JOIN expand e ON e.subj_type = 'group' AND e.subj_rel = 'member'
        AND t.obj_id = e.subj_id AND t.relation = 'member'
)
SELECT DISTINCT subj_type, subj_id FROM expand`

	type row struct {
		SubjType string `gorm:"column:subj_type"`
		SubjID   string `gorm:"column:subj_id"`
	}
	var rows []row
	if err := s.db.WithContext(ctx).Raw(query, groupID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = common.BuildToken(r.SubjType, r.SubjID)
	}
	return out, nil
}

// GetUserTokens returns all group tokens for a user (reverse recursive lookup).
func (s *PostgresStore) GetUserTokens(ctx context.Context, email string) ([]string, error) {
	const query = `
WITH RECURSIVE user_groups AS (
    SELECT obj_id
    FROM tuples
    WHERE subj_type = 'user' AND subj_id = $1 AND relation = 'member'
  UNION
    SELECT t.obj_id
    FROM tuples t
    INNER JOIN user_groups ug ON t.subj_type = 'group' AND t.subj_id = ug.obj_id
        AND t.subj_rel = 'member' AND t.relation = 'member'
)
SELECT DISTINCT obj_id FROM user_groups`

	var ids []string
	if err := s.db.WithContext(ctx).Raw(query, email).Scan(&ids).Error; err != nil {
		return nil, err
	}
	tokens := make([]string, 0, len(ids)+1)
	tokens = append(tokens, common.UserPrefix+email) // always include own user token
	for _, id := range ids {
		tokens = append(tokens, common.GroupPrefix+id)
	}
	return tokens, nil
}

// ── Sources ───────────────────────────────────────────────────────────────────

func (s *PostgresStore) CreateSource(ctx context.Context, src *common.Source) error {
	row := DBSource{
		ID:            src.ID,
		Name:          src.Name,
		Type:          src.Type,
		Schedule:      src.Schedule,
		DNEmailRegexp: src.DNEmailRegexp,
		FilePath:      src.FilePath,
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

func (s *PostgresStore) UpdateSource(ctx context.Context, id uuid.UUID, name, schedule, dnEmailRegexp, filePath string) error {
	return s.db.WithContext(ctx).Model(&DBSource{}).Where("id = ?", id).
		Updates(map[string]any{
			"name":            name,
			"schedule":        schedule,
			"dn_email_regexp": dnEmailRegexp,
			"file_path":       filePath,
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
		ID:             r.ID,
		Name:           r.Name,
		Type:           r.Type,
		Schedule:       r.Schedule,
		DNEmailRegexp:  r.DNEmailRegexp,
		FilePath:       r.FilePath,
		LastSyncedAt:   r.LastSyncedAt,
		LastSyncStatus: r.LastSyncStatus,
		CreatedAt:      r.CreatedAt,
		UpdatedAt:      r.UpdatedAt,
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
	}
	return out, nil
}

// ── ReplaceTuples ─────────────────────────────────────────────────────────────

// ReplaceTuples atomically replaces all tuples owned by sourceID with newTuples.
// It uses batch operations throughout to stay efficient with large imports.
func (s *PostgresStore) ReplaceTuples(ctx context.Context, sourceID uuid.UUID, newTuples []common.TuplePair) (added, removed int, err error) {
	const batchSize = 500

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Load only the three identifying columns — not the full row.
		type minTuple struct {
			ID       uuid.UUID `gorm:"column:id"`
			ObjID    string    `gorm:"column:obj_id"`
			SubjType string    `gorm:"column:subj_type"`
			SubjID   string    `gorm:"column:subj_id"`
		}
		var existing []minTuple
		if err := tx.Model(&DBTuple{}).
			Select("id, obj_id, subj_type, subj_id").
			Where("source_id = ?", sourceID).
			Find(&existing).Error; err != nil {
			return err
		}

		type key struct{ groupID, memberType, memberID string }

		existingMap := make(map[key]uuid.UUID, len(existing))
		for _, t := range existing {
			existingMap[key{t.ObjID, t.SubjType, t.SubjID}] = t.ID
		}

		// Deduplicate incoming tuples and build the insert list + set of needed groups.
		newSet := make(map[key]struct{}, len(newTuples))
		var toInsert []DBTuple
		groupsNeeded := map[string]struct{}{}

		for _, p := range newTuples {
			k := key{p.GroupID, p.MemberType, p.MemberID}
			if _, dup := newSet[k]; dup {
				continue
			}
			newSet[k] = struct{}{}
			groupsNeeded[p.GroupID] = struct{}{}

			if _, exists := existingMap[k]; !exists {
				subjRel := ""
				if p.MemberType == "group" {
					subjRel = "member"
				}
				toInsert = append(toInsert, DBTuple{
					ObjID:    p.GroupID,
					Relation: "member",
					SubjType: p.MemberType,
					SubjID:   p.MemberID,
					SubjRel:  subjRel,
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
					newGroups = append(newGroups, DBGroup{ID: id, DisplayName: id, SourceID: &sourceID})
				}
			}
			if len(newGroups) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
					CreateInBatches(newGroups, batchSize).Error; err != nil {
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
