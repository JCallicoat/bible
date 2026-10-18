// Package cli implements the plain-text command line interface.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/jcallicoat/bible/internal/bible"
)

type runner struct {
	store  *bible.Store
	stdout io.Writer
	stderr io.Writer
	lines  bool // one verse per line instead of paragraphs
	color  bool // emit ANSI bold escapes
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
		"Search translations for words. Supports 'b:<book>' and 't:<translation>'\n"+
			"(or 't:all') filters anywhere in the text, e.g. -s \"faith b:romans t:kjv\".")
	lines := fs.BoolP("lines", "L", false, "Print one verse per line instead of grouping verses into paragraphs")
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

	store, err := open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	defer store.Close()
	out := bufio.NewWriter(stdout)
	defer out.Flush()
	r := &runner{store: store, stdout: out, stderr: stderr, lines: *lines, color: useColor(stdout)}
	stdout = out

	if *list {
		fmt.Fprint(stdout, "Available translations:\n\n")
		for _, t := range store.Translations() {
			fmt.Fprintln(stdout, r.bold(strings.ToUpper(t)))
		}
		return 0
	}

	var base []string
	if strings.ToLower(strings.TrimSpace(*translation)) == "all" {
		base = store.Translations()
	} else {
		var msgs []string
		for _, name := range strings.Split(*translation, ",") {
			t, msg := store.ResolveTranslation(name)
			if msg != "" {
				msgs = append(msgs, msg)
			} else {
				base = append(base, t)
			}
		}
		if len(msgs) > 0 {
			r.errors(msgs)
			return 1
		}
	}

	if *search != "" {
		return r.search(base, *search)
	}

	// Positionals are "book [chapter_verse [filters...]]", defaulting to
	// Genesis 1, and are joined back together so unquoted multi-word books
	// ("song of solomon 2:1") work.
	pos := fs.Args()
	switch len(pos) {
	case 0:
		pos = []string{"Genesis", "1"}
	case 1:
		pos = append(pos, "1")
	}
	return r.lookup(base, strings.Join(pos, " "))
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

func (r *runner) errors(msgs []string) {
	for _, m := range msgs {
		fmt.Fprintf(r.stderr, "Error: %s\n", m)
	}
}

func (r *runner) fail(err error) int {
	fmt.Fprintf(r.stderr, "Error: %v\n", err)
	return 1
}

func (r *runner) lookup(base []string, raw string) int {
	raw = bible.NormalizeDashes(raw)
	f := bible.ExtractFilters(raw)

	tables, msg := r.store.TablesFor(base, f)
	if msg != "" {
		r.errors([]string{msg})
		return 1
	}

	ref, ok := bible.ParseReference(f.Remainder)
	if !ok {
		r.errors([]string{fmt.Sprintf("could not parse reference “%s”", raw)})
		return 1
	}
	bookInput := f.Book
	if bookInput == "" {
		bookInput = ref.Book
	}

	rows, msgs, err := r.store.Lookup(tables, bookInput, ref)
	if err != nil {
		return r.fail(err)
	}
	r.errors(msgs)

	if len(rows) == 0 {
		if len(msgs) > 0 {
			if s := bible.SuggestBooks(bookInput); len(s) > 0 {
				fmt.Fprintf(r.stderr, "Did you mean: %s?\n", strings.Join(s, ", "))
			}
			return 1
		}
		fmt.Fprintln(r.stdout, "No matching verses found.")
		return 0
	}

	if !r.lines {
		if err := r.store.MarkParagraphs(rows); err != nil {
			return r.fail(err)
		}
	}
	multi := len(tables) > 1
	if !multi {
		fmt.Fprintf(r.stdout, "%s\n\n", r.bold(strings.ToUpper(tables[0])))
	}
	r.printRows(rows, multi, "")
	return 0
}

func (r *runner) search(base []string, raw string) int {
	f := bible.ExtractFilters(raw)
	term := f.Remainder
	if term == "" {
		r.errors([]string{"no search text provided (only filters)."})
		return 1
	}

	tables, msg := r.store.TablesFor(base, f)
	if msg != "" {
		r.errors([]string{msg})
		return 1
	}

	rows, msgs, err := r.store.Search(tables, term, f.Book)
	if err != nil {
		return r.fail(err)
	}
	r.errors(msgs)

	multi := len(tables) > 1
	header := "Search results for \"" + r.bold(term) + "\""
	if f.Book != "" {
		header += " in " + r.bold(f.Book)
	}
	if !multi {
		header += " - " + r.bold(strings.ToUpper(tables[0]))
	}
	fmt.Fprintf(r.stdout, "%s\n\n", header)

	if len(rows) == 0 {
		fmt.Fprint(r.stdout, "No results found.\n\n")
		return 0
	}
	r.printRows(rows, multi, term)
	fmt.Fprintln(r.stdout)
	return 0
}

// printRows prints one paragraph per line: the first verse with its full
// reference and any continuing verses (see bible.Store.MarkParagraphs)
// after it as "(N) text". Translation names, references and verse numbers
// are bold, as are any case-insensitive matches of term in the text.
func (r *runner) printRows(rows []bible.Verse, multi bool, term string) {
	last := ""
	for i, v := range rows {
		if v.Cont {
			fmt.Fprintf(r.stdout, " (%s) %s", r.bold(fmt.Sprint(v.Verse)), r.highlight(v.Text, term))
			continue
		}
		if i > 0 {
			fmt.Fprintln(r.stdout)
		}
		if v.Translation != last {
			last = v.Translation
			if i > 0 {
				fmt.Fprintln(r.stdout)
			}
		}
		prefix := ""
		if multi {
			prefix = r.bold(strings.ToUpper(v.Translation)) + " "
		}
		ref := r.bold(fmt.Sprintf("%s %d:%d", v.Book, v.Chapter, v.Verse))
		fmt.Fprintf(r.stdout, "%s%s %s", prefix, ref, r.highlight(v.Text, term))
	}
	if len(rows) > 0 {
		fmt.Fprintln(r.stdout)
	}
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

// highlight bolds every occurrence of term in text, matching ASCII
// case-insensitively like the LIKE query in bible.Store.Search.
func (r *runner) highlight(text, term string) string {
	if term == "" || !r.color {
		return text
	}
	lowerText, lowerTerm := asciiLower(text), asciiLower(term)
	var b strings.Builder
	for {
		i := strings.Index(lowerText, lowerTerm)
		if i < 0 {
			break
		}
		end := i + len(term)
		b.WriteString(text[:i])
		b.WriteString(r.bold(text[i:end]))
		text, lowerText = text[end:], lowerText[end:]
	}
	b.WriteString(text)
	return b.String()
}

// asciiLower lowercases only A-Z, so byte offsets stay aligned with s.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
