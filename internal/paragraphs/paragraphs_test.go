package paragraphs

import (
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jcallicoat/bible/internal/bible"
	"github.com/jcallicoat/bible/internal/testdb"
)

func TestEndpoint(t *testing.T) {
	tests := map[string]string{
		"http://127.0.0.1:8080":                   "http://127.0.0.1:8080/v1/embeddings",
		"http://127.0.0.1:8080/":                  "http://127.0.0.1:8080/v1/embeddings",
		"https://api.example.com/v1":              "https://api.example.com/v1/embeddings",
		"https://api.example.com/v1/":             "https://api.example.com/v1/embeddings",
		"https://api.example.com/x/v1/embeddings": "https://api.example.com/x/v1/embeddings",
		"http://h/proxy":                          "http://h/proxy/v1/embeddings",
	}
	for in, want := range tests {
		if got, err := endpoint(in); err != nil || got != want {
			t.Errorf("endpoint(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "127.0.0.1:8080", "ftp://h/", "http://", "http://h/%zz"} {
		if _, err := endpoint(bad); err == nil {
			t.Errorf("endpoint(%q) succeeded", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := normalize([]float32{3, 4}); !reflect.DeepEqual(got, []float32{0.6, 0.8}) {
		t.Errorf("normalize([3 4]) = %v", got)
	}
	if got := normalize([]float32{0, 0}); !reflect.DeepEqual(got, []float32{0, 0}) {
		t.Errorf("normalize([0 0]) = %v", got)
	}
}

func TestCosine(t *testing.T) {
	tests := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{2, 0}, 1},
		{[]float64{1, 0}, []float64{0, 3}, 0},
		{[]float64{1, 0}, []float64{-1, 0}, -1},
		{[]float64{0, 0}, []float64{1, 0}, 0},
	}
	for _, tt := range tests {
		if got := cosine(tt.a, tt.b); math.Abs(got-tt.want) > 1e-12 {
			t.Errorf("cosine(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

var (
	e1 = []float32{1, 0}
	e2 = []float32{0, 1}
)

func TestDepths(t *testing.T) {
	if d := depths([][]float32{e1}, 1); d != nil {
		t.Errorf("depths of one verse = %v", d)
	}
	// The topic changes between verses 3 and 4: that gap is the only dip.
	vecs := [][]float32{e1, e1, e1, e2, e2, e2}
	if got, want := depths(vecs, 1), []float64{0, 0, 2, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Errorf("depths(window 1) = %v, want %v", got, want)
	}
	// A wider window blurs the change across neighboring gaps, but the
	// deepest gap stays the same.
	d := depths(vecs, 3)
	for g := range d {
		if g != 2 && d[g] >= d[2] {
			t.Errorf("depths(window 3) = %v: gap %d is as deep as gap 2", d, g)
		}
	}
}

func TestTryBreak(t *testing.T) {
	c := &chapter{verses: []int{1, 2, 3, 4, 5, 6}, starts: []int{0}}
	steps := []struct {
		at   int
		ok   bool
		want []int
	}{
		{3, true, []int{0, 3}},
		{1, false, []int{0, 3}}, // leaves a 1-verse paragraph before it
		{5, false, []int{0, 3}}, // ... or after it
		{4, false, []int{0, 3}},
		{2, false, []int{0, 3}},
	}
	for _, s := range steps {
		if ok := tryBreak(c, s.at, 2); ok != s.ok || !reflect.DeepEqual(c.starts, s.want) {
			t.Errorf("tryBreak(%d) = %v, starts %v; want %v, %v", s.at, ok, c.starts, s.ok, s.want)
		}
	}
}

// chapters builds chapters of the given lengths over one flat verse list.
func chapters(lens ...int) []chapter {
	var cs []chapter
	first := 0
	for i, n := range lens {
		c := chapter{book: "B", num: i + 1, first: first}
		for v := range n {
			c.verses = append(c.verses, v+1)
		}
		cs = append(cs, c)
		first += n
	}
	return cs
}

func TestSplit(t *testing.T) {
	vecs := [][]float32{e1, e1, e1, e2, e2, e2, e1, e1, e1, e1}

	// 10 verses averaging 3 per paragraph: one break beyond the two
	// chapter starts, at the deepest gap.
	cs := chapters(6, 4)
	split(cs, vecs, options{window: 1, avg: 3, min: 2})
	if cs[0].starts == nil || !reflect.DeepEqual(cs[0].starts, []int{0, 3}) || !reflect.DeepEqual(cs[1].starts, []int{0}) {
		t.Errorf("starts %v, %v; want [0 3], [0]", cs[0].starts, cs[1].starts)
	}

	// Long averages give no breaks beyond the chapter starts.
	cs = chapters(6, 4)
	split(cs, vecs, options{window: 1, avg: 100, min: 2})
	if !reflect.DeepEqual(cs[0].starts, []int{0}) || !reflect.DeepEqual(cs[1].starts, []int{0}) {
		t.Errorf("starts %v, %v; want [0], [0]", cs[0].starts, cs[1].starts)
	}

	// Overlong paragraphs are split until none is longer than max, even
	// without any dips.
	same := make([][]float32, 10)
	for i := range same {
		same[i] = e1
	}
	cs = chapters(10)
	split(cs, same, options{window: 1, avg: 100, min: 2, max: 4})
	if want := []int{0, 2, 4, 6}; !reflect.DeepEqual(cs[0].starts, want) {
		t.Errorf("starts with max 4 = %v, want %v", cs[0].starts, want)
	}
}

// fakeServer serves embeddings: verses mentioning faith point one way and
// all others another. fail, if set, picks a status to fail a request with.
type fakeServer struct {
	*httptest.Server
	requests atomic.Int32
	inputs   atomic.Int32
	fail     func(n int32) int
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.requests.Add(1)
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("request to %s with Authorization %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if f.fail != nil {
			if code := f.fail(n); code != 0 {
				http.Error(w, "failed", code)
				return
			}
		}
		var req struct {
			Model  string   `json:"model"`
			Input  []string `json:"input"`
			Format string   `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "m" || req.Format != "float" {
			t.Errorf("bad request %+v: %v", req, err)
		}
		f.inputs.Add(int32(len(req.Input)))
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var resp struct {
			Data []item `json:"data"`
		}
		// Answer in reverse order: the index field decides.
		for i := len(req.Input) - 1; i >= 0; i-- {
			v := []float32{0, 2}
			if strings.Contains(strings.ToLower(req.Input[i]), "faith") {
				v = []float32{2, 0}
			}
			resp.Data = append(resp.Data, item{i, v})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) options() options {
	return options{url: f.URL, model: "m", key: "key", batch: 2, parallel: 3,
		window: 1, avg: 6, min: 2, max: 20}
}

func TestEmbedAll(t *testing.T) {
	f := newFakeServer(t)
	texts := []string{"faith", "x", "Faith", "y", "z"}
	vecs, err := embedAll(f.options(), texts, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{1, 0}, {0, 1}, {1, 0}, {0, 1}, {0, 1}}
	if !reflect.DeepEqual(vecs, want) {
		t.Errorf("embedAll = %v, want %v (normalized, in input order)", vecs, want)
	}
	if n := f.requests.Load(); n != 3 {
		t.Errorf("%d requests, want 3 batches", n)
	}
}

func TestEmbedRetries(t *testing.T) {
	f := newFakeServer(t)
	// The first request fails transiently and is retried.
	f.fail = func(n int32) int {
		if n == 1 {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	o := f.options()
	o.batch = 10
	if _, err := embedAll(o, []string{"a", "b"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n := f.requests.Load(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestEmbedPermanentError(t *testing.T) {
	f := newFakeServer(t)
	f.fail = func(int32) int { return http.StatusBadRequest }
	_, err := embedAll(f.options(), []string{"a", "b", "c", "d", "e"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "400 Bad Request: failed") {
		t.Errorf("err = %v", err)
	}
	// Workers stop after the first error rather than trying every batch.
	if n := f.requests.Load(); n > 3 {
		t.Errorf("%d requests after a permanent error", n)
	}
}

func TestEmbedMalformed(t *testing.T) {
	tests := map[string]string{
		`{"data":[]}`:                            "got 0 embeddings for 1 inputs",
		`{"data":[{"index":5,"embedding":[1]}]}`: "malformed embeddings",
		`{"data":[{"index":0,"embedding":[]}]}`:  "malformed embeddings",
	}
	for body, want := range tests {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		_, err := embedAll(options{url: srv.URL, batch: 1, parallel: 1}, []string{"a"}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("response %s: err = %v, want %q", body, err, want)
		}
		srv.Close()
	}
}

// starts reads the paragraphs table.
func starts(t *testing.T, path string) map[string]string {
	t.Helper()
	u := url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT book, chapter, starts FROM " + bible.ParagraphTable)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var book, starts string
		var chapter int
		if err := rows.Scan(&book, &chapter, &starts); err != nil {
			t.Fatal(err)
		}
		got[book+" "+strconv.Itoa(chapter)] = starts
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRun(t *testing.T) {
	path := testdb.Path(t)
	f := newFakeServer(t)
	o := f.options()
	o.cache = filepath.Join(t.TempDir(), "cache.gob")
	if err := run(path, o, io.Discard); err != nil {
		t.Fatal(err)
	}
	// The kjv chapters are too short to split with a minimum of 2 verses,
	// so each is one paragraph, replacing the old table's rows.
	want := map[string]string{"Genesis 1": "1", "Esther 1": "1", "Esther (Greek) 1": "1", "John 3": "16"}
	if got := starts(t, path); !reflect.DeepEqual(got, want) {
		t.Errorf("paragraphs = %v, want %v", got, want)
	}
	if n := f.inputs.Load(); n != 6 {
		t.Errorf("embedded %d verses, want 6", n)
	}

	// The result is a valid database for the store.
	s, err := bible.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasParagraphs() {
		t.Error("store doesn't see the new paragraphs")
	}
	s.Close()

	// A second run loads the cache instead of calling the server.
	f.fail = func(int32) int { return http.StatusInternalServerError }
	if err := run(path, o, io.Discard); err != nil {
		t.Fatal(err)
	}

	// A cache for a different number of verses is rejected.
	other := testdb.Build(t, testdb.SQL+"INSERT INTO kjv VALUES (66, 'Revelation', 1, 1, 'More.');")
	if err := run(other, o, io.Discard); err == nil || !strings.Contains(err.Error(), "holds 6 embeddings, need 7") {
		t.Errorf("run with a stale cache: err = %v", err)
	}
}

func TestRunErrors(t *testing.T) {
	o := options{url: "http://127.0.0.1:1", batch: 1, parallel: 1, window: 1, avg: 6, min: 2}
	if err := run(filepath.Join(t.TempDir(), "missing.db"), o, io.Discard); err == nil {
		t.Error("run created a missing database")
	}
	empty := testdb.Build(t, "CREATE TABLE kjv (book_id, book, chapter, verse, text);")
	if err := run(empty, o, io.Discard); err == nil || !strings.Contains(err.Error(), "kjv table is empty") {
		t.Errorf("run on an empty source: err = %v", err)
	}
}

func TestMainFlags(t *testing.T) {
	tests := [][]string{
		{},                                 // --db is required
		{"--db", "x", "extra"},             // no positionals
		{"--db", "x", "--batch", "0"},      // batch >= 1
		{"--db", "x", "--avg-verses", "1"}, // avg >= min
		{"--db", "x", "--max-verses", "3"}, // max >= 2*min
		{"--db", "x", "--bogus"},
	}
	for _, args := range tests {
		var stderr strings.Builder
		if code := Main(args, &stderr); code != 2 {
			t.Errorf("Main(%q) = %d, want 2", args, code)
		}
	}
	var stderr strings.Builder
	if code := Main([]string{"-h"}, &stderr); code != 0 || !strings.HasPrefix(stderr.String(), "Usage: bible embed-paragraphs") {
		t.Errorf("Main(-h) = %d, %q", code, stderr.String())
	}
}
