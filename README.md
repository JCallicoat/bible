# bible

Read Bible translations from the command line or in a browser-based viewer.
This is a Go port of the Python `bible-cli`, built as a single
self-contained binary with the translations database embedded in it: no
Python, no Qt, no files to install alongside it.

```
bible matt 5:3                    # print a verse
bible 1cor 13:4-7 t:all           # in every translation
bible -s "faith b:romans t:kjv"   # search
bible gui                         # open the viewer in your browser
```

## Building

You need Go 1.26 or newer.

1. Copy your `bible.db` to `data/bible.db`. It is embedded at build time;
   the build fails with "pattern bible.db: no matching files found" if it
   is missing.
2. Build:

   ```
   make build        # or: CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bible .
   ```

`make release` cross-compiles for Linux, macOS (amd64/arm64) and Windows
into `dist/`. Builds use no cgo, so any platform can build all of them.

The binary is about 12 MB plus the size of `bible.db`. SQLite reads the
embedded database in place (through the `readervfs` VFS of
[ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3)), so it is never
copied into memory or extracted to disk; a lookup typically runs in
10 to 40 ms.

## Command line

```
bible [options] [book [chapter[:verse[-verse]]] [filters...]]

  -l, --list                 List available translations
  -t, --translation string   Translation code(s), comma separated, or "all" (default "NRSVUE")
  -s, --search string        Search verse text
```

With no arguments it prints Genesis 1. Books can be abbreviated to any
unambiguous prefix (`1cor`, `song`, `philem`). Lookups and searches accept
`t:<translation>` / `t:all` filters, and searches also accept `b:<book>`.
Options can appear anywhere on the line (`bible matt 5:3 -t kjv`). Output,
messages and exit codes match the Python version.

## Viewer

`bible gui` starts a small web server on 127.0.0.1 (on a free port) and
opens the viewer in your default browser. It works like the Qt window:
translation, book and chapter dropdowns, a lookup box, a search box and the
text view.

The server stops on its own about 3 seconds after you close the last tab
showing it (reloading the page is fine), or when you click **Quit**. If no
tab connects within 30 seconds of launch, it stops too.

```
bible gui --no-browser    # just print the address; stop with Ctrl+C
bible gui --port 8080     # use a fixed port
```

Keyboard shortcuts (Cmd works in place of Ctrl on a Mac):

| Keys    | Action            |
|---------|-------------------|
| Ctrl+L  | Lookup            |
| Ctrl+S  | Search            |
| Alt+T   | Translation       |
| Ctrl+B  | Book              |
| Ctrl+K  | Chapter           |
| Ctrl+D  | Text view         |
| Alt+Q   | Quit              |

Translation and Quit moved from Ctrl+T and Ctrl+Q to Alt because browsers
don't let pages override those two.

On Windows the binary is a console program, so a console window stays open
while the viewer runs.

## Differences from the Python version

- `%` and `_` in book names and search text match literally; Python
  treated them as SQL wildcards.
- After a lookup with `t:<translation>`, the book dropdown now lists that
  translation's books; the Qt version kept the previous translation's list.
- The database location is no longer a command-line argument. A hidden
  `--db PATH` option (on both `bible` and `bible gui`) reads a database
  file instead of the embedded copy, for trying a new `bible.db` without
  rebuilding.

## Development

Layout:

```
main.go                 dispatches "gui" vs. the CLI
data/                   embeds bible.db (go:embed can't reach parent directories)
internal/bible/         shared logic: database, filters, references, lookup, search, suggestions
internal/cli/           command-line interface
internal/web/           viewer: HTTP API, auto-shutdown, browser launch
internal/web/static/    the page (HTML/CSS/JS), embedded
internal/testfixture/   synthetic database for tests
```
