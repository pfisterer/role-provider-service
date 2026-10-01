// Package keycloak derives group memberships from the attributes the identity
// provider already holds about every person who has signed in — today the
// bwIDM affiliation ("student@dhbw-mannheim.de"). Nothing is imported by hand:
// the groups follow what the home institution says, on every sync.
package keycloak

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/pfisterer/role-provider-service/internal/common"
)

// Mapping turns attribute values of the form "<value>@<scope>" into groups. It
// is two independent tables — scope to location, value to role — rather than a
// list of combinations, so a combination nobody has had yet (the first student
// from a location that so far only sent staff) needs no new entry.
//
// Anything the tables do not name is reported, never guessed: reading an
// unknown value as a role could hand someone budgets meant for others, and an
// unknown scope could belong to anyone.
type Mapping struct {
	// Attribute is the user attribute holding the values, e.g.
	// "edu_person_affiliation".
	Attribute string `json:"attribute"`
	// AllGroup, when set, is the group every mapped person joins in addition
	// to their location's — the same role, across all locations.
	AllGroup *MappedGroup `json:"all_group,omitempty"`
	// Locations maps a scope ("dhbw-mannheim.de") to the group for it.
	Locations map[string]MappedGroup `json:"locations"`
	// Roles maps a value ("student") to the relation it grants in the
	// location's group ("studierende"). A person with a mapped scope but no
	// mapped value is a plain member of the location.
	Roles map[string]string `json:"roles"`
	// IgnoredScopes are scopes known and deliberately left without a group
	// (other universities), so they are not reported as unknown.
	IgnoredScopes []string `json:"ignored_scopes,omitempty"`
	// IgnoredValues are values known to carry no role ("member", "affiliate").
	IgnoredValues []string `json:"ignored_values,omitempty"`
}

// MappedGroup names a group this source maintains, and how it is described.
type MappedGroup struct {
	Group       string `json:"group"`
	Description string `json:"description,omitempty"`
}

// ParseMapping reads a mapping from JSON and checks it against the configured
// relations: a role mapped to a relation that does not exist would fail every
// sync, so it fails at startup instead.
func ParseMapping(raw string, relations common.Relations) (Mapping, error) {
	var m Mapping
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Mapping{}, fmt.Errorf("keycloak mapping: %w", err)
	}
	if strings.TrimSpace(m.Attribute) == "" {
		return Mapping{}, fmt.Errorf("keycloak mapping: attribute must be set")
	}
	if len(m.Locations) == 0 {
		return Mapping{}, fmt.Errorf("keycloak mapping: no locations")
	}

	locations := make(map[string]MappedGroup, len(m.Locations))
	for scope, g := range m.Locations {
		g.Group = common.NormalizeID(g.Group)
		if g.Group == "" {
			return Mapping{}, fmt.Errorf("keycloak mapping: location %q has no group", scope)
		}
		locations[common.NormalizeID(scope)] = g
	}
	m.Locations = locations
	if m.AllGroup != nil {
		m.AllGroup.Group = common.NormalizeID(m.AllGroup.Group)
		if m.AllGroup.Group == "" {
			return Mapping{}, fmt.Errorf("keycloak mapping: all_group has no group")
		}
	}

	// In a fixed order, so the same mapping always fails with the same message.
	roles := make(map[string]string, len(m.Roles))
	values := make([]string, 0, len(m.Roles))
	for value := range m.Roles {
		values = append(values, value)
	}
	slices.Sort(values)
	for _, value := range values {
		relation := m.Roles[value]
		rel, err := relations.Check(relation)
		if err != nil {
			return Mapping{}, fmt.Errorf("keycloak mapping: role %q: %w", value, err)
		}
		roles[common.NormalizeID(value)] = rel
	}
	m.Roles = roles
	for i := range m.IgnoredScopes {
		m.IgnoredScopes[i] = common.NormalizeID(m.IgnoredScopes[i])
	}
	for i := range m.IgnoredValues {
		m.IgnoredValues[i] = common.NormalizeID(m.IgnoredValues[i])
	}
	return m, nil
}

