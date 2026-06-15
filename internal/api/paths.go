package api

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"mediaplayer/internal/config"
)

var ErrTraversal = errors.New("path traversal")
var ErrMountNotFound = errors.New("mount not found")

// safeJoin joins a mount root with a user-supplied relative path, rejecting
// any result that escapes the mount via ".." or symlinks.
func safeJoin(root, rel string) (string, error) {
	cleaned := filepath.Clean("/" + rel)
	full := filepath.Join(root, cleaned)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) {
		return "", ErrTraversal
	}
	return absFull, nil
}

func resolveMount(cfg *config.Config, idxOrName string) (config.Mount, error) {
	if i, err := strconv.Atoi(idxOrName); err == nil {
		if m, ok := cfg.MountByIndex(i); ok {
			return m, nil
		}
	}
	snap := cfg.Snapshot()
	for _, m := range snap.Mounts {
		if m.Name == idxOrName {
			return m, nil
		}
	}
	return config.Mount{}, ErrMountNotFound
}

// target resolves the mount + path pair every filesystem handler receives,
// writing the appropriate error response on failure. Callers must return
// immediately when ok is false.
func (h *Handler) target(w http.ResponseWriter, mountRef, rel string) (mount config.Mount, full string, ok bool) {
	mount, err := resolveMount(h.Cfg, mountRef)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return config.Mount{}, "", false
	}
	full, err = safeJoin(mount.Path, rel)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return config.Mount{}, "", false
	}
	return mount, full, true
}

// queryTarget is target() for handlers that take mount/path as query params.
func (h *Handler) queryTarget(w http.ResponseWriter, r *http.Request) (config.Mount, string, bool) {
	return h.target(w, r.URL.Query().Get("mount"), r.URL.Query().Get("path"))
}
