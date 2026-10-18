package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jcallicoat/bible/internal/bible"
)

// Row is a verse as sent to the browser. Search results carry Segments
// (the text split around matches) instead of Text. Cont marks a verse that
// continues the previous row's paragraph.
type Row struct {
	Translation string          `json:"translation"`
	Book        string          `json:"book"`
	Chapter     int             `json:"chapter"`
	Verse       int             `json:"verse"`
	Text        string          `json:"text,omitempty"`
	Segments    []bible.Segment `json:"segments,omitempty"`
	Cont        bool            `json:"cont,omitempty"`
}

// Sync tells the browser which translation/book/chapter the dropdowns
// should point at after a lookup.
type Sync struct {
	Translation string `json:"translation"`
	Book        string `json:"book"`
	Chapter     int    `json:"chapter"`
}

// Result is the response for chapter, lookup and search requests. When
// Message is set, it is shown as plain text instead of rows.
type Result struct {
	Rows    []Row  `json:"rows"`
	Multi   bool   `json:"multi"`
	Message string `json:"message,omitempty"`
	Sync    *Sync  `json:"sync,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func toRows(vs []bible.Verse) []Row {
	rows := make([]Row, len(vs))
	for i, v := range vs {
		rows[i] = Row{Translation: v.Translation, Book: v.Book, Chapter: v.Chapter,
			Verse: v.Verse, Text: v.Text, Cont: v.Cont}
	}
	return rows
}

// table validates the "t" query parameter against the translation list.
// paragraphs groups vs into paragraphs unless the viewer was started
// with -L.
func (s *server) paragraphs(vs []bible.Verse) error {
	if s.lines {
		return nil
	}
	return s.store.MarkParagraphs(vs)
}

func (s *server) table(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := r.URL.Query().Get("t")
	for _, name := range s.store.Translations() {
		if name == t {
			return t, true
		}
	}
	http.Error(w, fmt.Sprintf("unknown translation %q", t), http.StatusBadRequest)
	return "", false
}

func (s *server) handleTranslations(w http.ResponseWriter, r *http.Request) {
	type item struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	items := []item{}
	for _, t := range s.store.Translations() {
		items = append(items, item{t, strings.ToUpper(t)})
	}
	writeJSON(w, map[string]any{"translations": items, "default": s.store.DefaultTranslation()})
}

func (s *server) handleBooks(w http.ResponseWriter, r *http.Request) {
	t, ok := s.table(w, r)
	if !ok {
		return
	}
	books, err := s.store.Books(t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if books == nil {
		books = []bible.Book{}
	}
	writeJSON(w, map[string]any{"books": books})
}

func (s *server) handleChapter(w http.ResponseWriter, r *http.Request) {
	t, ok := s.table(w, r)
	if !ok {
		return
	}
	c, err := strconv.Atoi(r.URL.Query().Get("c"))
	if err != nil {
		http.Error(w, "bad chapter", http.StatusBadRequest)
		return
	}
	vs, err := s.store.Chapter(t, r.URL.Query().Get("b"), c)
	if err == nil {
		err = s.paragraphs(vs)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, Result{Rows: toRows(vs)})
}

// handleLookup mirrors the Qt GUI's reference lookup.
func (s *server) handleLookup(w http.ResponseWriter, r *http.Request) {
	current, ok := s.table(w, r)
	if !ok {
		return
	}
	res, err := s.lookup(current, r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}

func (s *server) lookup(current, raw string) (Result, error) {
	raw = bible.NormalizeDashes(strings.TrimSpace(raw))
	f := bible.ExtractFilters(raw)

	ref, ok := bible.ParseReference(f.Remainder)
	if !ok {
		return Result{Message: fmt.Sprintf("Could not parse reference “%s”.", raw)}, nil
	}
	bookInput := f.Book
	if bookInput == "" {
		bookInput = ref.Book
	}

	tables, msg := s.store.TablesFor([]string{current}, f)
	if msg != "" {
		return Result{Message: msg}, nil
	}

	// For a single translation, resolve the book up front so the dropdowns
	// can follow even if the verse range has no matches. t:all leaves the
	// dropdowns alone.
	var sync *Sync
	if !f.TranslationAll {
		book, msg, err := s.store.ResolveBook(tables[0], bookInput)
		if err != nil {
			return Result{}, err
		}
		if msg != "" {
			return Result{Message: msg}, nil
		}
		sync = &Sync{Translation: tables[0], Book: book, Chapter: ref.Chapter}
		bookInput = book
	}

	vs, msgs, err := s.store.Lookup(tables, bookInput, ref)
	if err == nil {
		err = s.paragraphs(vs)
	}
	if err != nil {
		return Result{}, err
	}
	res := Result{Rows: toRows(vs), Multi: f.TranslationAll, Sync: sync}
	if len(vs) == 0 {
		if len(msgs) > 0 {
			res.Message = msgs[0]
		} else {
			ref.Book = bookInput
			res.Message = "No verses found for " + ref.String()
		}
	}
	return res, nil
}

// handleSearch mirrors the Qt GUI's search.
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	current, ok := s.table(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	res, err := s.search(current, q.Get("q"), q.Get("fixed") == "1", q.Get("icase") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, res)
}

// search runs a regular expression search, or a literal one if fixed is
// set, ignoring case if ignoreCase is set (the CLI's -f and -i).
func (s *server) search(current, raw string, fixed, ignoreCase bool) (Result, error) {
	f := bible.ExtractFilters(strings.TrimSpace(raw))
	term := f.Remainder
	if term == "" {
		return Result{Message: "Enter search text in addition to the filter(s)."}, nil
	}
	re, err := bible.SearchPattern(term, fixed, ignoreCase)
	if err != nil {
		return Result{Message: err.Error() + "."}, nil
	}
	tables, msg := s.store.TablesFor([]string{current}, f)
	if msg != "" {
		return Result{Message: msg}, nil
	}

	vs, msgs, err := s.store.Search(tables, re, f.Book)
	if err != nil {
		return Result{}, err
	}
	if len(vs) == 0 {
		if len(msgs) > 0 {
			return Result{Message: msgs[0]}, nil
		}
		desc := "“" + term + "”"
		if f.Book != "" {
			desc += " in " + f.Book
		}
		return Result{Message: "No results for " + desc + "."}, nil
	}

	hl := bible.Highlighter(re)
	rows := make([]Row, len(vs))
	for i, v := range vs {
		rows[i] = Row{Translation: v.Translation, Book: v.Book, Chapter: v.Chapter,
			Verse: v.Verse, Segments: hl(v.Text)}
	}
	return Result{Rows: rows, Multi: f.TranslationAll}, nil
}
