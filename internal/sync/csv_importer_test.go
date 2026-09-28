package sync

import (
	"strings"
	"testing"
)

func TestParseCSV_Descriptions(t *testing.T) {
	const in = `group,member,description
studierende-dhbw-ma,*@student.dhbw-mannheim.de,Studierende DHBW Mannheim
studiendekan-wi,dennis.pfisterer@dhbw.de,Studiendekan Wirtschaftsinformatik
studiendekan-wi,clemens.martin@dhbw.de,
leiter-zwr,someone@dhbw.de
`

	tuples, descriptions, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if len(tuples) != 4 {
		t.Errorf("expected 4 tuples, got %d", len(tuples))
	}

	if got := descriptions["studierende-dhbw-ma"]; got != "Studierende DHBW Mannheim" {
		t.Errorf("studierende-dhbw-ma description = %q", got)
	}
	// An empty description on a later row must not wipe the one already seen.
	if got := descriptions["studiendekan-wi"]; got != "Studiendekan Wirtschaftsinformatik" {
		t.Errorf("studiendekan-wi description = %q", got)
	}
	// A row without a third column simply has no description.
	if got, ok := descriptions["leiter-zwr"]; ok {
		t.Errorf("leiter-zwr should have no description, got %q", got)
	}
	// The header row is not mistaken for data.
	if got, ok := descriptions["group"]; ok {
		t.Errorf("header row leaked into descriptions: %q", got)
	}
}

func TestParseCSV_RelationColumn(t *testing.T) {
	const in = `group,member,description,relation
wwi23seb,anna@dhbw.de,Kurs WWI23SEB,dozent
wwi23seb,ben@dhbw.de,,
wwi23seb,group:wwi23seb-kurs,,studierende
`
	tuples, _, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	want := []string{"dozent", "member", "studierende"}
	if len(tuples) != len(want) {
		t.Fatalf("expected %d tuples, got %d", len(want), len(tuples))
	}
	for i, rel := range want {
		if tuples[i].Relation != rel {
			t.Errorf("row %d relation = %q, want %q", i, tuples[i].Relation, rel)
		}
	}
	if tuples[2].MemberType != "group" || tuples[2].MemberID != "wwi23seb-kurs" {
		t.Errorf("row 2 member = %s/%s", tuples[2].MemberType, tuples[2].MemberID)
	}
}

// Columns are found by name once the header starts with the group column.
func TestParseCSV_HeaderOrder(t *testing.T) {
	const in = "relation,member,group\ndozent,anna@dhbw.de,wwi23seb\n"
	// First cell is not "group", so this has no recognised header: the file is
	// read positionally and "dozent" becomes a group — which is why the header
	// must start with the group column.
	tuples, _, err := ParseCSV(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(tuples) != 2 || tuples[0].GroupID != "relation" {
		t.Fatalf("unexpected tuples %+v", tuples)
	}

	const named = "group,relation,member\nwwi23seb,dozent,anna@dhbw.de\n"
	tuples, _, err = ParseCSV(strings.NewReader(named))
	if err != nil {
		t.Fatal(err)
	}
	if len(tuples) != 1 || tuples[0].Relation != "dozent" || tuples[0].MemberID != "anna@dhbw.de" {
		t.Fatalf("unexpected tuples %+v", tuples)
	}
}

func TestParseCSV_UnknownHeaderColumn(t *testing.T) {
	if _, _, err := ParseCSV(strings.NewReader("group,member,relaton\nx,a@b.de,dozent\n")); err == nil {
		t.Fatal("a misspelt column must be refused, not imported as plain membership")
	}
}
