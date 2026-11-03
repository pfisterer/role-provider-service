package common

import (
	"regexp"
	"strings"
)

// MatchEmailPattern reports whether email matches a glob pattern where "*" means
// "any run of characters" (e.g. "*@student.dhbw-mannheim.de"). Matching is
// case-insensitive and whole-string anchored. All other characters are literal.
//
// Used for pattern-based group membership: a tuple with subjType "pattern" and
// subjID = the glob makes every matching email a (transitive) member of the group.
func MatchEmailPattern(pattern, email string) bool {
	return MatchGlob(pattern, email)
}

// MatchGlob reports whether s matches pattern, where "*" means any run of characters and everything else is literal. Case-insensitive and anchored at both ends. It serves pattern members and the names a source reserves.
func MatchGlob(pattern, s string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	e := strings.ToLower(strings.TrimSpace(s))
	if p == "" || e == "" {
		return false
	}

	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		if r == '*' {
			b.WriteString(".*")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(e)
}
