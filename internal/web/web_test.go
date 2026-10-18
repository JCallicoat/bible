package web

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jcallicoat/bible/internal/bible"
	"github.com/jcallicoat/bible/internal/testdb"
)

const testAddr = "127.0.0.1:1234"

func newServer(t *testing.T) *server {
	t.Helper()
	store, err := bible.OpenFile(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &server{store: store, addr: testAddr, grace: graceDefault, quit: make(chan struct{})}
}

// get makes a request to s and returns the response status and body.
func get(t *testing.T, s *server, path string, params map[string]string) (int, string) {
	t.Helper()
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+path+"?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// getResult makes an API request that must succeed and decodes its Result.
func getResult(t *testing.T, s *server, path string, params map[string]string) Result {
	t.Helper()
	code, body := get(t, s, path, params)
	if code != http.StatusOK {
		t.Fatalf("%s %v: status %d: %s", path, params, code, body)
	}
	var res Result
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("%s %v: %v", path, params, err)
	}
	return res
}

// refs formats rows compactly as "translation Book C:V" (with a "+" suffix
// for paragraph continuations).
func refs(rows []Row) []string {
	out := []string{}
	for _, r := range rows {
		s := r.Translation + " " + r.Book + " " + strconv.Itoa(r.Chapter) + ":" + strconv.Itoa(r.Verse)
		if r.Cont {
			s += "+"
		}
		out = append(out, s)
	}
	return out
}

func TestCheckHost(t *testing.T) {
	s := newServer(t)
	for host, want := range map[string]int{
		testAddr:         http.StatusOK,
		"localhost:1234": http.StatusOK,
		"[::1]:1234":     http.StatusOK,
		"localhost:9999": http.StatusForbidden,
		"evil.example":   http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/translations", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		s.handler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %s: status %d, want %d", host, rec.Code, want)
		}
	}
}

func TestStatic(t *testing.T) {
	s := newServer(t)
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		if code, _ := get(t, s, path, nil); code != http.StatusOK {
			t.Errorf("GET %s: status %d", path, code)
		}
	}
}

func TestTranslations(t *testing.T) {
	s := newServer(t)
	code, body := get(t, s, "/api/translations", nil)
	want := `{"default":"nrsvue","translations":[{"id":"kjv","label":"KJV"},` +
		`{"id":"nrsvue","label":"NRSVUE"},{"id":"ntonly","label":"NTONLY"}]}` + "\n"
	if code != http.StatusOK || body != want {
		t.Errorf("got %d %s, want %s", code, body, want)
	}
}

func TestBooks(t *testing.T) {
	s := newServer(t)
	code, body := get(t, s, "/api/books", map[string]string{"t": "ntonly"})
	want := `{"books":[{"name":"John","chapters":[3]},{"name":"Romans","chapters":[1]}]}` + "\n"
	if code != http.StatusOK || body != want {
		t.Errorf("got %d %s, want %s", code, body, want)
	}
	for _, table := range []string{"", "KJV", "notes"} {
		if code, _ := get(t, s, "/api/books", map[string]string{"t": table}); code != http.StatusBadRequest {
			t.Errorf("books for %q: status %d, want 400", table, code)
		}
	}
}

