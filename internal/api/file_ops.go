package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type renameReq struct {
	Mount   string `json:"mount"`
	Path    string `json:"path"`
	NewName string `json:"new_name"`
}

func (h *Handler) rename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req renameReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
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
	if err := os.Rename(src, dst); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type deleteReq struct {
	Mount string `json:"mount"`
	Path  string `json:"path"`
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req deleteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	mount, full, ok := h.target(w, req.Mount, req.Path)
	if !ok {
		return
	}
	// refuse to delete the mount root itself
	if full == filepath.Clean(mount.Path) {
		writeErr(w, http.StatusBadRequest, "refusing to delete mount root")
		return
	}
	if err := os.RemoveAll(full); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
