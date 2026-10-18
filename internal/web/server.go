// Package web serves the browser-based viewer ("bible gui"): a small
// single-page app plus a JSON API over the shared bible package.
//
// The server listens only on 127.0.0.1 and exits on its own once the last
// browser tab is closed: each open page holds a Server-Sent Events stream,
// and when the number of open streams drops to zero the server waits a
// short grace period (long enough for a page reload to reconnect) and then
// shuts down.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/jcallicoat/bible/internal/bible"
)

//go:embed static
var staticFS embed.FS

const (
	// How long to wait after the last tab disconnects before exiting.
	// Long enough for a reload to reconnect.
	graceDefault = 3 * time.Second
	// How long to wait for the first tab to connect after launch.
	startupDefault = 30 * time.Second
	// Keep-alive comments on the event stream, so a vanished peer is
	// noticed even if the TCP close was missed.
	pingInterval = 15 * time.Second
)

type server struct {
	store *bible.Store
	addr  string // host:port the browser should use
	lines bool   // one verse per line instead of paragraphs

	grace time.Duration

	mu      sync.Mutex
	clients int
	timer   *time.Timer // pending shutdown, if any

	quit     chan struct{} // closed to stop the server
	quitOnce sync.Once
}

// Main runs "bible gui" with args (after "gui") and returns the exit code.
func Main(args []string, stderr io.Writer, open bible.Opener) int {
	fset := pflag.NewFlagSet("bible gui", pflag.ContinueOnError)
	fset.SetOutput(stderr)
	noBrowser := fset.Bool("no-browser", false, "Don't open a browser; just print the URL (and don't exit if no tab connects)")
	port := fset.Int("port", 0, "Port to listen on (default: any free port)")
	lines := fset.BoolP("lines", "L", false, "Show one verse per line instead of grouping verses into paragraphs")
	dbPath := fset.String("db", "", "Use this database file instead of the embedded one")
	_ = fset.MarkHidden("db")
	fset.Usage = func() {
		fmt.Fprint(stderr, "Usage: bible gui [-L] [--no-browser] [--port N]\n\n"+
			"Opens the Bible viewer in your web browser. The viewer exits\n"+
			"when its last browser tab is closed.\n\nOptions:\n")
		fset.PrintDefaults()
	}
	if err := fset.Parse(args); err != nil {
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

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	s := &server{
		store: store,
		addr:  ln.Addr().String(),
		lines: *lines,
		grace: graceDefault,
		quit:  make(chan struct{}),
	}
	url := "http://" + s.addr + "/"

	fmt.Fprintf(stderr, "Bible viewer running at %s\n", url)
	if *noBrowser {
		fmt.Fprintln(stderr, "Open that address in your browser. Press Ctrl+C to stop.")
	} else {
		fmt.Fprintln(stderr, "It will stop when you close the browser tab (or press Ctrl+C).")
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(stderr, "Couldn't open a browser (%v); open the address above yourself.\n", err)
		} else {
			s.scheduleShutdown(startupDefault)
		}
	}

	if err := s.serve(ln); err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/translations", s.handleTranslations)
	mux.HandleFunc("GET /api/books", s.handleBooks)
	mux.HandleFunc("GET /api/chapter", s.handleChapter)
	mux.HandleFunc("GET /api/lookup", s.handleLookup)
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/quit", s.handleQuit)
	return s.checkHost(mux)
}

// checkHost rejects requests addressed to any other host name, so a web
// page can't reach this server through DNS rebinding.
func (s *server) checkHost(next http.Handler) http.Handler {
	_, port, _ := net.SplitHostPort(s.addr)
	allowed := map[string]bool{
		s.addr:              true,
		"localhost:" + port: true,
		"[::1]:" + port:     true,
		"127.0.0.1:" + port: true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			s.stop()
		case <-s.quit:
		}
	}()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return err
	case <-s.quit:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	return nil
}

func (s *server) stop() {
	s.quitOnce.Do(func() { close(s.quit) })
}

// scheduleShutdown stops the server after d unless a client connects first.
func (s *server) scheduleShutdown(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(d, func() {
		s.mu.Lock()
		idle := s.clients == 0
		s.mu.Unlock()
		if idle {
			s.stop()
		}
	})
}

func (s *server) connect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients++
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *server) disconnect() {
	s.mu.Lock()
	s.clients--
	idle := s.clients == 0
	s.mu.Unlock()
	if idle {
		s.scheduleShutdown(s.grace)
	}
}

// handleEvents holds an event stream open for as long as the page is open.
// Its only job is to let the server notice when the last tab goes away.
func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.WriteString(w, "event: hello\ndata: {}\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	s.connect()
	defer s.disconnect()

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.quit:
			_, _ = io.WriteString(w, "event: bye\ndata: {}\n\n")
			_ = rc.Flush()
			return
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *server) handleQuit(w http.ResponseWriter, r *http.Request) {
	// Only our own page may stop the server.
	if r.Header.Get("Origin") != "http://"+r.Host {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	go s.stop()
}
