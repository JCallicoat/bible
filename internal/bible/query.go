package bible

import (
	"fmt"
	"strings"
)

// Verse is one row of output.
type Verse struct {
	Translation string `json:"translation"`
	Book        string `json:"book"`
	Chapter     int    `json:"chapter"`
	Verse       int    `json:"verse"`
	Text        string `json:"text"`
}

// Chapter returns every verse of book chapter in table.
func (s *Store) Chapter(table, book string, chapter int) ([]Verse, error) {
	return s.verses(table, book, Reference{Book: book, Chapter: chapter})
}

func (s *Store) verses(table, book string, ref Reference) ([]Verse, error) {
	q := "SELECT verse, text FROM " + QuoteIdent(table) +
		" WHERE book = ? AND chapter = ?"
	args := []any{book, ref.Chapter}
	if ref.HasVerses {
		q += " AND verse >= ? AND verse <= ?"
		args = append(args, ref.Start, ref.End)
	}
	q += " ORDER BY verse"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verse
	for rows.Next() {
		v := Verse{Translation: table, Book: book, Chapter: ref.Chapter}
		if err := rows.Scan(&v.Verse, &v.Text); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Lookup looks up ref in one or more translation tables. bookInput
// overrides ref.Book when non-empty (a "b:" filter).
//
// Book resolution happens independently per table, so a translation that
// lacks a book (e.g. a New-Testament-only one for an Old Testament
// reference) contributes nothing instead of failing the whole lookup. With
// one table, its resolution failure is returned as a message; across
// several, a message is returned only if none of them resolve the book.
func (s *Store) Lookup(tables []string, bookInput string, ref Reference) ([]Verse, []string, error) {
	if bookInput == "" {
		bookInput = ref.Book
	}
	var out []Verse
	var msgs []string
	resolvedAny := false
	for _, t := range tables {
		book, msg, err := s.ResolveBook(t, bookInput)
		if err != nil {
			return nil, nil, err
		}
		if msg != "" {
			if len(tables) == 1 {
				msgs = append(msgs, msg)
			}
			continue
		}
		resolvedAny = true
		rows, err := s.verses(t, book, ref)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rows...)
	}
	if len(tables) > 1 && !resolvedAny {
		msgs = append(msgs, fmt.Sprintf("No book matches “%s” in any translation.", bookInput))
	}
	return out, msgs, nil
}

// Search finds verses containing term (ASCII case-insensitive, as SQLite's
// LIKE) in one or more tables, optionally limited to a book, which is
// resolved per table the same way as in Lookup.
func (s *Store) Search(tables []string, term, bookInput string) ([]Verse, []string, error) {
	var out []Verse
	var msgs []string
	resolvedAny := false
	for _, t := range tables {
		book := ""
		if bookInput != "" {
			b, msg, err := s.ResolveBook(t, bookInput)
			if err != nil {
				return nil, nil, err
			}
			if msg != "" {
				if len(tables) == 1 {
					msgs = append(msgs, msg)
				}
				continue
			}
			book = b
		}
		resolvedAny = true

		q := "SELECT book, chapter, verse, text FROM " + QuoteIdent(t) +
			` WHERE text LIKE ? ESCAPE '\'`
		args := []any{"%" + escapeLike(term) + "%"}
		if book != "" {
			q += " AND book = ?"
			args = append(args, book)
		}
		q += " ORDER BY book_id, chapter, verse"

		rows, err := s.db.Query(q, args...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			v := Verse{Translation: t}
			if err := rows.Scan(&v.Book, &v.Chapter, &v.Verse, &v.Text); err != nil {
				rows.Close()
				return nil, nil, err
			}
			out = append(out, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	if bookInput != "" && len(tables) > 1 && !resolvedAny {
		msgs = append(msgs, fmt.Sprintf("No book matches “%s” in any translation.", bookInput))
	}
	return out, msgs, nil
}

// escapeLike makes %, _ and \ match literally in a LIKE ... ESCAPE '\'
// pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// TablesFor applies a "t:" filter on top of the base translation selection.
// It returns the tables to use, or a message if the filter names an
// unknown translation.
func (s *Store) TablesFor(base []string, f Filters) ([]string, string) {
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
