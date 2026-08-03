package sync

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	gorldif "github.com/go-ldap/ldif"
	"github.com/pfisterer/role-provider-service/internal/common"
)

// ParseLDIF reads an LDIF stream and extracts group→member tuples plus each
// group's "description" attribute.
//
// dnEmailRegexp is applied to each member DN to extract an email address.
// The first capture group is used. Example patterns:
//
//	"mail=([^,]+)"            → extracts "user@dhbw.de" from "mail=user@dhbw.de,ou=..."
//	"uid=([^,@]+@[^,]+)"      → extracts email-shaped uid values
//	"uid=([^,]+)"             → extracts uid (suitable if uids are email addresses)
func ParseLDIF(r io.Reader, dnEmailRegexp string) ([]common.TuplePair, map[string]string, error) {
	re, err := regexp.Compile(dnEmailRegexp)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid dn_email_regexp %q: %w", dnEmailRegexp, err)
	}
	if re.NumSubexp() < 1 {
		return nil, nil, fmt.Errorf("dn_email_regexp %q must contain at least one capture group", dnEmailRegexp)
	}

	l := &gorldif.LDIF{}
	if err := gorldif.Unmarshal(r, l); err != nil {
		return nil, nil, fmt.Errorf("ldif parse error: %w", err)
	}

	var tuples []common.TuplePair
	descriptions := map[string]string{}

	for _, entry := range l.Entries {
		e := entry.Entry
		if e == nil {
			continue
		}
		if !isGroupObjectClass(e.GetAttributeValues("objectClass")) {
			continue
		}

		groupID := extractCN(e.DN)
		if groupID == "" {
			continue
		}
		// Strip accidental "group:" prefix.
		groupID = strings.TrimPrefix(groupID, common.GroupPrefix)

		if desc := strings.TrimSpace(firstValue(e.GetAttributeValues("description"))); desc != "" {
			descriptions[groupID] = desc
		}

		// Collect member DNs from both "member" and "uniqueMember" attributes.
		memberDNs := append(
			e.GetAttributeValues("member"),
			e.GetAttributeValues("uniqueMember")...,
		)
		for _, dn := range memberDNs {
			email := extractViaRegexp(re, dn)
			if email == "" {
				continue
			}
			tuples = append(tuples, common.TuplePair{
				GroupID:    groupID,
				MemberType: "user",
				MemberID:   email,
			})
		}
	}
	return tuples, descriptions, nil
}

// firstValue returns the first entry of vals, or "" when there is none.
func firstValue(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// isGroupObjectClass returns true when any objectClass value is a known group type.
func isGroupObjectClass(classes []string) bool {
	for _, cls := range classes {
		switch strings.ToLower(cls) {
		case "groupofnames", "groupofuniquenames", "posixgroup", "group":
			return true
		}
	}
	return false
}

// extractCN returns the first CN value from an LDAP DN string.
func extractCN(dn string) string {
	re := regexp.MustCompile(`(?i)cn=([^,]+)`)
	m := re.FindStringSubmatch(dn)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// extractViaRegexp applies re to dn and returns the first capture group.
func extractViaRegexp(re *regexp.Regexp, dn string) string {
	m := re.FindStringSubmatch(dn)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}