func TestChapter(t *testing.T) {
	s := newServer(t)
	params := map[string]string{"t": "kjv", "b": "Genesis", "c": "1"}
	res := getResult(t, s, "/api/chapter", params)
	want := []string{"kjv Genesis 1:1", "kjv Genesis 1:2+", "kjv Genesis 1:4"}
	if got := refs(res.Rows); !reflect.DeepEqual(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
	if res.Rows[0].Text != "KJV beginning with faith." || res.Rows[0].Segments != nil {
		t.Errorf("row 0 = %+v", res.Rows[0])
	}

	s.lines = true
	res = getResult(t, s, "/api/chapter", params)
	want = []string{"kjv Genesis 1:1", "kjv Genesis 1:2", "kjv Genesis 1:4"}
	if got := refs(res.Rows); !reflect.DeepEqual(got, want) {
		t.Errorf("with -L: rows %q, want %q", got, want)
	}

	params["c"] = "x"
	if code, _ := get(t, s, "/api/chapter", params); code != http.StatusBadRequest {
		t.Errorf("bad chapter: status %d", code)
	}
}

func TestLookup(t *testing.T) {
	s := newServer(t)
	tests := []struct {
		t, q  string
		rows  []string
		multi bool
		msg   string
		sync  *Sync
	}{
		{"nrsvue", "jon 1", []string{"nrsvue Jonah 1:1"}, false, "",
			&Sync{"nrsvue", "Jonah", 1}},
		{"nrsvue", " john 3:1–3 ", []string{"nrsvue John 3:1", "nrsvue John 3:2+", "nrsvue John 3:3+"}, false, "",
			&Sync{"nrsvue", "John", 3}},
		// A t: filter switches the dropdowns' translation.
		{"nrsvue", "esther 1 t:KJV", []string{"kjv Esther 1:1"}, false, "",
			&Sync{"kjv", "Esther", 1}},
		// The dropdowns follow even if no verses match.
		{"nrsvue", "gen 9", []string{}, false, "No verses found for Genesis 9.",
			&Sync{"nrsvue", "Genesis", 9}},
		{"nrsvue", "gen 1:7-9", []string{}, false, "No verses found for Genesis 1:7-9.",
			&Sync{"nrsvue", "Genesis", 1}},
		// t:all leaves the dropdowns alone.
		{"kjv", "john 3:16 t:all", []string{"kjv John 3:16", "nrsvue John 3:16", "ntonly John 3:16"}, true, "", nil},
		{"kjv", "xyz 1 t:all", []string{}, false, "No book matches “xyz” in any translation.", nil},
		{"nrsvue", "jud 1", []string{}, false, "“jud” is ambiguous, matches: Judges, Jude.", nil},
		{"ntonly", "gen 1", []string{}, false, "No book matches “gen”.", nil},
		{"nrsvue", "t:kjv", []string{}, false, "Could not parse reference “t:kjv”.", nil},
		{"nrsvue", "john t:niv", []string{}, false, "No translation matches “niv”.", nil},
	}
	for _, tt := range tests {
		res := getResult(t, s, "/api/lookup", map[string]string{"t": tt.t, "q": tt.q})
		if got := refs(res.Rows); !reflect.DeepEqual(got, tt.rows) || res.Multi != tt.multi ||
			res.Message != tt.msg || !reflect.DeepEqual(res.Sync, tt.sync) {
			t.Errorf("lookup %q in %s = %q, multi %v, %q, %+v; want %q, multi %v, %q, %+v",
				tt.q, tt.t, got, res.Multi, res.Message, res.Sync, tt.rows, tt.multi, tt.msg, tt.sync)
		}
	}
}

func TestSearch(t *testing.T) {
	s := newServer(t)
	res := getResult(t, s, "/api/search", map[string]string{"t": "nrsvue", "q": "fa.th b:rom"})
	want := []Row{{Translation: "nrsvue", Book: "Romans", Chapter: 1, Verse: 1, Segments: []bible.Segment{
		{Text: "Romans one one, by "}, {Text: "faith", Highlight: true}, {Text: "."}}}}
	if !reflect.DeepEqual(res.Rows, want) || res.Multi || res.Message != "" {
		t.Errorf("search = %+v, want rows %+v", res, want)
	}

	tests := []struct {
		params map[string]string
		rows   []string
		multi  bool
		msg    string
	}{
		{map[string]string{"q": "a.b"}, []string{"nrsvue John 3:2", "nrsvue John 3:3"}, false, ""},
		{map[string]string{"q": "a.b", "fixed": "1"}, []string{"nrsvue John 3:2"}, false, ""},
		{map[string]string{"q": "^faith", "icase": "1"}, []string{"nrsvue Genesis 1:4"}, false, ""},
		{map[string]string{"q": "faith b:john t:all"}, []string{"kjv John 3:16", "nrsvue John 3:16"}, true, ""},
		{map[string]string{"q": "nothing b:john"}, []string{}, false, "No results for “nothing” in john."},
		{map[string]string{"q": "faith b:xyz t:all"}, []string{}, false, "No book matches “xyz” in any translation."},
		{map[string]string{"q": "b:john"}, []string{}, false, "Enter search text in addition to the filters."},
		{map[string]string{"q": "(x"}, []string{}, false, "Error: invalid regular expression: missing closing ): `(x`."},
		{map[string]string{"q": "faith t:niv"}, []string{}, false, "No translation matches “niv”."},
	}
	for _, tt := range tests {
		tt.params["t"] = "nrsvue"
		res := getResult(t, s, "/api/search", tt.params)
		if got := refs(res.Rows); !reflect.DeepEqual(got, tt.rows) || res.Multi != tt.multi || res.Message != tt.msg {
			t.Errorf("search %v = %q, multi %v, %q; want %q, multi %v, %q",
				tt.params, got, res.Multi, res.Message, tt.rows, tt.multi, tt.msg)
		}
	}
}

func TestQuit(t *testing.T) {
	s := newServer(t)
	post := func(origin string) int {
		req := httptest.NewRequest(http.MethodPost, "http://"+testAddr+"/api/quit", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(""); code != http.StatusForbidden {
		t.Errorf("without Origin: status %d", code)
	}
	if code := post("http://evil.example"); code != http.StatusForbidden {
		t.Errorf("with another Origin: status %d", code)
	}
	if stopped(s) {
		t.Fatal("server stopped by a forbidden request")
	}
	if code := post("http://" + testAddr); code != http.StatusNoContent {
		t.Errorf("with our Origin: status %d", code)
	}
	waitFor(t, "the server to stop", func() bool { return stopped(s) })
}

func stopped(s *server) bool {
	select {
	case <-s.quit:
		return true
	default:
		return false
	}
}

func clients(s *server) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients
}

// waitFor polls cond until it is true, failing the test after 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStartupTimeout(t *testing.T) {
	s := newServer(t)
	s.scheduleShutdown(10 * time.Millisecond)
	waitFor(t, "the server to stop", func() bool { return stopped(s) })
}

// serve starts s on a free port and returns its base URL and a channel
// that receives serve's result.
func serve(t *testing.T, s *server) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	done := make(chan error, 1)
	go func() { done <- s.serve(ln) }()
	t.Cleanup(s.stop)
	return "http://" + s.addr, done
}

