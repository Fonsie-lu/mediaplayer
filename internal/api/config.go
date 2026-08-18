package api

import (
	"net/http"

	"mediaplayer/internal/config"
)

type configPayload struct {
	Mounts []config.Mount `json:"mounts"`
}

// getConfig reports the whole config. config.Snapshot carries the on-disk JSON
// tags, so it is already the wire shape — a hand-built map here would just be a
// second field list to keep in sync.
func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.Cfg.Snapshot())
}

// postConfig replaces the mount list, live and persisted.
func (h *Handler) postConfig(w http.ResponseWriter, r *http.Request) {
	var p configPayload
	if !decodeBody(w, r, &p) {
		return
	}
	if err := h.Cfg.Replace(p.Mounts); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}
