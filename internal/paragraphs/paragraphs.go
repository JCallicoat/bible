// Package paragraphs implements the hidden "bible embed-paragraphs"
// command, which splits the chapters of bible.SourceTranslation into
// paragraphs using sentence embeddings from an OpenAI-compatible
// /v1/embeddings endpoint (llama.cpp's llama-server, for example) and
// stores the result in bible.ParagraphTable.
//
// Breaks are chosen TextTiling-style: every verse is embedded, the
// similarity across each gap between verses is the cosine similarity of
// the mean embeddings of the window verses on either side, and a gap's
// depth is how far that similarity dips below the nearest peaks on both
// sides. The deepest gaps across the whole Bible become paragraph breaks,
// as many as give the requested average paragraph length, subject to a
// minimum and maximum paragraph length.
package paragraphs

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/spf13/pflag"

	"github.com/jcallicoat/bible/internal/bible"
)

type options struct {
	url, model, key string
	batch, parallel int
	window          int
	avg             float64
	min, max        int
	cache           string
}

// Main runs "bible embed-paragraphs" with args (after the command name)
// and returns the exit code.
func Main(args []string, stderr io.Writer) int {
	fset := pflag.NewFlagSet("bible embed-paragraphs", pflag.ContinueOnError)
	fset.SetOutput(stderr)
	var o options
	dbPath := fset.String("db", "", "Database file to add the paragraphs table to (required)")
	fset.StringVar(&o.url, "embed-url", "http://127.0.0.1:8080",
		"Embeddings endpoint: a server base URL or the full .../v1/embeddings URL")
	fset.StringVar(&o.model, "embed-model", "", "Model name to send with each request")
	fset.StringVar(&o.key, "embed-api-key", "", "API key, sent as a bearer token")
	fset.IntVar(&o.batch, "batch", 32, "Verses per request")
	fset.IntVar(&o.parallel, "parallel", 4, "Concurrent requests")
	fset.IntVar(&o.window, "window", 3, "Verses on each side of a gap to compare")
	fset.Float64Var(&o.avg, "avg-verses", 6, "Target average paragraph length, in verses")
	fset.IntVar(&o.min, "min-verses", 2, "Minimum paragraph length, in verses")
	fset.IntVar(&o.max, "max-verses", 20, "Maximum paragraph length, in verses (0 for no limit)")
	fset.StringVar(&o.cache, "embed-cache", "",
		"Save embeddings to this file, or load them from it if it exists, so the\n"+
			"break settings can be tuned without embedding again")
	fset.Usage = func() {
		fmt.Fprint(stderr, "Usage: bible embed-paragraphs --db PATH [options]\n\n"+
			"Splits "+strings.ToUpper(bible.SourceTranslation)+" chapters into paragraphs using an embeddings\n"+
			"endpoint and writes them to the "+bible.ParagraphTable+" table, replacing any\n"+
			"existing one, then vacuums the database.\n\nOptions:\n")
		fset.PrintDefaults()
	}
	if err := fset.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *dbPath == "" || fset.NArg() > 0 || o.batch < 1 || o.parallel < 1 || o.window < 1 ||
		o.min < 1 || o.avg < float64(o.min) || (o.max > 0 && o.max < 2*o.min) {
		fset.Usage()
		return 2
	}
	if err := run(*dbPath, o, stderr); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

type chapter struct {
	book   string
	num    int
	verses []int
	first  int // index of the chapter's first verse in the flat list
	starts []int
}

func run(path string, o options, stderr io.Writer) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	u := url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return err
	}
	defer db.Close()

	chapters, texts, err := readSource(db)
	if err != nil {
		return err
	}
	vecs, err := embeddings(o, texts, stderr)
	if err != nil {
		return err
	}

	total := 0
	for i := range chapters {
		c := &chapters[i]
		c.starts = []int{0}
		total += len(c.verses)
	}
	split(chapters, vecs, o)
	paras := 0
	for _, c := range chapters {
		paras += len(c.starts)
	}
	fmt.Fprintf(stderr, "%d verses in %d chapters split into %d paragraphs (%.1f verses each)\n",
		total, len(chapters), paras, float64(total)/float64(paras))

	if err := write(db, chapters); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "Vacuuming")
	_, err = db.Exec("VACUUM")
	return err
}

