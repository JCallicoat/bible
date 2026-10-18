package bible

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
)

// Verse is one row of output.
type Verse struct {
	Translation string `json:"translation"`
	Book        string `json:"book"`
	Chapter     int    `json:"chapter"`
	Verse       int    `json:"verse"`
	Text        string `json:"text"`
	// Cont is set by MarkParagraphs when this verse continues the
	// previous verse's paragraph.
	Cont bool `json:"cont,omitempty"`
}

// LookupQuery is a parsed lookup string, as returned by ParseLookup.
type LookupQuery struct {
	Ref Reference
	// Book is the book to resolve in each table: the "b:" filter if there
	// was one, otherwise Ref.Book.
	Book   string
	Tables []string // the translations to look in
	All    bool     // a "t:all" filter was given
}

// ParseLookup parses a lookup string such as "1cor 13:4-7 t:all" (see
// ExtractFilters and ParseReference), applying any "t:" filter on top of
// the base translations. It returns an error message if raw can't be
// parsed or names an unknown translation.
func (s *Store) ParseLookup(base []string, raw string) (LookupQuery, string) {
	raw = NormalizeDashes(strings.TrimSpace(raw))
	f := ExtractFilters(raw)
	ref, ok := ParseReference(f.Remainder)
	if !ok {
		return LookupQuery{}, fmt.Sprintf("Could not parse reference “%s”.", raw)
	}
	tables, msg := s.tablesFor(base, f)
	if msg != "" {
		return LookupQuery{}, msg
	}
	q := LookupQuery{Ref: ref, Book: f.Book, Tables: tables, All: f.TranslationAll}
	if q.Book == "" {
		q.Book = ref.Book
	}
	return q, ""
}

// SearchQuery is a parsed search string, as returned by ParseSearch.
type SearchQuery struct {
	Term   string   // the search text with the filters removed
	Book   string   // the "b:" filter, or "" to search every book
	Tables []string // the translations to search
	All    bool     // a "t:all" filter was given
}

// ParseSearch parses a search string such as "faith b:romans t:kjv" (see
// ExtractFilters), applying any "t:" filter on top of the base
// translations. It returns an error message if there is no search text or
// raw names an unknown translation. Compile Term with SearchPattern.
func (s *Store) ParseSearch(base []string, raw string) (SearchQuery, string) {
	f := ExtractFilters(raw)
	if f.Remainder == "" {
		return SearchQuery{}, "Enter search text in addition to the filters."
	}
	tables, msg := s.tablesFor(base, f)
	if msg != "" {
		return SearchQuery{}, msg
	}
	return SearchQuery{Term: f.Remainder, Book: f.Book, Tables: tables, All: f.TranslationAll}, ""
}

// tablesFor applies a "t:" filter on top of the base translation
// selection. It returns the tables to use, or a message if the filter
// names an unknown translation.
func (s *Store) tablesFor(base []string, f Filters) ([]string, string) {
	if f.TranslationAll {
		return s.Translations(), ""
	}
	if f.Translation != "" {
		t, msg := s.ResolveTranslation(f.Translation)
		if msg != "" {
			return nil, msg
		}
		return []string{t}, ""
	}
	return base, ""
}

// Chapter returns every verse of book chapter in table.
func (s *Store) Chapter(table, book string, chapter int) ([]Verse, error) {
	return s.verses(table, book, Reference{Book: book, Chapter: chapter})
}

func (s *Store) verses(table, book string, ref Reference) ([]Verse, error) {
	q := "SELECT book, chapter, verse, text FROM " + QuoteIdent(table) +
		" WHERE book = ? AND chapter = ?"
	args := []any{book, ref.Chapter}
	if ref.HasVerses {
		q += " AND verse >= ? AND verse <= ?"
		args = append(args, ref.Start, ref.End)
	}
	return s.queryVerses(table, q+" ORDER BY verse", args...)
}

// queryVerses runs q, which must select book, chapter, verse and text, and
// returns the rows as verses of table.
func (s *Store) queryVerses(table, q string, args ...any) ([]Verse, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verse
	for rows.Next() {
		v := Verse{Translation: table}
		if err := rows.Scan(&v.Book, &v.Chapter, &v.Verse, &v.Text); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// tableBook is a table and a book resolved in it ("" for every book).
type tableBook struct {
	table, book string
}

// resolveBooks resolves input independently in each table, so a
// translation that lacks a book (e.g. a New-Testament-only one for an Old
// Testament reference) is skipped instead of failing the whole request.
// With one table, its resolution failure is returned as a message; across
// several, a message is returned only if none of them resolve the book.
// An empty input selects every book of every table.
func (s *Store) resolveBooks(tables []string, input string) ([]tableBook, string, error) {
	var out []tableBook
	for _, t := range tables {
		if input == "" {
			out = append(out, tableBook{t, ""})
			continue
		}
		book, msg, err := s.ResolveBook(t, input)
		if err != nil {
			return nil, "", err
		}
		if msg != "" {
			if len(tables) == 1 {
				return nil, msg, nil
			}
			continue
		}
		out = append(out, tableBook{t, book})
	}
	if len(out) == 0 && len(tables) > 1 {
		return nil, fmt.Sprintf("No book matches “%s” in any translation.", input), nil
	}
	return out, "", nil
}

// Lookup looks up q.Ref, resolving q.Book in each of q.Tables (see
// resolveBooks). It returns the verses, or a message if the book could not
// be resolved.
func (s *Store) Lookup(q LookupQuery) ([]Verse, string, error) {
	targets, msg, err := s.resolveBooks(q.Tables, q.Book)
	if err != nil || msg != "" {
		return nil, msg, err
	}
	var out []Verse
	for _, tb := range targets {
		vs, err := s.verses(tb.table, tb.book, q.Ref)
		if err != nil {
			return nil, "", err
		}
		out = append(out, vs...)
	}
	return out, "", nil
}

// SearchPattern compiles a search term into the pattern Search matches.
// The term is a Go regular expression unless fixed is set, in which case
// it matches literally. Matching is case-sensitive unless ignoreCase is set.
func SearchPattern(term string, fixed, ignoreCase bool) (*regexp.Regexp, error) {
	if fixed {
		term = regexp.QuoteMeta(term)
	}
	if ignoreCase {
		term = "(?i)" + term
	}
	re, err := regexp.Compile(term)
	if se := (*syntax.Error)(nil); errors.As(err, &se) {
		return nil, fmt.Errorf("invalid regular expression: %s: `%s`", se.Code, se.Expr)
	}
	return re, err
}

// Search finds verses whose text matches re (see SearchPattern) in each of
// q.Tables, limited to q.Book if set, which is resolved per table the same
// way as in Lookup. It returns the verses, or a message if the book could
// not be resolved.
func (s *Store) Search(q SearchQuery, re *regexp.Regexp) ([]Verse, string, error) {
	targets, msg, err := s.resolveBooks(q.Tables, q.Book)
	if err != nil || msg != "" {
		return nil, msg, err
	}
	var out []Verse
	for _, tb := range targets {
		query := "SELECT book, chapter, verse, text FROM " + QuoteIdent(tb.table) +
			" WHERE text REGEXP ?"
		args := []any{re.String()}
		if tb.book != "" {
			query += " AND book = ?"
			args = append(args, tb.book)
		}
		vs, err := s.queryVerses(tb.table, query+" ORDER BY book_id, chapter, verse", args...)
		if err != nil {
			return nil, "", err
		}
		out = append(out, vs...)
	}
	return out, "", nil
}
