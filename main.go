// Command bible reads Bible translations from an embedded SQLite database,
// either on the command line ("bible matt 5:3") or in a browser-based
// viewer ("bible gui").
package main

import (
	"os"

	"github.com/jcallicoat/bible/data"
	"github.com/jcallicoat/bible/internal/bible"
	"github.com/jcallicoat/bible/internal/cli"
	"github.com/jcallicoat/bible/internal/web"
)

func openStore(path string) (*bible.Store, error) {
	if path != "" {
		return bible.OpenFile(path)
	}
	return bible.OpenEmbedded(data.BibleDB)
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "gui" {
		os.Exit(web.Main(args[1:], os.Stderr, openStore))
	}
	os.Exit(cli.Main(args, os.Stdout, os.Stderr, openStore))
}