func readSource(db *sql.DB) ([]chapter, []string, error) {
	rows, err := db.Query("SELECT book, chapter, verse, text FROM " +
		bible.QuoteIdent(bible.SourceTranslation) + " ORDER BY book_id, chapter, verse")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var chapters []chapter
	var texts []string
	for rows.Next() {
		var book, text string
		var num, verse int
		if err := rows.Scan(&book, &num, &verse, &text); err != nil {
			return nil, nil, err
		}
		if n := len(chapters); n == 0 || chapters[n-1].book != book || chapters[n-1].num != num {
			chapters = append(chapters, chapter{book: book, num: num, first: len(texts)})
		}
		c := &chapters[len(chapters)-1]
		c.verses = append(c.verses, verse)
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(texts) == 0 {
		return nil, nil, fmt.Errorf("%s table is empty", bible.SourceTranslation)
	}
	return chapters, texts, nil
}

func write(db *sql.DB, chapters []chapter) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DROP TABLE IF EXISTS " + bible.ParagraphTable); err != nil {
		return err
	}
	if _, err := tx.Exec(bible.ParagraphSchema); err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO " + bible.ParagraphTable + " (book, chapter, starts) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chapters {
		nums := make([]string, len(c.starts))
		for i, s := range c.starts {
			nums[i] = strconv.Itoa(c.verses[s])
		}
		if _, err := stmt.Exec(c.book, c.num, strings.Join(nums, " ")); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------- embeddings ----------

type cacheFile struct {
	Texts int
	Vecs  [][]float32
}

// embeddings returns a unit-length embedding for each text, from the
// cache file if there is one for the same number of texts.
func embeddings(o options, texts []string, stderr io.Writer) ([][]float32, error) {
	if o.cache != "" {
		f, err := os.Open(o.cache)
		if err == nil {
			defer f.Close()
			var c cacheFile
			if err := gob.NewDecoder(f).Decode(&c); err != nil {
				return nil, fmt.Errorf("reading %s: %w", o.cache, err)
			}
			if c.Texts != len(texts) {
				return nil, fmt.Errorf("%s holds %d embeddings, need %d; delete it to embed again",
					o.cache, c.Texts, len(texts))
			}
			fmt.Fprintf(stderr, "Loaded %d embeddings from %s\n", c.Texts, o.cache)
			return c.Vecs, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	vecs, err := embedAll(o, texts, stderr)
	if err != nil {
		return nil, err
	}
	if o.cache != "" {
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(cacheFile{len(texts), vecs}); err != nil {
			return nil, err
		}
		if err := os.WriteFile(o.cache, buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
	}
	return vecs, nil
}

func endpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("bad --embed-url %q", raw)
	}
	p := strings.TrimSuffix(u.Path, "/")
	switch {
	case strings.HasSuffix(p, "/embeddings"):
	case strings.HasSuffix(p, "/v1"):
		p += "/embeddings"
	default:
		p += "/v1/embeddings"
	}
	u.Path = p
	return u.String(), nil
}

func embedAll(o options, texts []string, stderr io.Writer) ([][]float32, error) {
	ep, err := endpoint(o.url)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vecs := make([][]float32, len(texts))
	batches := make(chan int)
	var (
		mu       sync.Mutex
		firstErr error
		done     int
		wg       sync.WaitGroup
	)
	client := &http.Client{Timeout: 5 * time.Minute}
	for range o.parallel {
		wg.Go(func() {
			for start := range batches {
				end := min(start+o.batch, len(texts))
				out, err := embedBatch(ctx, client, ep, o, texts[start:end])
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						cancel()
					}
				} else {
					copy(vecs[start:end], out)
					done += end - start
					fmt.Fprintf(stderr, "\rEmbedded %d/%d verses", done, len(texts))
				}
				mu.Unlock()
			}
		})
	}
feed:
	for start := 0; start < len(texts); start += o.batch {
		select {
		case batches <- start:
		case <-ctx.Done():
			break feed
		}
	}
	close(batches)
	wg.Wait()
	fmt.Fprintln(stderr)
	if firstErr != nil {
		return nil, firstErr
	}
	return vecs, nil
}

