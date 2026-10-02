// Package data embeds the Bible database into the binary.
//
// Put bible.db in this directory before building. It lives here rather
// than at the repository root because //go:embed can't reference files in
// parent directories.
package data

import _ "embed"

// BibleDB is the raw SQLite database image. It is a string rather than a
// []byte so it stays in the binary's read-only data and is never copied.
//
//go:embed bible.db
var BibleDB string
