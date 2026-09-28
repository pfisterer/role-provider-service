package common

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	GroupPrefix = "group:"
	UserPrefix  = "user:"
	// PatternPrefix marks an email-glob membership rule (see MatchEmailPattern):
	// every email matching the glob is a member of the group.
	PatternPrefix = "pattern:"

	// RelationMember is the relation every group has and every other relation
	// implies: holding "dozent" in a course makes you a member of it too.
	RelationMember = "member"
	// RelationSeparator joins a group and a relation in a token:
	// "group:wwi23seb#dozent". Membership itself has no suffix ("group:wwi23seb"),
	// so the tokens consumers already compare keep their meaning.
	RelationSeparator = "#"

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
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"` // "csv" | "ldif"
	Schedule      string    `json:"schedule,omitempty"`
	DNEmailRegexp string    `json:"dn_email_regexp,omitempty"`
	// GroupRelationRegexp (LDIF only) turns an LDAP group into a relation on
	// another group: matched against the CN, its named captures "group" and
	// "relation" name the target, e.g. `^(?P<group>.+)-(?P<relation>dozent)$`
	// maps "wwi23seb-dozent" to wwi23seb#dozent. A CN that does not match stays
	// a plain group.
	GroupRelationRegexp string     `json:"group_relation_regexp,omitempty"`
	FilePath            string     `json:"file_path,omitempty"`
	LastSyncedAt        *time.Time `json:"last_synced_at,omitempty"`
	LastSyncStatus      string     `json:"last_sync_status,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
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
	Relation   string // "member" or one of the configured relations; "" means "member"
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
	if after, ok := strings.CutPrefix(token, PatternPrefix); ok {
		return "pattern", after
	}
	return "", ""
}

// BuildToken builds "group:<id>" or "user:<id>" from parts.
func BuildToken(typ, id string) string {
	return typ + ":" + id
}

// SplitGroupRelation splits a group id as it appears after "group:" into the
// group and its relation: "wwi23seb#dozent" → ("wwi23seb", "dozent"),
// "wwi23seb" → ("wwi23seb", "member").
func SplitGroupRelation(id string) (group, relation string) {
	if g, rel, ok := strings.Cut(id, RelationSeparator); ok {
		return g, rel
	}
	return id, RelationMember
}

// GroupToken builds the token for a relation on a group. Membership is the
// plain "group:<id>", so a consumer that only knows groups sees no change.
func GroupToken(groupID, relation string) string {
	if relation == "" || relation == RelationMember {
		return GroupPrefix + groupID
	}
	return GroupPrefix + groupID + RelationSeparator + relation
}

// NormalizeRelation maps the empty relation to "member".
func NormalizeRelation(relation string) string {
	if relation == "" {
		return RelationMember
	}
	return relation
}
