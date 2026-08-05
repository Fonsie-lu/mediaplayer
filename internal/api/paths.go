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
//
// Two checks, because they catch different things. The lexical one anchors the
// rel path: prepending "/" before Clean makes ".." collapse against the root
// instead of climbing past it, so "../../../etc" becomes "/etc" and lands
// inside the mount. The symlink one covers what Clean cannot see — a link
// stored inside the mount may point anywhere on the host, and following it
// would hand out (or delete) files the mount was never meant to expose.
//
// Symlinks that stay inside the same mount are fine; ones leaving it, and
// links into another configured mount, are rejected.
func safeJoin(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(filepath.Join(absRoot, filepath.Clean("/"+rel)))
	if err != nil {
		return "", err
	}
	if !within(absRoot, absFull) {
		return "", ErrTraversal
	}
	if !within(resolveExisting(absRoot), resolveExisting(absFull)) {
		return "", ErrTraversal
	}
	// The unresolved path is what handlers act on: callers compare it against
	// mount.Path (del) and rebuild siblings from it (rename), and resolving
	// would break both for a mount root that is itself a symlink.
	return absFull, nil
}

// within reports whether p is root itself or nested under it. Comparing with
// the separator appended keeps "/srv/media-backup" from matching "/srv/media".
func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// resolveExisting expands symlinks in the longest prefix of p that exists and
// re-appends the rest verbatim. filepath.EvalSymlinks fails outright on a
// missing path, but handlers legitimately name paths that don't exist yet
// (a rename destination, or a mount pointing at an unmounted disk), and a
// component that doesn't exist cannot be a symlink. A prefix that exists but
// can't be resolved (permissions) is likewise left as-is — the handler's own
// filesystem call fails on it anyway.
func resolveExisting(p string) string {
	rest := ""
	for {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(p)
		if parent == p { // hit the filesystem root, nothing left to resolve
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
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
