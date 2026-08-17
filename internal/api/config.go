package api

import (
	"net/http"

	"mediaplayer/internal/config"
)

type configPayload struct {
	Mounts []config.Mount `json:"mounts"`
}

func (h *Handler) configRW(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snap := h.Cfg.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{
			"host":   snap.Host,
			"port":   snap.Port,
			"disk":   snap.Disk,
			"mounts": snap.Mounts,
		})
	case http.MethodPost:
		var p configPayload
		if !decodePost(w, r, &p) {
			return
		}
		if err := h.Cfg.Replace(p.Mounts); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeOK(w)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}