// openEvents opens the event stream and waits for its hello event.
func openEvents(t *testing.T, base string) (*http.Response, *bufio.Reader) {
	t.Helper()
	resp, err := http.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	r := bufio.NewReader(resp.Body)
	waitEvent(t, r, "hello")
	return resp, r
}

func waitEvent(t *testing.T, r *bufio.Reader, name string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("waiting for %s event: %v", name, err)
		}
		if strings.TrimSpace(line) == "event: "+name {
			return
		}
	}
}

func waitServe(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve didn't return")
	}
}

func TestLastTabClosed(t *testing.T) {
	s := newServer(t)
	s.grace = 50 * time.Millisecond
	base, done := serve(t, s)

	// A pending shutdown doesn't stop the server while a page is open.
	// (Connect first: the first connection can be slow, so racing it
	// against the timer would be flaky.)
	first, _ := openEvents(t, base)
	second, _ := openEvents(t, base)
	waitFor(t, "two clients", func() bool { return clients(s) == 2 })
	s.scheduleShutdown(10 * time.Millisecond)
	time.Sleep(4 * s.grace)
	if stopped(s) {
		t.Fatal("startup timeout stopped the server with tabs open")
	}

	// Closing one of two tabs doesn't stop it.
	first.Body.Close()
	waitFor(t, "one client", func() bool { return clients(s) == 1 })
	time.Sleep(4 * s.grace)
	if stopped(s) {
		t.Fatal("server stopped with a tab still open")
	}

	// Closing the last one does, after the grace period.
	second.Body.Close()
	waitFor(t, "the server to stop", func() bool { return stopped(s) })
	waitServe(t, done)
}

func TestStopSaysBye(t *testing.T) {
	s := newServer(t)
	base, done := serve(t, s)
	_, r := openEvents(t, base)
	s.stop()
	waitEvent(t, r, "bye")
	waitServe(t, done)
}
