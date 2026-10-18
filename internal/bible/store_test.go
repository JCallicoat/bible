package bible_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jcallicoat/bible/internal/bible"
	"github.com/jcallicoat/bible/internal/testdb"
)

func open(t *testing.T) *bible.Store {
	t.Helper()
	s, err := bible.OpenFile(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// refs formats verses compactly as "translation Book C:V" (with a "+"
// suffix for paragraph continuations).
func refs(vs []bible.Verse) []string {
	out := []string{}
	for _, v := range vs {
		s := fmt.Sprintf("%s %s %d:%d", v.Translation, v.Book, v.Chapter, v.Verse)
		if v.Cont {
			s += "+"
		}
		out = append(out, s)
	}
	return out
}

func TestOpenEmbedded(t *testing.T) {
	// OpenEmbedded serves a database image from memory; use the bytes of
	// the synthetic database.
	data, err := os.ReadFile(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := bible.OpenEmbedded(string(data))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Translations(); !reflect.DeepEqual(got, []string{"kjv", "nrsvue", "ntonly"}) {
		t.Errorf("Translations() = %q", got)
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := bible.OpenFile(t.TempDir() + "/missing.db"); err == nil {
		t.Error("OpenFile of a missing file succeeded")
	}
	empty := testdb.Build(t, "CREATE TABLE notes (id INTEGER, body TEXT);")
	_, err := bible.OpenFile(empty)
	if err == nil || !strings.Contains(err.Error(), "no tables with") {
		t.Errorf("OpenFile of a database without translations: err = %v", err)
	}
}

func TestTranslations(t *testing.T) {
	s := open(t)
	want := []string{"kjv", "nrsvue", "ntonly"}
	got := s.Translations()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Translations() = %q, want %q", got, want)
	}
	got[0] = "changed"
	if s.Translations()[0] != "kjv" {
		t.Error("Translations() returned the store's own slice")
	}

	if s.DefaultTranslation() != "nrsvue" {
		t.Errorf("DefaultTranslation() = %q", s.DefaultTranslation())
	}
	if !s.HasTranslation("kjv") || s.HasTranslation("KJV") || s.HasTranslation("notes") {
		t.Error("HasTranslation is wrong")
	}
	if !s.HasParagraphs() {
		t.Error("HasParagraphs() = false")
	}
}

func TestDefaultTranslationFallback(t *testing.T) {
	path := testdb.Build(t, "CREATE TABLE web (book_id, book, chapter, verse, text);"+
		"CREATE TABLE asv (book_id, book, chapter, verse, text);")
	s, err := bible.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.DefaultTranslation(); got != "asv" {
		t.Errorf("DefaultTranslation() = %q, want the first translation", got)
	}
	if s.HasParagraphs() {
		t.Error("HasParagraphs() = true without a paragraphs table")
	}
}

func TestResolveTranslation(t *testing.T) {
	s := open(t)
	if got, msg := s.ResolveTranslation(" KJV "); got != "kjv" || msg != "" {
		t.Errorf("ResolveTranslation(KJV) = %q, %q", got, msg)
	}
	if got, msg := s.ResolveTranslation("niv"); got != "" || msg != "No translation matches “niv”." {
		t.Errorf("ResolveTranslation(niv) = %q, %q", got, msg)
	}
}

func TestResolveBook(t *testing.T) {
	s := open(t)
	tests := []struct {
		table, input string
		want, msg    string
	}{
		{"nrsvue", "gen", "Genesis", ""},
		{"nrsvue", "GEN", "Genesis", ""},
		{"nrsvue", "1jo", "1 John", ""},
		{"nrsvue", "1 john", "1 John", ""},
		{"nrsvue", "john", "John", ""},
		{"nrsvue", "jude", "Jude", ""},
		{"nrsvue", "jud", "", "“jud” is ambiguous, matches: Judges, Jude."},
		{"nrsvue", "xyz", "", "No book matches “xyz”."},
		{"ntonly", "gen", "", "No book matches “gen”."},
		// An exact name wins over the books it is a prefix of.
		{"kjv", "esther", "Esther", ""},
		{"kjv", "Esther", "Esther", ""},
		{"kjv", "esth", "", "“esth” is ambiguous, matches: Esther, Esther (Greek)."},
		{"kjv", "esther (g", "Esther (Greek)", ""},
	}
	for _, tt := range tests {
		got, msg, err := s.ResolveBook(tt.table, tt.input)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want || msg != tt.msg {
			t.Errorf("ResolveBook(%s, %q) = %q, %q; want %q, %q",
				tt.table, tt.input, got, msg, tt.want, tt.msg)
		}
	}
	if _, _, err := s.ResolveBook("missing", "gen"); err == nil {
		t.Error("ResolveBook on a missing table succeeded")
	}
}

func TestBooks(t *testing.T) {
	s := open(t)
	books, err := s.Books("nrsvue")
	if err != nil {
		t.Fatal(err)
	}
	want := []bible.Book{
		{Name: "Genesis", Chapters: []int{1, 2}},
		{Name: "Judges", Chapters: []int{1}},
		{Name: "Jonah", Chapters: []int{1}},
		{Name: "John", Chapters: []int{3}},
		{Name: "Romans", Chapters: []int{1}},
		{Name: "1 John", Chapters: []int{1}},
		{Name: "Jude", Chapters: []int{1}},
	}
	if !reflect.DeepEqual(books, want) {
		t.Errorf("Books(nrsvue) = %+v, want %+v", books, want)
	}

	// ResolveBook after Books uses the cached book list.
	if got, _, _ := s.ResolveBook("nrsvue", "rom"); got != "Romans" {
		t.Errorf("ResolveBook after Books = %q", got)
	}
}

func TestConcurrentCaches(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	for range 8 {
		for _, table := range s.Translations() {
			wg.Go(func() {
				if _, err := s.Books(table); err != nil {
					t.Error(err)
				}
				if _, _, err := s.ResolveBook(table, "john"); err != nil {
					t.Error(err)
				}
				vs, err := s.Chapter(table, "John", 3)
				if err == nil {
					err = s.MarkParagraphs(vs)
				}
				if err != nil {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
}

func TestChapter(t *testing.T) {
	s := open(t)
	vs, err := s.Chapter("nrsvue", "Genesis", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []bible.Verse{
		{Translation: "nrsvue", Book: "Genesis", Chapter: 2, Verse: 1, Text: "Chapter two begins."},
		{Translation: "nrsvue", Book: "Genesis", Chapter: 2, Verse: 2, Text: "Chapter two continues."},
	}
	if !reflect.DeepEqual(vs, want) {
		t.Errorf("Chapter = %+v, want %+v", vs, want)
	}
}

func TestParseLookup(t *testing.T) {
	s := open(t)
	base := []string{"nrsvue"}
	tests := []struct {
		raw  string
		want bible.LookupQuery
		msg  string
	}{
		{"john 3:16", bible.LookupQuery{
			Ref:  bible.Reference{Book: "john", Chapter: 3, HasVerses: true, Start: 16, End: 16},
			Book: "john", Tables: base}, ""},
		{" gen 1:1–3 t:KJV ", bible.LookupQuery{
			Ref:  bible.Reference{Book: "gen", Chapter: 1, HasVerses: true, Start: 1, End: 3},
			Book: "gen", Tables: []string{"kjv"}}, ""},
		{"3:16 b:john t:all", bible.LookupQuery{
			Ref:  bible.Reference{Book: "3:16", Chapter: 1},
			Book: "john", Tables: []string{"kjv", "nrsvue", "ntonly"}, All: true}, ""},
		{"t:kjv", bible.LookupQuery{}, "Could not parse reference “t:kjv”."},
		{"john 3 t:niv", bible.LookupQuery{}, "No translation matches “niv”."},
	}
	for _, tt := range tests {
		got, msg := s.ParseLookup(base, tt.raw)
		if !reflect.DeepEqual(got, tt.want) || msg != tt.msg {
			t.Errorf("ParseLookup(%q) = %+v, %q; want %+v, %q", tt.raw, got, msg, tt.want, tt.msg)
		}
	}
}

func TestLookup(t *testing.T) {
	s := open(t)
	all := s.Translations()
	tests := []struct {
		raw  string
		base []string
		want []string
		msg  string
	}{
		{"john 3:16", []string{"nrsvue"}, []string{"nrsvue John 3:16"}, ""},
		{"gen 1:2-4", []string{"nrsvue"},
			[]string{"nrsvue Genesis 1:2", "nrsvue Genesis 1:3", "nrsvue Genesis 1:4"}, ""},
		{"gen 1:4-2", []string{"nrsvue"}, []string{}, ""},
		{"gen 9", []string{"nrsvue"}, []string{}, ""},
		// Translations lacking the book are skipped.
		{"gen 1:1", all, []string{"kjv Genesis 1:1", "nrsvue Genesis 1:1"}, ""},
		// A b: filter overrides the reference's book, and the base
		// translations' order is kept.
		{"x 3:16 b:john", []string{"ntonly", "kjv"}, []string{"ntonly John 3:16", "kjv John 3:16"}, ""},
		{"x 3:16 b:john t:all", nil,
			[]string{"kjv John 3:16", "nrsvue John 3:16", "ntonly John 3:16"}, ""},
		{"gen 1", []string{"ntonly"}, []string{}, "No book matches “gen”."},
		{"jud 1", []string{"nrsvue"}, []string{}, "“jud” is ambiguous, matches: Judges, Jude."},
		{"xyz 1", all, []string{}, "No book matches “xyz” in any translation."},
	}
	for _, tt := range tests {
		q, msg := s.ParseLookup(tt.base, tt.raw)
		if msg != "" {
			t.Fatalf("ParseLookup(%q): %s", tt.raw, msg)
		}
		vs, msg, err := s.Lookup(q)
		if err != nil {
			t.Fatal(err)
		}
		if got := refs(vs); !reflect.DeepEqual(got, tt.want) || msg != tt.msg {
			t.Errorf("Lookup(%q) = %q, %q; want %q, %q", tt.raw, got, msg, tt.want, tt.msg)
		}
	}
}

func TestParseSearch(t *testing.T) {
	s := open(t)
	base := []string{"nrsvue"}
	tests := []struct {
		raw  string
		want bible.SearchQuery
		msg  string
	}{
		{" faith  so ", bible.SearchQuery{Term: "faith so", Tables: base}, ""},
		{"faith b:rom t:all", bible.SearchQuery{Term: "faith", Book: "rom",
			Tables: []string{"kjv", "nrsvue", "ntonly"}, All: true}, ""},
		{"b:rom t:kjv", bible.SearchQuery{}, "Enter search text in addition to the filters."},
		{"faith t:niv", bible.SearchQuery{}, "No translation matches “niv”."},
	}
	for _, tt := range tests {
		got, msg := s.ParseSearch(base, tt.raw)
		if !reflect.DeepEqual(got, tt.want) || msg != tt.msg {
			t.Errorf("ParseSearch(%q) = %+v, %q; want %+v, %q", tt.raw, got, msg, tt.want, tt.msg)
		}
	}
}

func TestSearch(t *testing.T) {
	s := open(t)
	all := s.Translations()
	tests := []struct {
		raw               string
		base              []string
		fixed, ignoreCase bool
		want              []string
		msg               string
	}{
		{"faith", []string{"nrsvue"}, false, false,
			[]string{"nrsvue Genesis 1:1", "nrsvue John 3:16", "nrsvue Romans 1:1"}, ""},
		{"faith", []string{"nrsvue"}, false, true,
			[]string{"nrsvue Genesis 1:1", "nrsvue Genesis 1:4", "nrsvue John 3:16", "nrsvue Romans 1:1"}, ""},
		{"^Faith", []string{"nrsvue"}, false, false, []string{"nrsvue Genesis 1:4"}, ""},
		{"a.b", []string{"nrsvue"}, false, false, []string{"nrsvue John 3:2", "nrsvue John 3:3"}, ""},
		{"a.b", []string{"nrsvue"}, true, false, []string{"nrsvue John 3:2"}, ""},
		{"faith b:john", all, false, false, []string{"kjv John 3:16", "nrsvue John 3:16"}, ""},
		{"Romans b:rom", all, false, true, []string{"nrsvue Romans 1:1", "ntonly Romans 1:1"}, ""},
		{"nothing", all, false, false, []string{}, ""},
		{"faith b:gen", []string{"ntonly"}, false, false, []string{}, "No book matches “gen”."},
		{"faith b:xyz", all, false, false, []string{}, "No book matches “xyz” in any translation."},
	}
	for _, tt := range tests {
		q, msg := s.ParseSearch(tt.base, tt.raw)
		if msg != "" {
			t.Fatalf("ParseSearch(%q): %s", tt.raw, msg)
		}
		re, err := bible.SearchPattern(q.Term, tt.fixed, tt.ignoreCase)
		if err != nil {
			t.Fatal(err)
		}
		vs, msg, err := s.Search(q, re)
		if err != nil {
			t.Fatal(err)
		}
		if got := refs(vs); !reflect.DeepEqual(got, tt.want) || msg != tt.msg {
			t.Errorf("Search(%q, fixed=%v, icase=%v) = %q, %q; want %q, %q",
				tt.raw, tt.fixed, tt.ignoreCase, got, msg, tt.want, tt.msg)
		}
	}
}

func TestMarkParagraphs(t *testing.T) {
	s := open(t)
	q, _ := s.ParseLookup(nil, "b:gen 1 t:all")
	vs, _, err := s.Lookup(q)
	if err != nil {
		t.Fatal(err)
	}
	more, _ := s.Chapter("nrsvue", "Genesis", 2)
	vs = append(vs, more...)
	john, _ := s.Chapter("nrsvue", "John", 3)
	vs = append(vs, john...)
	if err := s.MarkParagraphs(vs); err != nil {
		t.Fatal(err)
	}
	want := []string{
		// Paragraphs start at verses 1 and 3. kjv omits verse 3, so
		// its verse 4 starts the paragraph.
		"kjv Genesis 1:1", "kjv Genesis 1:2+", "kjv Genesis 1:4",
		// A new translation starts a new paragraph.
		"nrsvue Genesis 1:1", "nrsvue Genesis 1:2+", "nrsvue Genesis 1:3",
		"nrsvue Genesis 1:4+", "nrsvue Genesis 1:5+",
		// No paragraph data: one verse per paragraph.
		"nrsvue Genesis 2:1", "nrsvue Genesis 2:2",
		// Starts are stored unsorted and with a different case.
		"nrsvue John 3:1", "nrsvue John 3:2+", "nrsvue John 3:3+", "nrsvue John 3:16",
	}
	if got := refs(vs); !reflect.DeepEqual(got, want) {
		t.Errorf("MarkParagraphs:\n got %q\nwant %q", got, want)
	}

	// Marking again gives the same result (Cont is reset first).
	if err := s.MarkParagraphs(vs); err != nil {
		t.Fatal(err)
	}
	if got := refs(vs); !reflect.DeepEqual(got, want) {
		t.Errorf("MarkParagraphs again:\n got %q\nwant %q", got, want)
	}
}
