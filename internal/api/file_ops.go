package api

import (
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type renameReq struct {
	Mount   string `json:"mount"`
	Path    string `json:"path"`
	NewName string `json:"new_name"`
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	var req renameReq
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.ContainsAny(req.NewName, "/\\") || req.NewName == "" || req.NewName == "." || req.NewName == ".." {
		writeErr(w, http.StatusBadRequest, "invalid name")
		return
	}
	mount, src, ok := h.target(w, req.Mount, req.Path)
	if !ok {
		return
	}
	dst := filepath.Join(filepath.Dir(src), req.NewName)
	dstCheck, err := safeJoin(mount.Path, filepath.Join(filepath.Dir(req.Path), req.NewName))
	if err != nil || dstCheck != dst {
		writeErr(w, http.StatusBadRequest, "invalid destination")
		return
	}
	// os.Rename silently replaces an existing destination; a rename onto a
	// sibling's name must not destroy the sibling. (Check-then-rename, so not
	// race-free — the same accepted limit as safeJoin's.)
	if dst != src {
		if _, err := os.Lstat(dst); err == nil {
			writeErr(w, http.StatusConflict, req.NewName+" already exists")
			return
		}
	}
	if err := os.Rename(src, dst); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.relocateStars(mount.Identity(), req.Path, path.Join(path.Dir(req.Path), req.NewName))
	writeOK(w)
}

// relocateStars moves (or, with newRel "", drops) the stars under oldRel after
// a rename or delete. The file operation has already succeeded by then, so a
// failure to persist is logged rather than reported as the request failing.
func (h *Handler) relocateStars(mount, oldRel, newRel string) {
	if h.Stars == nil {
		return
	}
	if err := h.Stars.relocate(mount, path.Clean(oldRel), newRel); err != nil {
		log.Printf("stars: could not follow %s: %v", oldRel, err)
	}
}

type deleteReq struct {
	Mount string `json:"mount"`
	Path  string `json:"path"`
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	var req deleteReq
	if !decodeBody(w, r, &req) {
		return
	}
	mount, full, ok := h.target(w, req.Mount, req.Path)
	if !ok {
		return
	}
	// Refuse to delete the mount root itself. Judged on the request path, not
	// by comparing full with mount.Path: full is absolute and a configured
	// mount path need not be, so "", "." or "x/.." slipped past that.
	if path.Clean("/"+filepath.ToSlash(req.Path)) == "/" || full == filepath.Clean(mount.Path) {
		writeErr(w, http.StatusBadRequest, "refusing to delete mount root")
		return
	}
	if err := os.RemoveAll(full); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.relocateStars(mount.Identity(), req.Path, "")
	writeOK(w)
}
