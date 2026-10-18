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

// toRows converts verses to rows. With a highlighter (see
// bible.Highlighter), each row carries the segments of its text instead of
// the text itself.
func toRows(vs []bible.Verse, hl func(string) []bible.Segment) []Row {
	rows := make([]Row, len(vs))
	for i, v := range vs {
		rows[i] = Row{Translation: v.Translation, Book: v.Book, Chapter: v.Chapter,
			Verse: v.Verse, Cont: v.Cont}
		if hl != nil {
			rows[i].Segments = hl(v.Text)
		} else {
			rows[i].Text = v.Text
		}
	}
	return rows
}

// paragraphs groups vs into paragraphs unless the viewer was started
// with -L.
func (s *server) paragraphs(vs []bible.Verse) error {
	if s.lines {
		return nil
	}
	return s.store.MarkParagraphs(vs)
}

// table validates the "t" query parameter against the translation list,
// replying with an error if it isn't one.
func (s *server) table(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := r.URL.Query().Get("t")
	if !s.store.HasTranslation(t) {
		http.Error(w, fmt.Sprintf("unknown translation %q", t), http.StatusBadRequest)
		return "", false
	}
	return t, true
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
	writeJSON(w, Result{Rows: toRows(vs, nil)})
}

// handleLookup looks up a reference typed in the lookup box.
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
	q, msg := s.store.ParseLookup([]string{current}, raw)
	if msg != "" {
		return Result{Message: msg}, nil
	}

	// For a single translation, resolve the book up front so the dropdowns
	// can follow even if the verse range has no matches. t:all leaves the
	// dropdowns alone.
	var sync *Sync
	if !q.All {
		book, msg, err := s.store.ResolveBook(q.Tables[0], q.Book)
		if err != nil {
			return Result{}, err
		}
		if msg != "" {
			return Result{Message: msg}, nil
		}
		sync = &Sync{Translation: q.Tables[0], Book: book, Chapter: q.Ref.Chapter}
		q.Book = book
	}

	vs, msg, err := s.store.Lookup(q)
	if err == nil {
		err = s.paragraphs(vs)
	}
	if err != nil {
		return Result{}, err
	}
	if msg != "" {
		return Result{Message: msg, Sync: sync}, nil
	}
	res := Result{Rows: toRows(vs, nil), Multi: q.All, Sync: sync}
	if len(vs) == 0 {
		ref := q.Ref
		ref.Book = q.Book
		res.Message = "No verses found for " + ref.String() + "."
	}
	return res, nil
}

// handleSearch runs a search typed in the search box.
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
	q, msg := s.store.ParseSearch([]string{current}, raw)
	if msg != "" {
		return Result{Message: msg}, nil
	}
	re, err := bible.SearchPattern(q.Term, fixed, ignoreCase)
	if err != nil {
		return Result{Message: "Error: " + err.Error() + "."}, nil
	}

	vs, msg, err := s.store.Search(q, re)
	if err != nil {
		return Result{}, err
	}
	if msg != "" {
		return Result{Message: msg}, nil
	}
	if len(vs) == 0 {
		desc := "“" + q.Term + "”"
		if q.Book != "" {
			desc += " in " + q.Book
		}
		return Result{Message: "No results for " + desc + "."}, nil
	}
	return Result{Rows: toRows(vs, bible.Highlighter(re)), Multi: q.All}, nil
}
