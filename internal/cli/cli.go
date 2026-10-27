// Package cli implements the plain-text command line interface.
package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/pflag"

	"github.com/jcallicoat/bible/internal/bible"
)

type runner struct {
	store  *bible.Store
	stdout io.Writer
	stderr io.Writer
	lines  bool // one verse per line instead of paragraphs
	color  bool // emit ANSI bold escapes

	fixed      bool // search for the literal text instead of a regexp
	ignoreCase bool // case-insensitive search
}

// Main runs the CLI with args (without the program name) and returns the
// process exit code.
func Main(args []string, stdout, stderr io.Writer, open bible.Opener) int {
	fs := pflag.NewFlagSet("bible", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	list := fs.BoolP("list", "l", false, "List available translations")
	translation := fs.StringP("translation", "t", strings.ToUpper(bible.DefaultTranslation),
		"The translation code (e.g., NET, KJV). Use commas for multiple, or 'all' for\n"+
			"all translations. Can be overridden by an embedded t:<name> or t:all filter.")
	search := fs.StringP("search", "s", "",
		"Search translations for a regular expression. Supports 'b:<book>' and\n"+
			"'t:<translation>' (or 't:all') filters anywhere in the text, e.g.\n"+
			"-s \"faith b:romans t:kjv\".")
	fixed := fs.BoolP("fixed-strings", "f", false, "Search for the literal text instead of a regular expression")
	ignoreCase := fs.BoolP("ignore-case", "i", false, "Ignore case when searching")
	lines := fs.BoolP("lines", "L", false, "Print one verse per line instead of grouping verses into paragraphs")
	width := fs.IntP("wrap", "w", 0, "Wrap output lines at the last space before this column (0: no wrapping)")
	dbPath := fs.String("db", "", "Use this database file instead of the embedded one")
	_ = fs.MarkHidden("db")
	fs.Usage = func() {
		fmt.Fprint(stderr, usageHeader)
		fs.PrintDefaults()
		fmt.Fprint(stderr, usageFooter)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *width < 0 {
		fmt.Fprintln(stderr, "Error: --wrap must not be negative")
		return 2
	}

	store, err := open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	defer store.Close()
	out := bufio.NewWriter(stdout)
	defer out.Flush()
	var w io.Writer = out
	if *width > 0 {
		ww := &wrapWriter{w: out, width: *width}
		defer ww.Flush() // before out.Flush
		w = ww
	}
	r := &runner{store: store, stdout: w, stderr: stderr, lines: *lines, color: useColor(stdout),
		fixed: *fixed, ignoreCase: *ignoreCase}

	if *list {
		fmt.Fprint(r.stdout, "Available translations:\n\n")
		for _, t := range store.Translations() {
			fmt.Fprintln(r.stdout, r.bold(strings.ToUpper(t)))
		}
		return 0
	}

	base, ok := r.translations(*translation, fs.Changed("translation"))
	if !ok {
		return 1
	}

	if *search != "" {
		return r.search(base, *search)
	}

	// Positionals are "book [chapter[:verse[-verse]]] [filters...]",
	// defaulting to Genesis 1 (ParseReference defaults the chapter), and
	// are joined back together so unquoted multi-word books ("song of
	// solomon 2:1") work.
	pos := fs.Args()
	if len(pos) == 0 {
		pos = []string{"Genesis", "1"}
	}
	return r.lookup(base, strings.Join(pos, " "))
}

// translations resolves the -t flag: a comma-separated list of names or
// "all". When the flag wasn't given, it is the store's default translation
// (DefaultTranslation, or the first one if the database lacks it). It
// reports every unknown name and returns false if there are any.
func (r *runner) translations(spec string, given bool) ([]string, bool) {
	if !given {
		return []string{r.store.DefaultTranslation()}, true
	}
	if strings.EqualFold(strings.TrimSpace(spec), "all") {
		return r.store.Translations(), true
	}
	var base []string
	ok := true
	for _, name := range strings.Split(spec, ",") {
		t, msg := r.store.ResolveTranslation(name)
		if msg != "" {
			r.fail(msg)
			ok = false
			continue
		}
		base = append(base, t)
	}
	return base, ok
}

const usageHeader = `Usage: bible [options] [book [chapter[:verse[-verse]]] [filters...]]
       bible gui [-L] [--no-browser] [--port N]

Query Bible translations. With no reference, shows Genesis 1.

Examples:
  bible matt 5:3
  bible 1cor 13:4-7 t:all
  bible -t kjv,nrsvue john 3:16
  bible -s "faith b:romans t:kjv"
  bible gui

Options:
`

const usageFooter = `
A lookup can include a t:<translation> or t:all filter, e.g. "1cor 1:1 t:all".
`

// fail prints msg as an error and returns the exit code for it.
func (r *runner) fail(msg string) int {
	fmt.Fprintf(r.stderr, "Error: %s\n", msg)
	return 1
}

func (r *runner) lookup(base []string, raw string) int {
	q, msg := r.store.ParseLookup(base, raw)
	if msg != "" {
		return r.fail(msg)
	}
	rows, msg, err := r.store.Lookup(q)
	if err != nil {
		return r.fail(err.Error())
	}
	if msg != "" {
		r.fail(msg)
		if s := bible.SuggestBooks(q.Book); len(s) > 0 {
			fmt.Fprintf(r.stderr, "Did you mean: %s?\n", strings.Join(s, ", "))
		}
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(r.stdout, "No matching verses found.")
		return 0
	}

	if !r.lines {
		if err := r.store.MarkParagraphs(rows); err != nil {
			return r.fail(err.Error())
		}
	}
	multi := len(q.Tables) > 1
	if !multi {
		fmt.Fprintf(r.stdout, "%s\n\n", r.bold(strings.ToUpper(q.Tables[0])))
	}
	r.printRows(rows, multi, nil)
	return 0
}

func (r *runner) search(base []string, raw string) int {
	q, msg := r.store.ParseSearch(base, raw)
	if msg != "" {
		return r.fail(msg)
	}
	re, err := bible.SearchPattern(q.Term, r.fixed, r.ignoreCase)
	if err != nil {
		return r.fail(err.Error() + " (use -f to search for the literal text)")
	}
	rows, msg, err := r.store.Search(q, re)
	if err != nil {
		return r.fail(err.Error())
	}
	if msg != "" {
		return r.fail(msg)
	}

	multi := len(q.Tables) > 1
	header := "Search results for \"" + r.bold(q.Term) + "\""
	if q.Book != "" {
		header += " in " + r.bold(q.Book)
	}
	if !multi {
		header += " - " + r.bold(strings.ToUpper(q.Tables[0]))
	}
	fmt.Fprintf(r.stdout, "%s\n\n", header)

	if len(rows) == 0 {
		fmt.Fprint(r.stdout, "No results found.\n\n")
		return 0
	}
	r.printRows(rows, multi, re)
	fmt.Fprintln(r.stdout)
	return 0
}

// printRows prints one paragraph per line: the first verse with the
// paragraph's full reference (e.g. "Matthew 5:3-6") and any continuing verses (see bible.Store.MarkParagraphs)
// after it as "(N) text". Translation names, references and verse numbers
// are bold, as are any matches of re (if not nil) in the text.
func (r *runner) printRows(rows []bible.Verse, multi bool, re *regexp.Regexp) {
	hl := r.highlighter(re)
	for i, v := range rows {
		if v.Cont {
			fmt.Fprintf(r.stdout, " (%s) %s", r.bold(strconv.Itoa(v.Verse)), hl(v.Text))
			continue
		}
		if i > 0 {
			// End the previous paragraph, plus a blank line between
			// translations.
			fmt.Fprintln(r.stdout)
			if v.Translation != rows[i-1].Translation {
				fmt.Fprintln(r.stdout)
			}
		}
		prefix := ""
		if multi {
			prefix = r.bold(strings.ToUpper(v.Translation)) + " "
		}
		ref := fmt.Sprintf("%s %d:%d", v.Book, v.Chapter, v.Verse)
		if last := paragraphEnd(rows, i); last != v.Verse {
			ref += fmt.Sprintf("-%d", last)
		}
		fmt.Fprintf(r.stdout, "%s%s %s", prefix, r.bold(ref), hl(v.Text))
	}
	if len(rows) > 0 {
		fmt.Fprintln(r.stdout)
	}
}

// paragraphEnd returns the verse number of the last verse in the paragraph
// starting at rows[i].
func paragraphEnd(rows []bible.Verse, i int) int {
	for i+1 < len(rows) && rows[i+1].Cont {
		i++
	}
	return rows[i].Verse
}

const (
	ansiBold  = "\x1b[1m"
	ansiReset = "\x1b[0m"
)

// useColor reports whether stdout is a terminal and NO_COLOR
// (https://no-color.org) is unset or empty.
func useColor(stdout io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (r *runner) bold(s string) string {
	if !r.color {
		return s
	}
	return ansiBold + s + ansiReset
}

// highlighter returns a function that bolds the matches of re in a verse,
// or leaves it unchanged if re is nil.
func (r *runner) highlighter(re *regexp.Regexp) func(text string) string {
	if re == nil || !r.color {
		return func(text string) string { return text }
	}
	segments := bible.Highlighter(re)
	return func(text string) string {
		var b strings.Builder
		for _, seg := range segments(text) {
			if seg.Highlight {
				b.WriteString(r.bold(seg.Text))
			} else {
				b.WriteString(seg.Text)
			}
		}
		return b.String()
	}
}

// wrapWriter breaks each line it writes at the last space before column
// width, replacing that space with a newline. ANSI escape sequences don't
// count toward the column, and a word longer than width is left whole.
type wrapWriter struct {
	w     io.Writer
	width int
	line  []byte // pending output not yet ended by a newline
}

func (ww *wrapWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			ww.line = append(ww.line, p...)
			break
		}
		ww.line = append(ww.line, p[:i+1]...)
		p = p[i+1:]
		if err := ww.Flush(); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// Flush wraps and writes any pending output.
func (ww *wrapWriter) Flush() error {
	if len(ww.line) == 0 {
		return nil
	}
	wrapLine(ww.line, ww.width)
	_, err := ww.w.Write(ww.line)
	ww.line = ww.line[:0]
	return err
}

// wrapLine wraps b in place so that no line reaches width columns where a
// space allows breaking earlier.
func wrapLine(b []byte, width int) {
	col := 0 // visible characters on the current line
	space, spaceCol := -1, 0
	for i := 0; i < len(b); {
		switch {
		case b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[':
			// Skip a CSI sequence up to and including its final byte.
			i += 2
			for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
				i++
			}
			i++
			continue
		case b[i] == '\n':
			col, space = 0, -1
			i++
			continue
		case b[i] == ' ':
			space, spaceCol = i, col
		}
		_, size := utf8.DecodeRune(b[i:])
		i += size
		col++
		if col >= width && space >= 0 {
			b[space] = '\n'
			col -= spaceCol + 1
			space = -1
		}
	}
}
