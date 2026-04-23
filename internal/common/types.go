package common

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	GroupPrefix = "group:"
	UserPrefix  = "user:"

	SourceTypeCSV  = "csv"
	SourceTypeLDIF = "ldif"

	SyncStatusOK      = "ok"
	SyncStatusError   = "error"
	SyncStatusRunning = "running"
)

// Group represents a named group with optional description.
type Group struct {
	ID          string     `json:"id"`    // short ID, e.g. "dept_cs_faculty"
	Token       string     `json:"token"` // full token, e.g. "group:dept_cs_faculty"
	DisplayName string     `json:"display_name"`
	Description string     `json:"description"`
	SourceID    *uuid.UUID `json:"source_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Source represents a sync source (CSV or LDIF file).
type Source struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Type           string     `json:"type"` // "csv" | "ldif"
	Schedule       string     `json:"schedule,omitempty"`
	DNEmailRegexp  string     `json:"dn_email_regexp,omitempty"`
	FilePath       string     `json:"file_path,omitempty"`
	LastSyncedAt   *time.Time `json:"last_synced_at,omitempty"`
	LastSyncStatus string     `json:"last_sync_status,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// SyncLog records the result of a single sync run.
type SyncLog struct {
	ID            uuid.UUID  `json:"id"`
	SourceID      uuid.UUID  `json:"source_id"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	TuplesAdded   int        `json:"tuples_added"`
	TuplesRemoved int        `json:"tuples_removed"`
	ErrorMessage  string     `json:"error_message,omitempty"`
}

// TuplePair is a single group→member relationship used by the sync engine.
type TuplePair struct {
	GroupID    string // raw group name, no "group:" prefix
	MemberType string // "user" or "group"
	MemberID   string // email or group name, no prefix
}

// ParseToken splits a token like "group:foo" or "user:bar" into (type, id).
// Returns ("", "") if the format is invalid.
func ParseToken(token string) (typ, id string) {
	if after, ok := strings.CutPrefix(token, GroupPrefix); ok {
		return "group", after
	}
	if after, ok := strings.CutPrefix(token, UserPrefix); ok {
		return "user", after
	}
	return "", ""
}

// BuildToken builds "group:<id>" or "user:<id>" from parts.
func BuildToken(typ, id string) string {
	return typ + ":" + id
}
