package common

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidID marks an id that does not have the one shape the service accepts for its kind.
var ErrInvalidID = errors.New("invalid id")

// GroupIDPattern is the shape of a group id. It keeps ids token-safe: no ":" (token prefix), "#" (relation separator), "," or whitespace (list separators in configuration and SQL), and nothing outside ASCII. The database enforces the same expression as a CHECK constraint.
const GroupIDPattern = `^[a-z0-9][a-z0-9._-]{0,127}$`

var groupIDRe = regexp.MustCompile(GroupIDPattern)

// emailRe is a plain ASCII addr-spec, lowercase: a dot-atom local part and a domain of LDH labels. No quoted local parts, comments or IP literals.
var emailRe = regexp.MustCompile("^[a-z0-9!#$%&'*+/=?^_`{|}~-]+(\\.[a-z0-9!#$%&'*+/=?^_`{|}~-]+)*@" + domainExpr + "$")

// domainRe is a domain of one or more LDH labels, lowercase.
var domainRe = regexp.MustCompile("^" + domainExpr + "$")

const domainExpr = `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*`

// maxEmailLength is the longest address SMTP can carry (RFC 5321 path limit minus the brackets).
const maxEmailLength = 254

// ValidateGroupID checks a group id that is already normalized (NormalizeID).
func ValidateGroupID(id string) error {
	if !groupIDRe.MatchString(id) {
		return fmt.Errorf("%w: group id %q must match %s", ErrInvalidID, id, GroupIDPattern)
	}
	return nil
}

// reservedNameRe is a group id in which "*" may stand for any run of characters.
var reservedNameRe = regexp.MustCompile(`^[a-z0-9*][a-z0-9._*-]{0,127}$`)

// ValidateReservedName checks a glob a source reserves, already normalized. It must name something: a glob of only "*" and separators would reserve every id there is.
func ValidateReservedName(glob string) error {
	if !reservedNameRe.MatchString(glob) || strings.Trim(glob, "*.-_") == "" {
		return fmt.Errorf("%w: reserved name %q must be a group id, with \"*\" for any run of characters, and name at least one letter or digit", ErrInvalidID, glob)
	}
	return nil
}

// ValidateEmail checks an address that is already normalized (NormalizeID): a plain ASCII addr-spec.
func ValidateEmail(email string) error {
	if len(email) > maxEmailLength || !emailRe.MatchString(email) {
		return fmt.Errorf("%w: %q is not a plain e-mail address", ErrInvalidID, email)
	}
	return nil
}

// ValidatePattern checks a pattern rule that is already normalized (NormalizeID). Only two shapes are accepted: "*@<domain>" with a literal domain, or a literal address. Anything broader ("*", "*@*", "*dhbw.de", "*@*.dhbw.de") would hand a group to people nobody meant to include.
func ValidatePattern(pattern string) error {
	if domain, ok := strings.CutPrefix(pattern, "*@"); ok {
		if domainRe.MatchString(domain) {
			return nil
		}
	} else if !strings.Contains(pattern, "*") && ValidateEmail(pattern) == nil {
		return nil
	}
	return fmt.Errorf("%w: pattern %q must be \"*@<domain>\" or a literal e-mail address", ErrInvalidID, pattern)
}

// ValidateMember checks a member id of the given type ("user", "group" or "pattern"), already normalized. A group may carry a relation, "wwi23seb#studierende" (see MemberGroup); whether the relation is configured is for the caller to check.
func ValidateMember(memberType, id string) error {
	switch memberType {
	case "user":
		return ValidateEmail(id)
	case "group":
		group, relation, hasRelation := strings.Cut(id, RelationSeparator)
		if err := ValidateGroupID(group); err != nil {
			return err
		}
		if hasRelation && !relationName.MatchString(relation) {
			return fmt.Errorf("%w: relation %q of member group %q must be lowercase letters, digits, '-' and '_', starting with a letter", ErrInvalidID, relation, group)
		}
		return nil
	case "pattern":
		return ValidatePattern(id)
	default:
		return fmt.Errorf("%w: unknown member type %q", ErrInvalidID, memberType)
	}
}

// ValidateTuple checks the group and the member of a tuple, both already normalized.
func ValidateTuple(t TuplePair) error {
	if err := ValidateGroupID(t.GroupID); err != nil {
		return err
	}
	return ValidateMember(t.MemberType, t.MemberID)
}
