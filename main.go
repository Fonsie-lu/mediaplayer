package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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

// defaultConfigPath returns ~/.config/mediaplayer.json (via os.UserConfigDir,
// so XDG_CONFIG_HOME is honored), falling back to the working directory when
// no home is resolvable.
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "mediaplayer.json"
	}
	return filepath.Join(dir, "mediaplayer.json")
}

// defaultStarsPath returns ~/.config/mediaplayer-stars.json (via
// os.UserConfigDir, so XDG_CONFIG_HOME is honored), falling back to the working
// directory when no home is resolvable.
func defaultStarsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "mediaplayer-stars.json"
	}
	return filepath.Join(dir, "mediaplayer-stars.json")
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "path to config.json")
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
	stars, err := api.NewStarStore(defaultStarsPath())
	if err != nil {
		log.Fatalf("stars: %v", err)
	}

	h := &api.Handler{Cfg: cfg, Sessions: mgr, Stars: stars}

	mux := http.NewServeMux()
	h.Register(mux)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}
	fileServer := http.FileServer(http.FS(web))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			r2 := *r
			r2.URL.Path = "/browser.html"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		if r.URL.Path == "/player" {
			r2 := *r
			r2.URL.Path = "/player.html"
			fileServer.ServeHTTP(w, &r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	addr := cfg.Host + ":" + strconv.Itoa(cfg.Port)
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