// Unmapped counts what one or more users carried that no rule places: scopes
// no location names, and values neither mapped to a role nor ignored.
type Unmapped struct {
	Scopes map[string]int
	Values map[string]int
}

func newUnmapped() Unmapped {
	return Unmapped{Scopes: map[string]int{}, Values: map[string]int{}}
}

// Notes renders the counts for the sync log, most frequent first.
func (u Unmapped) Notes() []string {
	var notes []string
	add := func(kind string, counts map[string]int) {
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int {
			if counts[a] != counts[b] {
				return counts[b] - counts[a]
			}
			return strings.Compare(a, b)
		})
		for _, k := range keys {
			notes = append(notes, fmt.Sprintf("%s %q: %d user(s)", kind, k, counts[k]))
		}
	}
	add("unknown scope", u.Scopes)
	add("unmapped value", u.Values)
	return notes
}

// Derive returns the memberships one user's attribute values grant, and
// records in unmapped what it could not place.
//
// Per location the user holds each mapped role, or plain membership when they
// have none there; the all-group gets the union across locations. A relation
// implies membership, so a plain membership tuple is written only where no
// role is.
func (m Mapping) Derive(email string, values []string, unmapped Unmapped) []common.TuplePair {
	held := map[string]map[string]bool{} // group → roles held there
	var order []string

	join := func(group, role string) {
		if held[group] == nil {
			held[group] = map[string]bool{}
			order = append(order, group)
		}
		if role != "" {
			held[group][role] = true
		}
	}

	// Counted per person, not per value: one person often carries several
	// values with the same unknown scope ("employee@x", "member@x").
	unknownScopes, unknownValues := map[string]bool{}, map[string]bool{}
	defer func() {
		for k := range unknownScopes {
			unmapped.Scopes[k]++
		}
		for k := range unknownValues {
			unmapped.Values[k]++
		}
	}()

	for _, raw := range values {
		value, scope, ok := strings.Cut(common.NormalizeID(raw), "@")
		if !ok || scope == "" {
			continue
		}
		loc, known := m.Locations[scope]
		if !known {
			if !slices.Contains(m.IgnoredScopes, scope) {
				unknownScopes[scope] = true
			}
			continue
		}
		role, mapped := m.Roles[value]
		if !mapped && !slices.Contains(m.IgnoredValues, value) {
			unknownValues[value+"@"+scope] = true
		}
		join(loc.Group, role)
		if m.AllGroup != nil {
			join(m.AllGroup.Group, role)
		}
	}

	var out []common.TuplePair
	for _, group := range order {
		roles := held[group]
		if len(roles) == 0 {
			out = append(out, common.TuplePair{GroupID: group, Relation: common.RelationMember, MemberType: "user", MemberID: email})
			continue
		}
		sorted := make([]string, 0, len(roles))
		for r := range roles {
			sorted = append(sorted, r)
		}
		slices.Sort(sorted)
		for _, r := range sorted {
			out = append(out, common.TuplePair{GroupID: group, Relation: r, MemberType: "user", MemberID: email})
		}
	}
	return out
}

// Descriptions returns the description of every group the mapping maintains.
func (m Mapping) Descriptions() map[string]string {
	out := map[string]string{}
	for _, g := range m.Locations {
		if g.Description != "" {
			out[g.Group] = g.Description
		}
	}
	if m.AllGroup != nil && m.AllGroup.Description != "" {
		out[m.AllGroup.Group] = m.AllGroup.Description
	}
	return out
}

// Groups returns every group the mapping fills.
func (m Mapping) Groups() []string {
	var out []string
	for _, g := range m.Locations {
		out = append(out, g.Group)
	}
	if m.AllGroup != nil {
		out = append(out, m.AllGroup.Group)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
