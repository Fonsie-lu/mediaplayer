package api

import (
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediaplayer/internal/session"
	"mediaplayer/internal/transcode"
)

func (h *Handler) probe(w http.ResponseWriter, r *http.Request) {
	_, full, ok := h.queryTarget(w, r)
	if !ok {
		return
	}
	if _, err := os.Stat(full); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	res, err := transcode.Probe(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) streamDirect(w http.ResponseWriter, r *http.Request) {
	_, full, ok := h.queryTarget(w, r)
	if !ok {
		return
	}
	// No session involved: the browser fetches the file itself. (This used to
	// close "the client's" HLS session, which with one cookie per browser was
	// any other tab's.)
	switch strings.ToLower(filepath.Ext(full)) {
	case ".mkv":
		w.Header().Set("Content-Type", "video/x-matroska")
	case ".ts", ".m2ts", ".mts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".webm":
		w.Header().Set("Content-Type", "video/webm")
	case ".mp4", ".m4v":
		w.Header().Set("Content-Type", "video/mp4")
	}
	http.ServeFile(w, r, full)
}

// mpegts-embeddable audio codecs browsers can decode — eligible for -c:a copy.
var copyableAudio = map[string]bool{"aac": true, "mp3": true}

// remuxMaxSegment caps segment length in remux mode. Sources with very
// sparse keyframes would otherwise produce huge segments (slow seeks, big
// tmpfs spikes), so those fall back to a re-encode with forced keyframes.
const remuxMaxSegment = 30.0

// qualityHeights maps the player's quality selector to a height cap. Anything
// else ("", "auto", "source") means the source resolution.
var qualityHeights = map[string]int{"1080": 1080, "720": 720, "480": 480}

// streamPlan is how a session will produce its segments: the whole
// remux-vs-transcode decision, as a value.
type streamPlan struct {
	MaxHeight  int
	AudioIdx   int
	CopyAudio  bool
	CopyVideo  bool      // remux mode
	Boundaries []float64 // remux segment boundaries, from the source origin
}

func (p streamPlan) mode() string {
	if p.CopyVideo {
		return "remux"
	}
	return "transcode"
}

// planStream decides how to stream probe at quality q with the requested audio
// track (audio-stream-relative, as the query string gave it; anything invalid
// means the probed preference). keyframes supplies the source's keyframe times
// measured from its origin, and is only called when remux is on the table —
// it is the expensive part (a whole-file scan), which is also why it is a
// parameter: everything else here is pure and tested without ffprobe.
//
// Remux needs video every browser decodes (8-bit 4:2:0 h264 — Hi10P shares
// the codec name and decodes nowhere), no height cap, and a keyframe layout
// that splits into segments of at most remuxMaxSegment. Anything else is
// transcoded.
func planStream(probe *transcode.ProbeResult, q, audio string, keyframes func() ([]float64, error)) streamPlan {
	p := streamPlan{MaxHeight: qualityHeights[q], AudioIdx: probe.PreferredAudio}
	if a, err := strconv.Atoi(audio); err == nil && a >= 0 && a < len(probe.AudioTracks) {
		p.AudioIdx = a
	}
	if p.AudioIdx < len(probe.AudioTracks) {
		p.CopyAudio = copyableAudio[probe.AudioTracks[p.AudioIdx].Codec]
	}
	if probe.VCodec != "h264" || !probe.BrowserDecodableVideo() || p.MaxHeight != 0 {
		return p
	}
	kfs, err := keyframes()
	if err != nil || len(kfs) == 0 {
		return p // unscannable — re-encode instead
	}
	b := transcode.BuildBoundaries(kfs, probe.Duration, session.SegDuration)
	if transcode.MaxGap(b) > remuxMaxSegment {
		return p // keyframes too sparse to split on
	}
	p.CopyVideo, p.Boundaries = true, b
	return p
}

// streamOpen registers a transcode session. No ffmpeg yet — segments are
// produced on demand by streamHLS, so the client sees the full timeline
// immediately and can seek anywhere.
//
// Query: mount, path, q (quality: source/1080/720/480), audio
// (audio-stream-relative track index; defaults to the probed preference).
// The legacy `t` (start seconds) is accepted for backward compat but
// ignored: with VOD playlists the client seeks via standard HLS, not via a
// re-spawn at offset. See planStream for the remux-vs-transcode decision.
func (h *Handler) streamOpen(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	rel, q := query.Get("path"), query.Get("q")
	_, full, ok := h.queryTarget(w, r)
	if !ok {
		return
	}
	probe, err := transcode.Probe(full)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "probe failed: "+err.Error())
		return
	}
	if probe.Duration <= 0 {
		writeErr(w, http.StatusInternalServerError, "could not determine duration")
		return
	}
	plan := planStream(probe, q, query.Get("audio"), func() ([]float64, error) {
		return transcode.KeyframeTimes(full, probe.StartTime)
	})

	// A keyframe scan can take minutes. Nobody would ever learn the ID of a
	// session registered for an open the client has already abandoned (a
	// quality switch, another video), so it would only hold a dir until the
	// reaper found it.
	if r.Context().Err() != nil {
		return
	}
	dir, err := os.MkdirTemp("", session.SessTempPrefix)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess := &session.Session{
		Input:       full,
		Duration:    probe.Duration,
		MaxHeight:   plan.MaxHeight,
		AudioIdx:    plan.AudioIdx,
		Dir:         dir,
		Origin:      probe.StartTime,
		Deinterlace: probe.Interlaced,
		CopyVideo:   plan.CopyVideo,
		CopyAudio:   plan.CopyAudio,
		Boundaries:  plan.Boundaries,
	}
	sid, err := h.Sessions.Add(sess)
	if err != nil {
		_ = os.RemoveAll(dir)
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	log.Printf("[session %s] opened path=%s dur=%.1fs q=%s audio=%d mode=%s acopy=%t deint=%t origin=%.3f segs=%d dir=%s",
		sid, rel, probe.Duration, q, plan.AudioIdx, plan.mode(), plan.CopyAudio, sess.Deinterlace && !plan.CopyVideo,
		probe.StartTime, sess.NumSegments(), dir)
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sid,
		"playlist": "/api/stream/hls/" + sid + "/playlist.m3u8",
		"duration": probe.Duration,
		"mode":     plan.mode(),
	})
}

