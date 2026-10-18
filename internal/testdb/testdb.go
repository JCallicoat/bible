// Package testdb builds small synthetic Bible databases for tests. The
// standard one is described in testdb.sql.
package testdb

import (
	"database/sql"
	_ "embed"
	"net/url"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// SQL creates the standard test database.
//
//go:embed testdb.sql
var SQL string

// Path builds the standard test database in a temporary directory and
// returns its path.
func Path(t testing.TB) string {
	return Build(t, SQL)
}

// Build runs script against a new database file in a temporary directory
// and returns its path.
func Build(t testing.TB, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bible.db")
	u := url.URL{Scheme: "file", OmitHost: true, Path: path}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(script); err != nil {
		t.Fatalf("building test database: %v", err)
	}
	return path
}
