package sync

import (
	"strings"
	"testing"
)

const courseLDIF = `dn: cn=wwi23seb,ou=groups,dc=example
objectClass: groupOfNames
description: Kurs WWI23SEB
member: mail=dora@example,ou=people,dc=example

dn: cn=wwi23seb-dozent,ou=groups,dc=example
objectClass: groupOfNames
description: Dozierende WWI23SEB
member: mail=anna@example,ou=people,dc=example
`

func TestParseLDIF_GroupRelationRegexp(t *testing.T) {
	tuples, descriptions, err := ParseLDIF(strings.NewReader(courseLDIF), "mail=([^,]+)", `^(?P<group>.+)-(?P<relation>dozent)$`)
	if err != nil {
		t.Fatalf("ParseLDIF: %v", err)
	}
	got := map[string]string{}
	for _, tp := range tuples {
		got[tp.MemberID] = tp.GroupID + "#" + tp.Relation
	}
	if got["anna@example"] != "wwi23seb#dozent" || got["dora@example"] != "wwi23seb#member" {
		t.Errorf("tuples = %v", got)
	}
	// The relation group's description must not replace the course's.
	if descriptions["wwi23seb"] != "Kurs WWI23SEB" {
		t.Errorf("description = %q", descriptions["wwi23seb"])
	}
}

func TestParseLDIF_WithoutMappingEverythingIsMembership(t *testing.T) {
	tuples, _, err := ParseLDIF(strings.NewReader(courseLDIF), "mail=([^,]+)", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range tuples {
		if tp.Relation != "member" {
			t.Errorf("%s: relation %q without a mapping", tp.GroupID, tp.Relation)
		}
	}
}

func TestParseLDIF_MappingNeedsNamedCaptures(t *testing.T) {
	if _, _, err := ParseLDIF(strings.NewReader(courseLDIF), "mail=([^,]+)", `^(.+)-(dozent)$`); err == nil {
		t.Fatal("a mapping without named captures must be refused")
	}
}
