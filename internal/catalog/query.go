package catalog

import (
	"regexp"
	"strings"
)

// A query is what a person types into a group search box, read the way search
// boxes usually are:
//
//	data science mannheim      every word (AND)
//	mannheim ODER karlsruhe    either word (also OR, |)
//	"fak technik"              this phrase
//	stud*  *-ma                wildcards, matched against a whole field or word
//
// UND, AND, & and a leading + are accepted and change nothing, since words are
// required anyway. Terms are compared after folding — lowercase, umlauts
// spelled out — so "beschäftigte" finds "beschaeftigte". A "group:" prefix is
// dropped, so a pasted token finds its group.
type query struct {
	// clauses must all match; the terms within one are alternatives.
	clauses [][]term
}

type term struct {
	text string         // folded, for a plain substring match
	glob *regexp.Regexp // set when the term has a wildcard
}

var (
	andWords = map[string]bool{"und": true, "and": true, "&": true}
	orWords  = map[string]bool{"oder": true, "or": true, "|": true}
)

func parseQuery(raw string) query {
	type word struct {
		text   string
		phrase bool
	}
	var words []word
	rest := raw
	for {
		before, quoted, found := cutQuoted(rest)
		for _, w := range strings.Fields(before) {
			words = append(words, word{w, false})
		}
		if !found {
			break
		}
		if q := strings.TrimSpace(quoted.text); q != "" {
			words = append(words, word{q, true})
		}
		rest = quoted.rest
	}

	var q query
	or := false
	for _, w := range words {
		if !w.phrase && andWords[strings.ToLower(w.text)] {
			continue
		}
		if !w.phrase && orWords[strings.ToLower(w.text)] {
			or = len(q.clauses) > 0
			continue
		}
		text := w.text
		if !w.phrase {
			text = strings.TrimPrefix(text, "+")
		}
		t, ok := newTerm(text)
		if !ok {
			continue
		}
		if or {
			last := len(q.clauses) - 1
			q.clauses[last] = append(q.clauses[last], t)
		} else {
			q.clauses = append(q.clauses, []term{t})
		}
		or = false
	}
	return q
}

type quotedPart struct{ text, rest string }

// cutQuoted splits off the first "..." phrase. An unclosed quote quotes the
// rest, which is what a person still typing it means.
func cutQuoted(s string) (string, quotedPart, bool) {
	before, after, found := strings.Cut(s, `"`)
	if !found {
		return s, quotedPart{}, false
	}
	inner, rest, _ := strings.Cut(after, `"`)
	return before, quotedPart{text: inner, rest: rest}, true
}

func newTerm(raw string) (term, bool) {
	text := fold(strings.TrimPrefix(strings.TrimSpace(raw), "group:"))
	if strings.Trim(text, "*") == "" {
		return term{}, false
	}
	if !strings.Contains(text, "*") {
		return term{text: text}, true
	}
	parts := strings.Split(text, "*")
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return term{text: text, glob: regexp.MustCompile("^" + strings.Join(parts, ".*") + "$")}, true
}

// empty reports whether the query asks for nothing in particular.
func (q query) empty() bool { return len(q.clauses) == 0 }

// matches reports whether an entry's fields satisfy the term: a plain term is
// a substring of any field, a wildcard term matches a whole field or one of
// its words.
func (t term) matches(fields []string) bool {
	for _, f := range fields {
		if t.glob == nil {
			if strings.Contains(f, t.text) {
				return true
			}
			continue
		}
		if t.glob.MatchString(f) {
			return true
		}
		for _, w := range strings.FieldsFunc(f, func(r rune) bool { return r == ' ' || r == '(' || r == ')' || r == ',' }) {
			if t.glob.MatchString(w) {
				return true
			}
		}
	}
	return false
}

// score returns how well the fields answer the query, or -1 for no match:
// every clause must match through one of its terms. Each matching term counts,
// so an entry answering more of the alternatives ranks higher.
func (q query) score(fields []string) int {
	n := 0
	for _, clause := range q.clauses {
		matched := 0
		for _, t := range clause {
			if t.matches(fields) {
				matched++
			}
		}
		if matched == 0 {
			return -1
		}
		n += matched
	}
	return n
}

// exact reports whether the query is exactly this id — the case of a token
// pasted or checked for existence, which must come first whatever else
// matches.
func (q query) exact(id string) bool {
	return len(q.clauses) == 1 && len(q.clauses[0]) == 1 && q.clauses[0][0].glob == nil && q.clauses[0][0].text == id
}

var folder = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")

// fold is the spelling terms and fields are compared in.
func fold(s string) string {
	return folder.Replace(strings.ToLower(s))
}
