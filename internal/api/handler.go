package api

import (
	"encoding/json"
	"log"
	"net/http"

	"mediaplayer/internal/config"
	"mediaplayer/internal/session"
)

type Handler struct {
	Cfg      *config.Config
	Sessions *session.Manager
	Stars    *StarStore
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/mounts", h.getMounts)
	mux.HandleFunc("/api/browse", h.browse)
	mux.HandleFunc("/api/rename", h.rename)
	mux.HandleFunc("/api/delete", h.del)
	mux.HandleFunc("/api/preview", h.preview)
	mux.HandleFunc("/api/disk", h.disk)
	mux.HandleFunc("/api/sheet", h.sheet)
	mux.HandleFunc("/api/probe", h.probe)
	mux.HandleFunc("/api/stream/direct", h.streamDirect)
	mux.HandleFunc("/api/stream/open", h.streamOpen)
	mux.HandleFunc("/api/stream/close", h.streamClose)
	mux.HandleFunc("/api/stream/hls/", h.streamHLS) // /api/stream/hls/{sid}/{file}
	mux.HandleFunc("/api/config", h.configRW)
	mux.HandleFunc("/api/stars", h.getStars)
	mux.HandleFunc("/api/stars/toggle", h.toggleStar)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeOK is the bare acknowledgement the mutating endpoints share.
func writeOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// decodePost enforces POST and decodes the JSON body into v, writing the error
// response itself. Callers must return immediately when ok is false.
func decodePost(w http.ResponseWriter, r *http.Request, v any) (ok bool) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}
