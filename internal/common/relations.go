package common

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// relationName keeps relation names token-safe: they end up after "#" in a
// token and, sanitized, in Keystone group names downstream.
var relationName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Relations is the set of relations a group can carry. "member" is always in it.
type Relations struct {
	allowed map[string]struct{}
}

// NewRelations builds the set from the configured names, adding "member".
func NewRelations(names []string) (Relations, error) {
	r := Relations{allowed: map[string]struct{}{RelationMember: {}}}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !relationName.MatchString(n) {
			return Relations{}, fmt.Errorf("invalid relation name %q: use lowercase letters, digits, '-' and '_', starting with a letter", n)
		}
		r.allowed[n] = struct{}{}
	}
	return r, nil
}

// Check returns the normalized relation, or an error naming the allowed ones.
func (r Relations) Check(relation string) (string, error) {
	relation = NormalizeRelation(strings.TrimSpace(relation))
	if _, ok := r.allowed[relation]; !ok {
		return "", fmt.Errorf("unknown relation %q (allowed: %s)", relation, strings.Join(r.List(), ", "))
	}
	return relation, nil
}

// List returns the relations in a stable order, "member" first.
func (r Relations) List() []string {
	out := make([]string, 0, len(r.allowed))
	for n := range r.allowed {
		if n != RelationMember {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return append([]string{RelationMember}, out...)
}
