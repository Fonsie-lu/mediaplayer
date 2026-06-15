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

	"mediaplayer/internal/api"
	"mediaplayer/internal/config"
	"mediaplayer/internal/session"
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
	flag.Parse()

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

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("shutting down, cleaning transcode sessions...")
	mgr.CloseAll()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
