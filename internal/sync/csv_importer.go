package sync

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/pfisterer/role-provider-service/internal/common"
)

// ParseCSV reads a CSV of group memberships and returns TuplePairs plus the
// group descriptions found in the file.
//
// Without a header the columns are group, member and an optional description.
// A header row (first cell "group" or "group_id") is skipped and names the
// columns instead, which is how the optional "relation" column is recognised:
//
//	group,member,description,relation
//	wwi23seb,anna@dhbw.de,Kurs WWI23SEB,dozent
//
// A row without a relation is a plain membership, so files written before
// relations existed import unchanged. The description is per group: the last
// non-empty value for a group wins.
func ParseCSV(r io.Reader) ([]common.TuplePair, map[string]string, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.Comment = '#'
	reader.FieldsPerRecord = -1 // allow variable columns

	var tuples []common.TuplePair
	descriptions := map[string]string{}
	cols := csvColumns{group: 0, member: 1, description: 2, relation: -1}
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

		// Excel's "CSV UTF-8" starts the file with a byte-order mark, which
		// would otherwise stick to the first cell and hide the header.
		if lineNum == 1 && len(record) > 0 {
			record[0] = strings.TrimPrefix(record[0], "\ufeff")
		}

		if lineNum == 1 && isHeader(record) {
			cols, err = headerColumns(record)
			if err != nil {
				return nil, nil, err
			}
			continue
		}

		group := strings.TrimSpace(cell(record, cols.group))
		member := strings.TrimSpace(cell(record, cols.member))
		if group == "" || member == "" {
			continue
		}

		// Strip "group:" prefix if present in the group column.
		group = strings.TrimPrefix(group, common.GroupPrefix)

		if desc := strings.TrimSpace(cell(record, cols.description)); desc != "" {
			descriptions[group] = desc
		}

		// Determine member type.
		memberType, memberID := resolveMember(member)
		tuples = append(tuples, common.TuplePair{
			GroupID:    group,
			Relation:   common.NormalizeRelation(strings.TrimSpace(cell(record, cols.relation))),
			MemberType: memberType,
			MemberID:   memberID,
		})
	}
	return tuples, descriptions, nil
}

// csvColumns holds the index of each known column; -1 means absent.
type csvColumns struct{ group, member, description, relation int }

func isHeader(record []string) bool {
	if len(record) == 0 {
		return false
	}
	first := strings.TrimSpace(record[0])
	return strings.EqualFold(first, "group") || strings.EqualFold(first, "group_id")
}

// headerColumns maps the header names to indexes. Unknown names are an error:
// a misspelt "relaton" column would otherwise import everyone as a plain member.
func headerColumns(record []string) (csvColumns, error) {
	cols := csvColumns{group: -1, member: -1, description: -1, relation: -1}
	for i, name := range record {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "group", "group_id":
			cols.group = i
		case "member", "user", "user_email":
			cols.member = i
		case "description":
			cols.description = i
		case "relation":
			cols.relation = i
		case "name", "note", "":
			// For the human reading the file; the import ignores it.
		default:
			return cols, fmt.Errorf("csv header: unknown column %q (known: group, member, description, relation, name/note)", name)
		}
	}
	if cols.group < 0 || cols.member < 0 {
		return cols, fmt.Errorf("csv header must name a group and a member column")
	}
	return cols, nil
}

// cell returns record[i], or "" when the column is absent or the row is short.
func cell(record []string, i int) string {
	if i < 0 || i >= len(record) {
		return ""
	}
	return record[i]
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
