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
