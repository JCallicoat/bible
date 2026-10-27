package cli

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jcallicoat/bible/internal/bible"
	"github.com/jcallicoat/bible/internal/testdb"
)

// run runs Main against the database at path and returns its exit code,
// stdout and stderr.
func run(t *testing.T, path string, args ...string) (int, string, string) {
	t.Helper()
	open := func(p string) (*bible.Store, error) {
		if p != "" {
			t.Errorf("open got --db %q", p)
		}
		return bible.OpenFile(path)
	}
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr, open)
	return code, stdout.String(), stderr.String()
}

func TestMain(t *testing.T) {
	path := testdb.Path(t)
	tests := []struct {
		name         string
		args         []string
		code         int
		stdout       string
		stderrPrefix string
	}{
		{
			name: "default is Genesis 1 in paragraphs",
			stdout: "NRSVUE\n\n" +
				"Genesis 1:1-2 In the beginning there was faith. (2) The second verse.\n" +
				"Genesis 1:3-5 The third verse. (4) Faith begins the fourth verse. (5) The fifth verse.\n",
		},
		{
			name:   "separate arguments",
			args:   []string{"john", "3:16"},
			stdout: "NRSVUE\n\nJohn 3:16 For faith so loved the world.\n",
		},
		{
			name:   "one quoted argument",
			args:   []string{"john 3:16"},
			stdout: "NRSVUE\n\nJohn 3:16 For faith so loved the world.\n",
		},
		{
			name:   "book only defaults to chapter 1",
			args:   []string{"jonah"},
			stdout: "NRSVUE\n\nJonah 1:1 The Jonah verse.\n",
		},
		{
			name:   "translation filter in a quoted argument",
			args:   []string{"gen 1:1–2 t:kjv"},
			stdout: "KJV\n\nGenesis 1:1-2 KJV beginning with faith. (2) KJV second.\n",
		},
		{
			name:   "lines",
			args:   []string{"-L", "gen", "1:1-2"},
			stdout: "NRSVUE\n\nGenesis 1:1 In the beginning there was faith.\nGenesis 1:2 The second verse.\n",
		},
		{
			name: "all translations",
			args: []string{"-t", "all", "john", "3:16"},
			stdout: "KJV John 3:16 KJV for faith so loved.\n\n" +
				"NRSVUE John 3:16 For faith so loved the world.\n\n" +
				"NTONLY John 3:16 NT John three sixteen.\n",
		},
		{
			name:   "translation list skips those without the book",
			args:   []string{"-t", "NTONLY,kjv", "gen", "1:1"},
			stdout: "KJV Genesis 1:1 KJV beginning with faith.\n",
		},
		{
			name:   "no matching verses",
			args:   []string{"gen", "9"},
			stdout: "No matching verses found.\n",
		},
		{
			name:         "ambiguous book",
			args:         []string{"jud", "1"},
			code:         1,
			stderrPrefix: "Error: “jud” is ambiguous, matches: Judges, Jude.\nDid you mean: Jude",
		},
		{
			name:         "unknown book in every translation",
			args:         []string{"-t", "all", "jonn", "1"},
			code:         1,
			stderrPrefix: "Error: No book matches “jonn” in any translation.\nDid you mean: John",
		},
		{
			name:         "unparsable reference",
			args:         []string{"t:kjv"},
			code:         1,
			stderrPrefix: "Error: Could not parse reference “t:kjv”.\n",
		},
		{
			name:         "unknown translations",
			args:         []string{"-t", "niv,kjv,xyz", "john"},
			code:         1,
			stderrPrefix: "Error: No translation matches “niv”.\nError: No translation matches “xyz”.\n",
		},
		{
			name:         "unknown translation filter",
			args:         []string{"john t:niv"},
			code:         1,
			stderrPrefix: "Error: No translation matches “niv”.\n",
		},
		{
			name: "search",
			args: []string{"-s", "faith"},
			stdout: "Search results for \"faith\" - NRSVUE\n\n" +
				"Genesis 1:1 In the beginning there was faith.\n" +
				"John 3:16 For faith so loved the world.\n" +
				"Romans 1:1 Romans one one, by faith.\n\n",
		},
		{
			name: "search ignoring case",
			args: []string{"-i", "-s", "^FAITH"},
			stdout: "Search results for \"^FAITH\" - NRSVUE\n\n" +
				"Genesis 1:4 Faith begins the fourth verse.\n\n",
		},
		{
			name: "search for fixed strings",
			args: []string{"-f", "-s", "a.b"},
			stdout: "Search results for \"a.b\" - NRSVUE\n\n" +
				"John 3:2 John three two, with a.b inside.\n\n",
		},
		{
			name: "search with filters",
			args: []string{"-s", "faith b:john t:all"},
			stdout: "Search results for \"faith\" in john\n\n" +
				"KJV John 3:16 KJV for faith so loved.\n\n" +
				"NRSVUE John 3:16 For faith so loved the world.\n\n",
		},
		{
			name:   "search without results",
			args:   []string{"-s", "nothing"},
			stdout: "Search results for \"nothing\" - NRSVUE\n\nNo results found.\n\n",
		},
		{
			name:         "search with only filters",
			args:         []string{"-s", "b:rom t:kjv"},
			code:         1,
			stderrPrefix: "Error: Enter search text in addition to the filters.\n",
		},
		{
			name:         "search with an invalid pattern",
			args:         []string{"-s", "(x"},
			code:         1,
			stderrPrefix: "Error: invalid regular expression: missing closing ): `(x` (use -f to search for the literal text)\n",
		},
		{
			name:         "search in an unknown book",
			args:         []string{"-t", "all", "-s", "faith b:xyz"},
			code:         1,
			stderrPrefix: "Error: No book matches “xyz” in any translation.\n",
		},
		{
			name:   "list",
			args:   []string{"-l"},
			stdout: "Available translations:\n\nKJV\nNRSVUE\nNTONLY\n",
		},
		{
			name:         "help",
			args:         []string{"-h"},
			stderrPrefix: "Usage: bible ",
		},
		{
			name: "bad flag",
			args: []string{"--bogus"},
			code: 2,
		},
		{
			name:         "negative wrap",
			args:         []string{"-w", "-1"},
			code:         2,
			stderrPrefix: "Error: --wrap must not be negative\n",
		},
		{
			name:   "wrap",
			args:   []string{"-w", "20", "john", "3:16"},
			stdout: "NRSVUE\n\nJohn 3:16 For faith\nso loved the world.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, path, tt.args...)
			if code != tt.code {
				t.Errorf("exit code %d, want %d (stderr %q)", code, tt.code, stderr)
			}
			if stdout != tt.stdout {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tt.stdout)
			}
			if !strings.HasPrefix(stderr, tt.stderrPrefix) || (tt.stderrPrefix == "" && tt.code != 2 && stderr != "") {
				t.Errorf("stderr %q, want prefix %q", stderr, tt.stderrPrefix)
			}
		})
	}
}