// streamClose ends the session named by ?session=. The player sends it as a
// beacon on its way out of a stream, so the reply is never read; an unknown
// ID — already reaped, or closed twice — is not an error.
func (h *Handler) streamClose(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session")
	if sid == "" {
		writeErr(w, http.StatusBadRequest, "missing session")
		return
	}
	if h.Sessions.Close(sid) {
		log.Printf("[session %s] closed", sid)
	}
	writeOK(w)
}

// streamHLS handles both the playlist and the segments under
// /api/stream/hls/{sid}/{file}. The playlist is generated from session
// metadata; segments are transcoded on demand in ~1 min batches.
//
// file needs no path sanitising: it is one path segment (the mux's {file}
// wildcard cannot span a slash) and it is never joined onto anything — either
// it equals the playlist name, or ParseSegName validates it down to an int and
// the segment path is rebuilt from that.
func (h *Handler) streamHLS(w http.ResponseWriter, r *http.Request) {
	sid, file := r.PathValue("sid"), r.PathValue("file")
	sess, ok := h.Sessions.Get(sid)
	if !ok {
		writeErr(w, http.StatusNotFound, "no session")
		return
	}
	sess.Touch()

	if file == "playlist.m3u8" {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(sess.PlaylistText()))
		return
	}

	n, ok := transcode.ParseSegName(file)
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad segment name")
		return
	}
	f, err := sess.EnsureSegment(r.Context(), n)
	if err != nil {
		switch {
		case r.Context().Err() != nil:
			// Client aborted the request (hls.js cancels segment loads on
			// seeks); nobody is listening for a response.
		case errors.Is(err, session.ErrSessionClosed):
			// Closed while this request waited: the same answer as a session
			// that was never there, which the player meets by reopening at
			// once (a 5xx would first sit through hls.js's retry backoff).
			writeErr(w, http.StatusNotFound, "no session")
		case errors.Is(err, session.ErrSegmentTimeout):
			writeErr(w, http.StatusGatewayTimeout, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// Served from the handle EnsureSegment opened under the session lock:
	// another request's batch start can evict or clear the name at any moment
	// after that, and an open file survives it.
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	// Scratch files gone from the server within minutes: a film's worth would
	// only fill the browser's disk cache.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, file, st.ModTime(), f)
}