// embedBatch embeds texts with one request, retrying transient failures.
func embedBatch(ctx context.Context, client *http.Client, ep string, o options, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model":           o.model,
		"input":           texts,
		"encoding_format": "float",
	})
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := range 4 {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		vecs, retry, err := post(ctx, client, ep, o.key, body, len(texts))
		if err == nil {
			return vecs, nil
		}
		if !retry {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func post(ctx context.Context, client *http.Client, ep, key string, body []byte, n int) ([][]float32, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retry, fmt.Errorf("%s: %s: %s", ep, resp.Status, bytes.TrimSpace(msg))
	}
	var r struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, true, fmt.Errorf("%s: decoding response: %w", ep, err)
	}
	if len(r.Data) != n {
		return nil, false, fmt.Errorf("%s: got %d embeddings for %d inputs", ep, len(r.Data), n)
	}
	vecs := make([][]float32, n)
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= n || vecs[d.Index] != nil || len(d.Embedding) == 0 {
			return nil, false, fmt.Errorf("%s: malformed embeddings in response", ep)
		}
		vecs[d.Index] = normalize(d.Embedding)
	}
	return vecs, false, nil
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// ---------- splitting ----------

// depths returns the depth score of each gap between consecutive vectors
// (gap g lies between vecs[g] and vecs[g+1]).
func depths(vecs [][]float32, window int) []float64 {
	n := len(vecs)
	if n < 2 {
		return nil
	}
	sims := make([]float64, n-1)
	for g := range sims {
		left := sum(vecs[max(0, g+1-window) : g+1])
		right := sum(vecs[g+1 : min(n, g+1+window)])
		sims[g] = cosine(left, right)
	}
	d := make([]float64, len(sims))
	for g, s := range sims {
		lp, rp := s, s
		for j := g - 1; j >= 0 && sims[j] >= lp; j-- {
			lp = sims[j]
		}
		for j := g + 1; j < len(sims) && sims[j] >= rp; j++ {
			rp = sims[j]
		}
		d[g] = (lp - s) + (rp - s)
	}
	return d
}

// sum adds vectors; for cosine similarity it is as good as the mean.
func sum(vs [][]float32) []float64 {
	m := make([]float64, len(vs[0]))
	for _, v := range vs {
		for i, x := range v {
			m[i] += float64(x)
		}
	}
	return m
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

type gap struct {
	ch    int // index into chapters
	at    int // index within the chapter of the verse that would start a paragraph
	depth float64
}

// split fills in each chapter's starts (indexes into its verses, always
// beginning with 0).
func split(chapters []chapter, vecs [][]float32, o options) {
	total := 0
	var cands []gap
	allDepths := make([][]float64, len(chapters))
	for ci, c := range chapters {
		total += len(c.verses)
		d := depths(vecs[c.first:c.first+len(c.verses)], o.window)
		allDepths[ci] = d
		for g, v := range d {
			if v > 0 {
				cands = append(cands, gap{ci, g + 1, v})
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b gap) int { return cmp.Compare(b.depth, a.depth) })

	want := int(math.Round(float64(total)/o.avg)) - len(chapters)
	for _, g := range cands {
		if want <= 0 {
			break
		}
		if tryBreak(&chapters[g.ch], g.at, o.min) {
			want--
		}
	}

	if o.max <= 0 {
		return
	}
	// Split overlong paragraphs at their deepest allowed gap.
	for ci := range chapters {
		c := &chapters[ci]
		d := allDepths[ci]
		for i := 0; i < len(c.starts); i++ {
			end := len(c.verses)
			if i+1 < len(c.starts) {
				end = c.starts[i+1]
			}
			start := c.starts[i]
			if end-start <= o.max {
				continue
			}
			best := -1
			for at := start + o.min; at <= end-o.min; at++ {
				if best < 0 || d[at-1] > d[best-1] {
					best = at
				}
			}
			c.starts = slices.Insert(c.starts, i+1, best)
			i-- // recheck the shortened paragraph
		}
	}
}

// tryBreak adds a paragraph start at verse index at if that leaves every
// paragraph at least minLen verses long.
func tryBreak(c *chapter, at, minLen int) bool {
	i, _ := slices.BinarySearch(c.starts, at)
	prev := c.starts[i-1] // starts[0] is 0 and at > 0
	next := len(c.verses)
	if i < len(c.starts) {
		next = c.starts[i]
	}
	if at-prev < minLen || next-at < minLen {
		return false
	}
	c.starts = slices.Insert(c.starts, i, at)
	return true
}
