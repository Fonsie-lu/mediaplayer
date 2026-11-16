package api

import (
	"log"
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
	before := h.Cfg.Snapshot().Mounts
	if err := h.Cfg.Replace(p.Mounts); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The mounts are replaced by then, so a failure to move the stars along is
	// logged rather than reported as the request failing (as in relocateStars).
	if h.Stars != nil {
		if err := h.Stars.FollowMounts(before, h.Cfg.Snapshot().Mounts); err != nil {
			log.Printf("stars: could not follow renamed mounts: %v", err)
		}
	}
	writeOK(w)
}
