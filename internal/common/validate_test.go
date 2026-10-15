package common

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateGroupID(t *testing.T) {
	valid := []string{"wwi23seb", "root-admin", "root_uni", "standort.ma", "0", strings.Repeat("a", 128)}
	invalid := []string{"", "root-admin,kurs-x", "wwi23seb#dozent", "group:x", "a b", " a", "Upper", "-lead", ".lead", "_lead", "kurs\tx", "grüppe", "ｒoot", strings.Repeat("a", 129)}
	for _, id := range valid {
		if err := ValidateGroupID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	for _, id := range invalid {
		if err := ValidateGroupID(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%q: accepted", id)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	valid := []string{"anna@dhbw.de", "first.last+tag@student.dhbw-mannheim.de", "o'brien@x.example", "a@example"}
	invalid := []string{"", "anna", "@dhbw.de", "anna@", "Anna@dhbw.de", "a b@dhbw.de", "a,b@dhbw.de", "\"a\"@dhbw.de", "a..b@dhbw.de", ".a@dhbw.de", "anna@dhbw..de", "anna@-dhbw.de", "anna@[127.0.0.1]", "ännа@dhbw.de", "anna@dhbw.de,root@x", "anna@dhbw.de ", strings.Repeat("a", 250) + "@x.de"}
	for _, e := range valid {
		if err := ValidateEmail(e); err != nil {
			t.Errorf("%q: %v", e, err)
		}
	}
	for _, e := range invalid {
		if err := ValidateEmail(e); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%q: accepted", e)
		}
	}
}

// A pattern rule is a literal address or every address of one literal domain — nothing that can reach further.
func TestValidatePattern(t *testing.T) {
	valid := []string{"*@student.dhbw-mannheim.de", "*@dhbw.de", "anna@dhbw.de"}
	invalid := []string{"*", "*@*", "*dhbw.de", "*@*.dhbw.de", "a*@dhbw.de", "*@", "*@dhbw.de,x", "**@dhbw.de", "@dhbw.de", "", "*@DHBW.de"}
	for _, p := range valid {
		if err := ValidatePattern(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range invalid {
		if err := ValidatePattern(p); !errors.Is(err, ErrInvalidID) {
			t.Errorf("%q: accepted", p)
		}
	}
}

func TestValidateMember(t *testing.T) {
	if err := ValidateMember("group", "root-admin,kurs-x"); err == nil {
		t.Error("group member with a comma accepted")
	}
	if err := ValidateMember("user", "anna@dhbw.de"); err != nil {
		t.Error(err)
	}
	if err := ValidateMember("pattern", "*"); err == nil {
		t.Error("catch-all pattern accepted")
	}
	if err := ValidateMember("robot", "x"); err == nil {
		t.Error("unknown member type accepted")
	}
}
