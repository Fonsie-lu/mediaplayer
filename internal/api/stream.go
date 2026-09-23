package api

import (
	"crypto/rand"
	"encoding/hex"
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

const sessionCookie = "mp_sid"

func (h *Handler) sid(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	id := hex.EncodeToString(buf[:])
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return id
}

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
	// On open, wipe any prior transcode session for this client.
	sid := h.sid(w, r)
	h.Sessions.Close(sid)
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

	// Sessions are keyed by cookie and adopted in the order opens *finish*, and
	// a keyframe scan can take minutes. An open whose client has already moved
	// on (a quality switch, another video) must not land after the newer one
	// and replace the session that page is playing.
	if r.Context().Err() != nil {
		return
	}
	sid := h.sid(w, r)
	dir, err := os.MkdirTemp("", session.SessTempPrefix)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess := &session.Session{
		ID:          sid,
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
	h.Sessions.Adopt(sid, sess)
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

func (h *Handler) streamClose(w http.ResponseWriter, r *http.Request) {
	sid := h.sid(w, r)
	h.Sessions.Close(sid)
	log.Printf("[session %s] closed", sid)
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
	path, err := sess.EnsureSegment(r.Context(), n)
	if err != nil {
		if r.Context().Err() != nil {
			// Client aborted the request (hls.js cancels segment loads on
			// seeks); nobody is listening for a response.
			return
		}
		if errors.Is(err, session.ErrSegmentTimeout) {
			writeErr(w, http.StatusGatewayTimeout, err.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	// Opened here rather than by path inside ServeFile: another request's batch
	// start can evict or clear this segment at any moment (hls.js backtracking
	// to n-1 clears n.. in remux mode). An open file survives its deletion; a
	// file already gone is a 503, which hls.js retries — a 404 would read as
	// "session lost" to the player and reopen the whole stream.
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "segment was replaced, retry")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	// The URL is per cookie, not per video or per session instance: the same
	// seg_00042.ts names a different file after a quality or audio switch, or
	// on the next video. A cached copy — a Last-Modified invites heuristic
	// caching — would splice the old one in.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, file, st.ModTime(), f)
}
