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
	// SourceTypeKeycloak derives groups from the users' attributes in
	// Keycloak. There is at most one such source; it is created from the
	// configuration at startup, not through the API.
	SourceTypeKeycloak = "keycloak"
	// SourceTypeLocal holds the groups maintained here, through the API and the self-service UI. There is exactly one such source; it is created at startup, never synced, and owns every group no other source owns.
	SourceTypeLocal = "local"

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
	// SourceName is the name of the source the group belongs to, shown next to the group ("wwi23seb (ZWR)") and searchable like its other fields.
	SourceName string `json:"source_name,omitempty"`
	// Relations are the relations someone actually holds in this group
	// (besides member) — what a search can offer as "group:<id>#<relation>".
	Relations []string  `json:"relations,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	// ReservedNames are globs ("*" for any run of characters) of the group ids this source may create, now or later. No other source and not the API may create a group matching one, so a group made by hand today cannot block a source that brings it tomorrow.
	ReservedNames []string `json:"reserved_names,omitempty"`
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
	// Notes are what a successful run could not place — for the Keycloak
	// source, attribute values no rule maps. Not errors: the run imported
	// everything it understood.
	Notes []string `json:"notes,omitempty"`
}

// Membership is one direct entry of a group as the people maintaining it see it: who, in which relation, and until when.
type Membership struct {
	// Member is a token: "user:<email>", "group:<id>" or "pattern:<glob>".
	Member   string `json:"member"`
	Relation string `json:"relation"`
	// ExpiresAt, if set, is when the entry stops counting. An expired entry grants nothing but stays listed until somebody removes it, so it is visible that it ran out.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Expired   bool       `json:"expired,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// Manager is a token allowed to maintain a group: "user:<email>", "group:<id>" or "group:<id>#<relation>". Managing a group is not being in it; a manager gains none of the group's rights.
type Manager struct {
	Token     string    `json:"token"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Change actions recorded in a group's history.
const (
	ChangeGroupCreated   = "group_created"
	ChangeGroupUpdated   = "group_updated"
	ChangeGroupDeleted   = "group_deleted"
	ChangeMemberAdded    = "member_added"
	ChangeMemberRemoved  = "member_removed"
	ChangeManagerAdded   = "manager_added"
	ChangeManagerRemoved = "manager_removed"
	ChangeGroupAdopted   = "group_adopted"
	// ChangeRelationChanged: Relation is the new relation, Detail the one before.
	ChangeRelationChanged = "relation_changed"
)

// Change is one entry in the history of a group maintained here. Changes made by sync sources are not recorded here; their sync log says what a run changed.
type Change struct {
	ID      uuid.UUID `json:"id"`
	GroupID string    `json:"group_id"`
	At      time.Time `json:"at"`
	// Actor is who made the change: a person's identity, "api-token" for a service with a write token, or "system" for what the service did at startup.
	Actor string `json:"actor"`
	// OnBehalfOf is the person an admin acted for (X-Act-As); empty otherwise.
	OnBehalfOf string     `json:"on_behalf_of,omitempty"`
	Action     string     `json:"action"`
	Member     string     `json:"member,omitempty"`
	Relation   string     `json:"relation,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Detail     string     `json:"detail,omitempty"`
}

// TuplePair is a single group→member relationship used by the sync engine.
type TuplePair struct {
	GroupID    string // raw group name, no "group:" prefix
	Relation   string // "member" or one of the configured relations; "" means "member"
	MemberType string // "user" or "group"
	MemberID   string // email or group name, no prefix
}

// NormalizeID is the one spelling of an id stored and looked up here — an
// email address, a group id, a pattern. Lowercase, because none of them may
// tell two things apart by case: mail systems compare addresses
// case-insensitively and identity providers release them in whatever case the
// directory holds, and a group "Leiter-ZWR" next to "leiter-zwr" is a typo that
// grants nothing, not a second group.
func NormalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
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
