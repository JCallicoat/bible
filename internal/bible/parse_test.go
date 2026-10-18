package bible

import (
	"reflect"
	"regexp"
	"testing"
)

func TestExtractFilters(t *testing.T) {
	tests := []struct {
		in   string
		want Filters
	}{
		{"", Filters{}},
		{"john 3:16", Filters{Remainder: "john 3:16"}},
		{"  faith   b:rom  t:KJV ", Filters{Book: "rom", Translation: "KJV", Remainder: "faith"}},
		{"B:gen T:net 1:1", Filters{Book: "gen", Translation: "net", Remainder: "1:1"}},
		{"t:ALL love", Filters{TranslationAll: true, Remainder: "love"}},
		{"t:kjv t:net x", Filters{Translation: "net", Remainder: "x"}},
		// A colon inside a word, a bare "b:" and other keys aren't filters.
		{"ab:c b: x:y", Filters{Remainder: "ab:c b: x:y"}},
	}
	for _, tt := range tests {
		if got := ExtractFilters(tt.in); got != tt.want {
			t.Errorf("ExtractFilters(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseReference(t *testing.T) {
	tests := []struct {
		in   string
		want Reference
		ok   bool
	}{
		{"john", Reference{Book: "john", Chapter: 1}, true},
		{"john 3", Reference{Book: "john", Chapter: 3}, true},
		{"john 3:16", Reference{Book: "john", Chapter: 3, HasVerses: true, Start: 16, End: 16}, true},
		{"1cor 13:4-7", Reference{Book: "1cor", Chapter: 13, HasVerses: true, Start: 4, End: 7}, true},
		{"  song of solomon 2:1 ", Reference{Book: "song of solomon", Chapter: 2, HasVerses: true, Start: 1, End: 1}, true},
		{"1 john 1", Reference{Book: "1 john", Chapter: 1}, true},
		{"", Reference{}, false},
		{"   ", Reference{}, false},
		{"john 99999999999999999999", Reference{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseReference(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseReference(%q) = %+v, %v; want %+v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestReferenceString(t *testing.T) {
	tests := []struct {
		ref  Reference
		want string
	}{
		{Reference{Book: "John", Chapter: 3}, "John 3"},
		{Reference{Book: "John", Chapter: 3, HasVerses: true, Start: 16, End: 16}, "John 3:16"},
		{Reference{Book: "John", Chapter: 3, HasVerses: true, Start: 16, End: 18}, "John 3:16-18"},
	}
	for _, tt := range tests {
		if got := tt.ref.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestNormalizeDashes(t *testing.T) {
	if got := NormalizeDashes("13:4–7 1:2—3"); got != "13:4-7 1:2-3" {
		t.Errorf("NormalizeDashes = %q", got)
	}
}

func TestHighlighter(t *testing.T) {
	tests := []struct {
		re, text string
		want     []Segment
	}{
		{"x", "", []Segment{{Text: ""}}},
		{"x", "abc", []Segment{{Text: "abc"}}},
		{"b", "abc", []Segment{{Text: "a"}, {Text: "b", Highlight: true}, {Text: "c"}}},
		{"a|c", "abc", []Segment{{Text: "a", Highlight: true}, {Text: "b"}, {Text: "c", Highlight: true}}},
		{"b", "bb", []Segment{{Text: "b", Highlight: true}, {Text: "b", Highlight: true}}},
		// Empty matches aren't marked.
		{"x*", "ab", []Segment{{Text: "ab"}}},
	}
	for _, tt := range tests {
		got := Highlighter(regexp.MustCompile(tt.re))(tt.text)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Highlighter(%q)(%q) = %+v, want %+v", tt.re, tt.text, got, tt.want)
		}
	}
}

func TestSearchPattern(t *testing.T) {
	tests := []struct {
		term              string
		fixed, ignoreCase bool
		text              string
		match             bool
	}{
		{"fa.th", false, false, "faith", true},
		{"fa.th", true, false, "faith", false},
		{"a.b", true, false, "a.b", true},
		{"Faith", false, false, "faith", false},
		{"Faith", false, true, "faith", true},
		{"(x", true, true, "A (X", true},
	}
	for _, tt := range tests {
		re, err := SearchPattern(tt.term, tt.fixed, tt.ignoreCase)
		if err != nil {
			t.Fatalf("SearchPattern(%q, %v, %v): %v", tt.term, tt.fixed, tt.ignoreCase, err)
		}
		if got := re.MatchString(tt.text); got != tt.match {
			t.Errorf("SearchPattern(%q, %v, %v) matches %q = %v, want %v",
				tt.term, tt.fixed, tt.ignoreCase, tt.text, got, tt.match)
		}
	}

	_, err := SearchPattern("(x", false, false)
	if want := "invalid regular expression: missing closing ): `(x`"; err == nil || err.Error() != want {
		t.Errorf("SearchPattern(\"(x\") error = %v, want %q", err, want)
	}
}

func TestNormalizeBook(t *testing.T) {
	if got := normalizeBook("1 Song Of SOLOMON"); got != "1songofsolomon" {
		t.Errorf("normalizeBook = %q", got)
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := QuoteIdent(`a"b`); got != `"a""b"` {
		t.Errorf("QuoteIdent = %s", got)
	}
}
