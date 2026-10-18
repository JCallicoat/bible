package bible

import (
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// ParagraphTable holds paragraph breaks, one row per chapter: starts is a
// space-separated list of the verse numbers that begin a paragraph. It is
// generated from SourceTranslation by "bible embed-paragraphs" and applied
// to every translation, matching books by name (case-insensitively, since
// e.g. "Song Of Solomon" and "Song of Solomon" both occur) rather than by
// book_id, which differs between translations.
const ParagraphTable = "paragraphs"

// SourceTranslation is the translation paragraphs are computed from.
const SourceTranslation = "kjv"

// ParagraphSchema creates ParagraphTable.
const ParagraphSchema = "CREATE TABLE " + ParagraphTable + " (" +
	"book TEXT NOT NULL COLLATE NOCASE, " +
	"chapter INTEGER NOT NULL, " +
	"starts TEXT NOT NULL, " +
	"PRIMARY KEY (book, chapter)) WITHOUT ROWID"

type chapterKey struct {
	book    string // lowercased
	chapter int
}

func (s *Store) loadHasParagraphs() (bool, error) {
	tables, err := s.tablesWithColumns("book", "chapter", "starts")
	return slices.Contains(tables, ParagraphTable), err
}

// HasParagraphs reports whether the database has paragraph data.
func (s *Store) HasParagraphs() bool { return s.hasParagraphs }

// paragraphStarts returns the paragraph-start verses of a chapter, sorted,
// or nil if the chapter has no paragraph data.
func (s *Store) paragraphStarts(book string, chapter int) ([]int, error) {
	key := chapterKey{strings.ToLower(book), chapter}
	s.mu.Lock()
	starts, ok := s.paragraphs[key]
	s.mu.Unlock()
	if ok {
		return starts, nil
	}

	var raw string
	err := s.db.QueryRow("SELECT starts FROM "+ParagraphTable+" WHERE book = ? AND chapter = ?",
		book, chapter).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	for _, f := range strings.Fields(raw) {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, err
		}
		starts = append(starts, n)
	}
	slices.Sort(starts)
	s.mu.Lock()
	s.paragraphs[key] = starts
	s.mu.Unlock()
	return starts, nil
}

// MarkParagraphs sets Cont on each verse that continues the paragraph of
// the verse before it. vs must be in the order returned by Lookup or
// Chapter. A verse starts a new paragraph if it is the first of its
// translation/chapter in vs, or if a paragraph start lies after the
// previous verse and at or before it (so a verse a translation omits can
// still carry a break). Chapters without paragraph data, and every verse
// when the database has no paragraph table, are left one verse per
// paragraph.
func (s *Store) MarkParagraphs(vs []Verse) error {
	if !s.hasParagraphs {
		return nil
	}
	var starts []int
	for i := range vs {
		v := &vs[i]
		v.Cont = false
		if i == 0 || vs[i-1].Translation != v.Translation || vs[i-1].Book != v.Book ||
			vs[i-1].Chapter != v.Chapter {
			var err error
			if starts, err = s.paragraphStarts(v.Book, v.Chapter); err != nil {
				return err
			}
			continue
		}
		if starts == nil {
			continue
		}
		// Find the first start after the previous verse.
		j, _ := slices.BinarySearch(starts, vs[i-1].Verse+1)
		v.Cont = j == len(starts) || starts[j] > v.Verse
	}
	return nil
}
