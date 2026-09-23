package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"

	"mediaplayer/internal/api"
	"mediaplayer/internal/applog"
	"mediaplayer/internal/config"
	"mediaplayer/internal/session"
	"mediaplayer/internal/tui"
)

//go:embed all:web
var webFS embed.FS

// configPath returns name under ~/.config (via os.UserConfigDir, so
// XDG_CONFIG_HOME is honored), falling back to the working directory when no
// home is resolvable.
func configPath(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return name
	}
	return filepath.Join(dir, name)
}

func main() {
	cfgPath := flag.String("config", configPath("mediaplayer.json"), "path to config.json")
	noTUI := flag.Bool("no-tui", false, "run headless (no terminal UI even on a TTY)")
	flag.Parse()

	// The TUI takes over the terminal by default when attached to one; redirect
	// the logger into an in-memory buffer (its Logs tab) so it doesn't corrupt
	// the rendered UI. Headless runs keep logging to stderr as before.
	useTUI := !*noTUI && isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
	if useTUI {
		log.SetFlags(0)
		log.SetOutput(applog.Default)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Wipe leftover transcode dirs from any prior crashed run before
	// new sessions allocate names that could collide.
	session.CleanStaleTempDirs()

	mgr := session.NewManager()
	mgr.StartReaper()

	// Starred entries persist to ~/.config/mediaplayer-stars.json.
	stars, err := api.NewStarStore(configPath("mediaplayer-stars.json"), cfg)
	if err != nil {
		log.Fatalf("stars: %v", err)
	}

	h := &api.Handler{Cfg: cfg, Sessions: mgr, Stars: stars}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}
	mux := newMux(h, web)

	addr := cfg.Addr()
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	snap := cfg.Snapshot()
	log.Printf("mediaplayer listening on http://%s", addr)
	log.Printf("mounts: %d", len(snap.Mounts))
	for i, m := range snap.Mounts {
		log.Printf("  [%d] %s -> %s", i, m.Name, m.Path)
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	shutdown := func() {
		log.Println("shutting down, cleaning transcode sessions...")
		mgr.CloseAll()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}

	if useTUI {
		restart, err := tui.Run(cfg, stars, applog.Default, "http://"+addr)
		shutdown()
		if err != nil {
			log.SetOutput(os.Stderr)
			log.Fatalf("tui: %v", err)
		}
		if restart {
			reexec()
		}
		return
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	shutdown()
}

// newMux is the server's whole HTTP surface: the API's patterns plus the two
// pages and the embedded assets. It exists as a function so a test can assert
// on the same routing table the server runs (see main_test.go).
//
// Everything is registered under an exact pattern — there is deliberately no
// bare "/" catch-all, and that is load-bearing for the API: ServeMux answers a
// method mismatch with 405 only when *nothing* matched, so a catch-all would
// swallow every `GET /api/rename` into the file server's 404 and quietly undo
// the method enforcement in api.Register's patterns.
//
// Two pages, not a SPA (spec): /player is its own URL so browser back returns
// to the file browser. Both are served by rewriting to the embedded file rather
// than redirecting, which would show .html in the address bar.
func newMux(h *api.Handler, web fs.FS) *http.ServeMux {
	mux := http.NewServeMux()
	h.Register(mux)
	fileServer := withETags(web, http.FileServer(http.FS(web)))
	mux.HandleFunc("GET /{$}", servePage(fileServer, "/browser.html"))
	mux.HandleFunc("GET /player", servePage(fileServer, "/player.html"))
	mux.Handle("GET /css/", fileServer)
	mux.Handle("GET /js/", fileServer)
	return mux
}

// withETags gives every embedded file a content-hash ETag, computed once, and
// asks browsers to revalidate rather than reuse blindly.
//
// Embedded files have no modification time, so http.FileServer sends neither
// Last-Modified nor ETag, and without a validator a browser can only
// re-download: every hop between the file list and the player re-fetched the
// whole module graph and both stylesheets. With one, those hops are 304s. The
// FileServer does the conditional-request handling itself — it reads the ETag
// header already set on the response — so this only has to set it. no-cache
// (not a max-age) because the files change with every rebuild of the binary.
//
// It must wrap the file server *inside* servePage's rewrite, since the hash is
// looked up by the path actually being served.
func withETags(web fs.FS, next http.Handler) http.Handler {
	tags := map[string]string{}
	_ = fs.WalkDir(web, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(web, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tags["/"+p] = `"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := tags[r.URL.Path]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// servePage serves one embedded file under whatever URL it is registered at.
// The request is copied (URL included — the copy would otherwise alias the
// caller's) so the rewrite can't be seen upstream.
func servePage(fileServer http.Handler, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r2 := *r
		u := *r.URL
		u.Path = name
		r2.URL = &u
		fileServer.ServeHTTP(w, &r2)
	}
}

// reexec replaces the current process image with a fresh copy of the executable,
// preserving the original arguments and environment. The listening socket is
// closed on exec (Go sets close-on-exec), so the restarted process rebinds the
// port; the server was already gracefully shut down by the caller.
func reexec() {
	exe, err := os.Executable()
	if err != nil {
		log.SetOutput(os.Stderr)
		log.Fatalf("restart: %v", err)
	}
	args := append([]string{exe}, os.Args[1:]...)
	if err := syscall.Exec(exe, args, os.Environ()); err != nil {
		log.SetOutput(os.Stderr)
		log.Fatalf("restart: %v", err)
	}
}
