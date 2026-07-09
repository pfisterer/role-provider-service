package common

import "testing"

func TestMatchEmailPattern(t *testing.T) {
	cases := []struct {
		pattern, email string
		want           bool
	}{
		{"*@student.dhbw-mannheim.de", "max.mustermann@student.dhbw-mannheim.de", true},
		{"*@student.dhbw-mannheim.de", "Max.Mustermann@Student.DHBW-Mannheim.DE", true}, // case-insensitive
		{"*@student.dhbw-mannheim.de", "clemens.martin@dhbw.de", false},
		{"*@student.dhbw-mannheim.de", "eve@evil-student.dhbw-mannheim.de.attacker.com", false}, // anchored
		{"*@student.dhbw-mannheim.de", "someone@dhbw-mannheim.de", false},                       // subdomain matters
		{"admin@*", "admin@anything.example", true},
		{"*", "whoever@wherever", true},
		{"*@dhbw.de", "", false},
		{"", "x@dhbw.de", false},
	}
	for _, c := range cases {
		if got := MatchEmailPattern(c.pattern, c.email); got != c.want {
			t.Errorf("MatchEmailPattern(%q, %q) = %v, want %v", c.pattern, c.email, got, c.want)
		}
	}
}