func TestMainDefaultTranslationFallback(t *testing.T) {
	path := testdb.Build(t, "CREATE TABLE web (book_id, book, chapter, verse, text);"+
		"INSERT INTO web VALUES (1, 'Genesis', 1, 1, 'WEB text.');")
	code, stdout, stderr := run(t, path)
	if want := "WEB\n\nGenesis 1:1 WEB text.\n"; code != 0 || stdout != want {
		t.Errorf("got %d, %q, %q; want 0, %q", code, stdout, stderr, want)
	}
}

func TestMainOpenError(t *testing.T) {
	open := func(path string) (*bible.Store, error) {
		if path != "other.db" {
			t.Errorf("open got %q, want the --db path", path)
		}
		return nil, errors.New("no database")
	}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"--db", "other.db"}, &stdout, &stderr, open); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if stderr.String() != "Error: no database\n" {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestHighlighter(t *testing.T) {
	re := regexp.MustCompile("faith")
	plain := &runner{}
	if got := plain.highlighter(re)("by faith"); got != "by faith" {
		t.Errorf("without color: %q", got)
	}
	color := &runner{color: true}
	if got := color.highlighter(nil)("by faith"); got != "by faith" {
		t.Errorf("without a pattern: %q", got)
	}
	if got := color.highlighter(re)("by faith, faith"); got != "by "+ansiBold+"faith"+ansiReset+", "+ansiBold+"faith"+ansiReset {
		t.Errorf("with color: %q", got)
	}
	if got := color.bold("x"); got != ansiBold+"x"+ansiReset {
		t.Errorf("bold: %q", got)
	}
}

func TestUseColor(t *testing.T) {
	if useColor(&bytes.Buffer{}) {
		t.Error("useColor of a buffer")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if useColor(f) {
		t.Error("useColor of a regular file")
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		defer tty.Close()
		t.Setenv("NO_COLOR", "1")
		if useColor(tty) {
			t.Error("useColor with NO_COLOR set")
		}
		t.Setenv("NO_COLOR", "")
		if !useColor(tty) {
			t.Error("useColor of a terminal")
		}
	}
}

func TestWrapLine(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"aaa bbb ccc ddd", 10, "aaa bbb\nccc ddd"},
		{"aaa bbb", 10, "aaa bbb"},
		// A word longer than the width is left whole.
		{"abcdefgh ij", 5, "abcdefgh\nij"},
		{"abcdefgh", 5, "abcdefgh"},
		// Escape sequences don't count toward the column.
		{"\x1b[1mabc\x1b[0m def", 6, "\x1b[1mabc\x1b[0m\ndef"},
		// Existing newlines reset the column.
		{"ab cd\nef gh", 4, "ab\ncd\nef\ngh"},
		// Columns count characters, not bytes.
		{"éé éé", 4, "éé\néé"},
		{"", 4, ""},
	}
	for _, tt := range tests {
		b := []byte(tt.in)
		wrapLine(b, tt.width)
		if string(b) != tt.want {
			t.Errorf("wrapLine(%q, %d) = %q, want %q", tt.in, tt.width, b, tt.want)
		}
	}
}

func TestWrapWriter(t *testing.T) {
	const text = "In the beginning there was faith. (2) The second verse.\n" +
		"Genesis 1:3 \x1b[1mThe\x1b[0m third verse.\nlast line without newline"
	var out bytes.Buffer
	ww := &wrapWriter{w: &out, width: 16}
	// Write in uneven pieces that split lines and escape sequences.
	for rest := text; rest != ""; {
		n := min(7, len(rest))
		if _, err := ww.Write([]byte(rest[:n])); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	if err := ww.Flush(); err != nil {
		t.Fatal(err)
	}

	want := []byte(text)
	wrapLine(want, 16)
	if out.String() != string(want) {
		t.Errorf("wrapWriter wrote %q, want %q", out.String(), want)
	}
	// Spaces only ever become newlines.
	if strings.ReplaceAll(out.String(), "\n", " ") != strings.ReplaceAll(text, "\n", " ") {
		t.Error("wrapWriter changed more than spaces")
	}
	for _, line := range strings.Split(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(out.String(), ""), "\n") {
		if n := utf8.RuneCountInString(line); n >= 16 && strings.Contains(line, " ") {
			t.Errorf("line %q is %d columns", line, n)
		}
	}
}
