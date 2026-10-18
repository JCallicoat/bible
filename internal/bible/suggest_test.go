package bible

import (
	"reflect"
	"testing"
)

func TestTitleCase(t *testing.T) {
	tests := map[string]string{
		"1cor":            "1Cor",
		"song of SOLOMON": "Song Of Solomon",
		"élan":            "Élan",
		"":                "",
	}
	for in, want := range tests {
		if got := titleCase(in); got != want {
			t.Errorf("titleCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestBooks(t *testing.T) {
	// Expected values from difflib.get_close_matches(input.title(),
	// CanonicalBooks), which SuggestBooks matches.
	tests := []struct {
		in   string
		want []string
	}{
		{"jon", []string{"John", "Jonah", "Job"}},
		{"genisis", []string{"Genesis"}},
		{"revelations", []string{"Revelation", "Lamentations", "Galatians"}},
		{"zzzzzz", []string{}},
	}
	for _, tt := range tests {
		if got := SuggestBooks(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SuggestBooks(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCloseMatches(t *testing.T) {
	// At most n results, best first, ties going to the later name.
	got := closeMatches("ab", []string{"ab", "ax", "ay", "zz"}, 2, 0.5)
	if want := []string{"ab", "ay"}; !reflect.DeepEqual(got, want) {
		t.Errorf("closeMatches = %q, want %q", got, want)
	}
}

func TestRatio(t *testing.T) {
	// Values from difflib.SequenceMatcher(None, a, b).ratio().
	tests := []struct {
		a, b string
		want float64
	}{
		{"", "", 1},
		{"abc", "abc", 1},
		{"abc", "xyz", 0},
		{"abcd", "bcde", 0.75},
		{"Genisis", "Genesis", 2 * 6 / 14.0},
	}
	for _, tt := range tests {
		if got := newMatcher([]rune(tt.b)).ratio([]rune(tt.a)); got != tt.want {
			t.Errorf("ratio(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
