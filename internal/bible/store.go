// Package bible holds the database access and text-parsing logic shared by
// the CLI and the web GUI, so that translation/book resolution, filter
// parsing and query construction behave identically in both. Presentation
// (plain text vs. HTML) lives in the cli and web packages.
package bible

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/ncruces/go-sqlite3/driver"
	sqlregexp "github.com/ncruces/go-sqlite3/ext/regexp"
	"github.com/ncruces/go-sqlite3/util/ioutil"
	"github.com/ncruces/go-sqlite3/vfs/readervfs"
)

// DefaultTranslation is the translation used when none is requested.
const DefaultTranslation = "nrsvue"

// requiredColumns are the columns a table needs to count as a translation.
var requiredColumns = []string{"book", "book_id", "chapter", "verse", "text"}

// Book is one book of a translation, with its chapter numbers in order.
type Book struct {
	Name     string `json:"name"`
	Chapters []int  `json:"chapters"`
}

// Store is a read-only handle on the Bible database. The database never
// changes while the program runs, so the translation list and each
// translation's book/chapter lists are loaded once and cached.
type Store struct {
	db           *sql.DB
	translations []string

	hasParagraphs bool

	// The caches are filled lazily. mu guards the maps only, not the queries
	// that fill them, so a slow query doesn't block other tables; two
	// requests racing to fill the same entry just store equal values.
	mu         sync.Mutex
	books      map[string][]Book     // keyed by table name
	bookNames  map[string][]bookName // keyed by table name
	paragraphs map[chapterKey][]int  // paragraph starts
}

// bookName is a book name along with its normalizeBook form.
type bookName struct {
	name, key string
}

// Opener opens the Bible database: the embedded copy when path is "",
// otherwise the file at path.
type Opener func(path string) (*Store, error)

// OpenEmbedded opens a database image held in memory (typically a
// //go:embed string). SQLite reads pages directly from data, so nothing is
// copied or written to disk.
func OpenEmbedded(data string) (*Store, error) {
	const name = "bible.db"
	readervfs.Create(name, ioutil.NewSizeReaderAt(strings.NewReader(data)))
	db, err := driver.Open("file:"+name+"?vfs=reader", sqlregexp.Register)
	if err != nil {
		return nil, err
	}
	return newStore(db)
}

// OpenFile opens a database file on disk read-only. Used for the --db
// override, e.g. to try a new bible.db without rebuilding.
func OpenFile(path string) (*Store, error) {
	u := url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: "mode=ro"}
	db, err := driver.Open(u.String(), sqlregexp.Register)
	if err != nil {
		return nil, err
	}
	return newStore(db)
}

func newStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db, books: map[string][]Book{}, bookNames: map[string][]bookName{},
		paragraphs: map[chapterKey][]int{}}
	if err := s.load(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	var err error
	if s.translations, err = s.tablesWithColumns(requiredColumns...); err != nil {
		return fmt.Errorf("reading database: %w", err)
	}
	if len(s.translations) == 0 {
		return fmt.Errorf("no tables with %s columns were found in the database",
			strings.Join(requiredColumns, "/"))
	}
	if s.hasParagraphs, err = s.loadHasParagraphs(); err != nil {
		return fmt.Errorf("reading database: %w", err)
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// QuoteIdent safely quotes an SQLite identifier (table/column name).
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// tablesWithColumns returns the tables that have all of cols, alphabetically.
func (s *Store) tablesWithColumns(cols ...string) ([]string, error) {
	args := make([]any, 0, len(cols)+1)
	for _, c := range cols {
		args = append(args, c)
	}
	args = append(args, len(cols))
	rows, err := s.db.Query(
		"SELECT m.name FROM sqlite_master AS m JOIN pragma_table_info(m.name) AS p "+
			"WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' "+
			"AND p.name IN (?"+strings.Repeat(", ?", len(cols)-1)+") "+
			"GROUP BY m.name HAVING COUNT(*) = ? ORDER BY m.name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// Translations returns the tables that look like translations (they have
// all of requiredColumns), alphabetically, as stored in the database
// (typically lowercase). There is always at least one.
func (s *Store) Translations() []string {
	return slices.Clone(s.translations)
}

// HasTranslation reports whether table is one of the translations, exactly
// as returned by Translations.
func (s *Store) HasTranslation(table string) bool {
	return slices.Contains(s.translations, table)
}

// DefaultTranslation returns DefaultTranslation if the database has it,
// otherwise the first translation.
func (s *Store) DefaultTranslation() string {
	if s.HasTranslation(DefaultTranslation) {
		return DefaultTranslation
	}
	return s.translations[0]
}

// ResolveTranslation matches name case-insensitively against the
// translation list. It returns the table name, or an error message.
func (s *Store) ResolveTranslation(name string) (string, string) {
	trimmed := strings.TrimSpace(name)
	for _, t := range s.translations {
		if strings.EqualFold(t, trimmed) {
			return t, ""
		}
	}
	return "", fmt.Sprintf("No translation matches “%s”.", name)
}

// Books returns table's books in canonical order (by minimum book_id), each
// with its chapters in order. The result is cached and must not be modified.
func (s *Store) Books(table string) ([]Book, error) {
	s.mu.Lock()
	b, ok := s.books[table]
	s.mu.Unlock()
	if ok {
		return b, nil
	}

	rows, err := s.db.Query(
		"SELECT book, chapter, MIN(MIN(book_id)) OVER (PARTITION BY book) AS ord " +
			"FROM " + QuoteIdent(table) + " GROUP BY book, chapter " +
			"ORDER BY ord, book, chapter")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var books []Book
	for rows.Next() {
		var name string
		var chapter int
		var ord sql.NullInt64
		if err := rows.Scan(&name, &chapter, &ord); err != nil {
			return nil, err
		}
		if n := len(books); n == 0 || books[n-1].Name != name {
			books = append(books, Book{Name: name})
		}
		last := &books[len(books)-1]
		last.Chapters = append(last.Chapters, chapter)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.books[table] = books
	s.mu.Unlock()
	return books, nil
}

// bookList returns just the book names of table in canonical order. It is
// cheaper than Books, which also collects every chapter number.
func (s *Store) bookList(table string) ([]bookName, error) {
	s.mu.Lock()
	names, ok := s.bookNames[table]
	books, haveBooks := s.books[table]
	s.mu.Unlock()
	if ok {
		return names, nil
	}
	if haveBooks {
		for _, b := range books {
			names = append(names, bookName{b.Name, normalizeBook(b.Name)})
		}
	} else {
		var err error
		if names, err = s.queryBookList(table); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.bookNames[table] = names
	s.mu.Unlock()
	return names, nil
}

// queryBookList walks the distinct book_ids of table with one index seek
// each (the (book_id, chapter, verse) index makes this far cheaper than a
// GROUP BY scan of the whole table). The first time a name appears is its
// lowest book_id, which gives the canonical order.
func (s *Store) queryBookList(table string) ([]bookName, error) {
	stmt, err := s.db.Prepare("SELECT book_id, book FROM " + QuoteIdent(table) +
		" WHERE book_id > ? ORDER BY book_id LIMIT 1")
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	var names []bookName
	seen := map[string]bool{}
	var last int64 = -1 << 62
	for {
		var id int64
		var name string
		err := stmt.QueryRow(last).Scan(&id, &name)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, bookName{name, normalizeBook(name)})
		}
		last = id
	}
	return names, nil
}

// ResolveBook resolves a possibly-abbreviated book name against table's
// books. The input (lowercased, spaces removed) must be a prefix of the
// book name normalized the same way, e.g. "1cor" matches "1 Corinthians".
// A book whose whole name matches wins over books it is a prefix of, so a
// resolved name always resolves to itself. It returns the book name, or an
// error message if there is no match or more than one.
func (s *Store) ResolveBook(table, input string) (string, string, error) {
	books, err := s.bookList(table)
	if err != nil {
		return "", "", err
	}
	prefix := normalizeBook(strings.ToLower(input))
	var matches []string
	for _, b := range books {
		if b.key == prefix {
			return b.name, "", nil
		}
		if strings.HasPrefix(b.key, prefix) {
			matches = append(matches, b.name)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Sprintf("No book matches “%s”.", input), nil
	case 1:
		return matches[0], "", nil
	default:
		return "", fmt.Sprintf("“%s” is ambiguous, matches: %s.",
			input, strings.Join(matches, ", ")), nil
	}
}

// normalizeBook removes spaces and lowercases ASCII letters, like SQLite's
// LOWER(REPLACE(book, " ", "")) (LOWER only folds ASCII case).
func normalizeBook(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
