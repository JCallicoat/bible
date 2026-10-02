package bible

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Filters holds the "b:<book>" and "t:<translation>" tokens pulled out of
// a lookup or search string.
type Filters struct {
	Book           string
	Translation    string
	TranslationAll bool
	// Remainder is the input with every filter token removed and
	// whitespace collapsed: the lookup/search text that is left over.
	Remainder string
}

// ExtractFilters pulls "b:<book>" and "t:<translation>" tokens out of raw.
// A token only counts as a filter when it starts at the beginning of the
// input or after whitespace, so a colon inside an ordinary word is left
// alone. "t:all" (any case) sets TranslationAll. When a key repeats, the
// last one wins.
func ExtractFilters(raw string) Filters {
	var f Filters
	var rest []string
	for _, tok := range strings.Fields(raw) {
		if len(tok) > 2 && tok[1] == ':' {
			switch tok[0] {
			case 'b', 'B':
				f.Book = tok[2:]
				continue
			case 't', 'T':
				if strings.ToLower(tok[2:]) == "all" {
					f.TranslationAll = true
				} else {
					f.Translation = tok[2:]
				}
				continue
			}
		}
		rest = append(rest, tok)
	}
	f.Remainder = strings.Join(rest, " ")
	return f
}

// Reference is a parsed "book [chapter[:start[-end]]]" string.
type Reference struct {
	Book    string
	Chapter int
	// HasVerses is false when no verse was given, meaning the whole
	// chapter. With a single verse, Start == End.
	HasVerses  bool
	Start, End int
}

// "book" is a lazy free-form match so it can contain spaces or digits (e.g.
// "1cor", "song of solomon"); the trailing chapter/verse part is only taken
// if it looks like whitespace followed by digits.
var referenceRE = regexp.MustCompile(
	`^\s*(.+?)(?:\s+(\d+)(?::(\d+)(?:-(\d+))?)?)?\s*$`)

// ParseReference parses "book [chapter[:start_verse[-end_verse]]]". A
// missing chapter defaults to 1; a missing verse means the whole chapter.
// It returns false if ref doesn't have that shape.
func ParseReference(ref string) (Reference, bool) {
	m := referenceRE.FindStringSubmatch(ref)
	if m == nil || m[1] == "" {
		return Reference{}, false
	}
	r := Reference{Book: strings.TrimSpace(m[1]), Chapter: 1}
	var err error
	if m[2] != "" {
		if r.Chapter, err = strconv.Atoi(m[2]); err != nil {
			return Reference{}, false
		}
	}
	if m[3] != "" {
		r.HasVerses = true
		if r.Start, err = strconv.Atoi(m[3]); err != nil {
			return Reference{}, false
		}
		r.End = r.Start
		if m[4] != "" {
			if r.End, err = strconv.Atoi(m[4]); err != nil {
				return Reference{}, false
			}
		}
	}
	return r, true
}

// String formats the reference as "Book 1", "Book 1:2" or "Book 1:2-3".
func (r Reference) String() string {
	s := r.Book + " " + strconv.Itoa(r.Chapter)
	if !r.HasVerses {
		return s
	}
	s += ":" + strconv.Itoa(r.Start)
	if r.End != r.Start {
		s += "-" + strconv.Itoa(r.End)
	}
	return s
}

// NormalizeDashes replaces en and em dashes with hyphens so pasted verse
// ranges like "13:4–7" parse.
func NormalizeDashes(s string) string {
	return strings.NewReplacer("–", "-", "—", "-").Replace(s)
}

// Segment is a run of verse text, marked if it matched the search term.
type Segment struct {
	Text      string `json:"text"`
	Highlight bool   `json:"hl,omitempty"`
}

// Highlighter returns a function that splits text into segments, marking
// case-insensitive occurrences of term.
func Highlighter(term string) func(text string) []Segment {
	if term == "" {
		return func(text string) []Segment { return []Segment{{Text: text}} }
	}
	re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(term))
	return func(text string) []Segment {
		var segs []Segment
		last := 0
		for _, loc := range re.FindAllStringIndex(text, -1) {
			if loc[0] > last {
				segs = append(segs, Segment{Text: text[last:loc[0]]})
			}
			segs = append(segs, Segment{Text: text[loc[0]:loc[1]], Highlight: true})
			last = loc[1]
		}
		if last < len(text) || len(segs) == 0 {
			segs = append(segs, Segment{Text: text[last:]})
		}
		return segs
	}
}

// titleCase mimics Python's str.title(): a cased character is uppercased
// when it follows an uncased one and lowercased otherwise, so "1cor"
// becomes "1Cor".
func titleCase(s string) string {
	var b strings.Builder
	prevCased := false
	for _, r := range s {
		cased := unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r)
		if cased {
			if prevCased {
				r = unicode.ToLower(r)
			} else {
				r = unicode.ToTitle(r)
			}
		}
		prevCased = cased
		b.WriteRune(r)
	}
	return b.String()
}
