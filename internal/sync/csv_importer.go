package sync

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/pfisterer/role-provider-service/internal/common"
)

// ParseCSV reads a two-column CSV (group, user_email) and returns TuplePairs
// plus the group descriptions found in the file.
// The header row is detected automatically and skipped.
// An optional third column holds the group description; it is per group, so the
// last non-empty value for a group wins.
func ParseCSV(r io.Reader) ([]common.TuplePair, map[string]string, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.Comment = '#'
	reader.FieldsPerRecord = -1 // allow variable columns

	var tuples []common.TuplePair
	descriptions := map[string]string{}
	lineNum := 0

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("csv parse error: %w", err)
		}
		lineNum++

		if len(record) < 2 {
			continue
		}

		group := strings.TrimSpace(record[0])
		member := strings.TrimSpace(record[1])

		// Skip header row.
		if lineNum == 1 && (strings.EqualFold(group, "group") || strings.EqualFold(group, "group_id")) {
			continue
		}
		if group == "" || member == "" {
			continue
		}

		// Strip "group:" prefix if present in the group column.
		group = strings.TrimPrefix(group, common.GroupPrefix)

		if len(record) > 2 {
			if desc := strings.TrimSpace(record[2]); desc != "" {
				descriptions[group] = desc
			}
		}

		// Determine member type.
		memberType, memberID := resolveMember(member)
		tuples = append(tuples, common.TuplePair{
			GroupID:    group,
			MemberType: memberType,
			MemberID:   memberID,
		})
	}
	return tuples, descriptions, nil
}

// resolveMember returns (type, id) from a member string.
// Accepts "user:email", "group:name", "pattern:<email-glob>", or bare email / group name.
func resolveMember(member string) (typ, id string) {
	if strings.HasPrefix(member, common.UserPrefix) {
		return "user", strings.TrimPrefix(member, common.UserPrefix)
	}
	if strings.HasPrefix(member, common.GroupPrefix) {
		return "group", strings.TrimPrefix(member, common.GroupPrefix)
	}
	if strings.HasPrefix(member, common.PatternPrefix) {
		return "pattern", strings.TrimPrefix(member, common.PatternPrefix)
	}
	// Bare value: treat as email (user) if it contains @, otherwise group.
	if strings.Contains(member, "@") {
		return "user", member
	}
	return "group", member
}
