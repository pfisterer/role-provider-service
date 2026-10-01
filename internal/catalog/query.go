package catalog

import (
	"regexp"
	"strings"
)

// A query is what a person types into a group search box, read the way search
// boxes usually are:
//
//	mannheim karlsruhe         either term (OR)
//	"fak technik"              this phrase, required
//	stud*  *-ma                wildcards, matched against a whole field or word
//	mannheim UND studierende   both terms (also AND, &, or +studierende)
//
// Terms are compared after folding — lowercase, umlauts spelled out — so
// "beschäftigte" finds "beschaeftigte". A "group:" prefix is dropped, so a
// pasted token finds its group.
type query struct {
	required []term
	optional []term
}

type term struct {
	text string         // folded, for a plain substring match
	glob *regexp.Regexp // set when the term has a wildcard
}

// andWords join the terms on either side into required ones.
var andWords = map[string]bool{"und": true, "and": true, "&": true}

func parseQuery(raw string) query {
	var words []struct {
		text   string
		phrase bool
	}
	rest := raw
	for {
		before, quoted, found := cutQuoted(rest)
		for _, w := range strings.Fields(before) {
			words = append(words, struct {
				text   string
				phrase bool
			}{w, false})
		}
		if !found {
			break
		}
		if q := strings.TrimSpace(quoted.text); q != "" {
			words = append(words, struct {
				text   string
				phrase bool
			}{q, true})
		}
		rest = quoted.rest
	}

	var q query
	required := make([]bool, len(words))
	for i, w := range words {
		if !w.phrase && andWords[strings.ToLower(w.text)] {
			if i > 0 {
				required[i-1] = true
			}
			if i+1 < len(words) {
				required[i+1] = true
			}
		}
	}
	for i, w := range words {
		text := w.text
		if !w.phrase && andWords[strings.ToLower(text)] {
			continue
		}
		req := required[i] || w.phrase
		if !w.phrase && strings.HasPrefix(text, "+") {
			text, req = text[1:], true
		}
		t, ok := newTerm(text)
		if !ok {
			continue
		}
		if req {
			q.required = append(q.required, t)
		} else {
			q.optional = append(q.optional, t)
		}
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
func (q query) empty() bool { return len(q.required) == 0 && len(q.optional) == 0 }

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
// every required term must match, and if there are optional terms at least
// one of them. More matching terms rank higher.
func (q query) score(fields []string) int {
	n := 0
	for _, t := range q.required {
		if !t.matches(fields) {
			return -1
		}
		n++
	}
	optional := 0
	for _, t := range q.optional {
		if t.matches(fields) {
			optional++
		}
	}
	if len(q.optional) > 0 && optional == 0 {
		return -1
	}
	return n + optional
}

// exact reports whether the query is exactly this id — the case of a token
// pasted or checked for existence, which must come first whatever else
// matches.
func (q query) exact(id string) bool {
	all := append(append([]term{}, q.required...), q.optional...)
	return len(all) == 1 && all[0].glob == nil && all[0].text == id
}

var folder = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")

// fold is the spelling terms and fields are compared in.
func fold(s string) string {
	return folder.Replace(strings.ToLower(s))
}
