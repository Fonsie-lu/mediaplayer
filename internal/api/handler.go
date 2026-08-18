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

// Register wires every endpoint. The patterns carry their method, so the mux
// rejects the wrong one (405 + Allow) before a handler runs and no handler
// checks r.Method itself. That only holds while nothing in the server
// registers a catch-all pattern — see the routing comment in main.go.
//
// Note a "GET" pattern also matches HEAD, so every route below answers HEAD by
// running its handler: harmless (and required by http.ServeFile) on the
// read-only routes, but /api/stream/open adopts a session as a side effect, so
// a HEAD of it is not free. Moving that one to POST is the tidier fix and
// would break clients running a cached copy of the old api.js.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mounts", h.getMounts)
	mux.HandleFunc("GET /api/browse", h.browse)
	mux.HandleFunc("POST /api/rename", h.rename)
	mux.HandleFunc("POST /api/delete", h.del)
	mux.HandleFunc("GET /api/preview", h.preview)
	mux.HandleFunc("GET /api/disk", h.disk)
	mux.HandleFunc("GET /api/sheet", h.sheet)
	mux.HandleFunc("GET /api/probe", h.probe)
	mux.HandleFunc("GET /api/stream/direct", h.streamDirect)
	mux.HandleFunc("GET /api/stream/open", h.streamOpen)
	mux.HandleFunc("POST /api/stream/close", h.streamClose)
	// Wildcards, so the sid and the filename arrive parsed: {file} can never
	// contain a slash, and the mux resolves any dot segments before matching.
	mux.HandleFunc("GET /api/stream/hls/{sid}/{file}", h.streamHLS)
	mux.HandleFunc("GET /api/config", h.getConfig)
	mux.HandleFunc("POST /api/config", h.postConfig)
	mux.HandleFunc("GET /api/stars", h.getStars)
	mux.HandleFunc("POST /api/stars/toggle", h.toggleStar)
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

// decodeBody decodes the JSON request body into v, writing the error response
// itself. Callers must return immediately when ok is false. The method is the
// mux's business (see Register), so this only handles the body.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) (ok bool) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}
