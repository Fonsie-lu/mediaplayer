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

// streamOpen registers a transcode session. No ffmpeg yet — segments are
// produced on demand by streamHLS, so the client sees the full timeline
// immediately and can seek anywhere.
//
// Query: mount, path, q (quality: source/1080/720/480), audio
// (audio-stream-relative track index; defaults to the probed preference).
// The legacy `t` (start seconds) is accepted for backward compat but
// ignored: with VOD playlists the client seeks via standard HLS, not via a
// re-spawn at offset.
//
// When the source video is h264 and no quality cap is requested, the
// session runs in remux mode: video is stream-copied (bit-identical to the
// source) and segments split on the source's own keyframes.
func (h *Handler) streamOpen(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	q := r.URL.Query().Get("q")
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
	maxH := 0
	switch q {
	case "1080":
		maxH = 1080
	case "720":
		maxH = 720
	case "480":
		maxH = 480
	}

	audioIdx := probe.PreferredAudio
	if a, err := strconv.Atoi(r.URL.Query().Get("audio")); err == nil && a >= 0 && a < len(probe.AudioTracks) {
		audioIdx = a
	}
	copyAudio := false
	if audioIdx < len(probe.AudioTracks) {
		copyAudio = copyableAudio[probe.AudioTracks[audioIdx].Codec]
	}

	copyVideo := probe.VCodec == "h264" && maxH == 0
	var bounds []float64
	if copyVideo {
		kfs, err := transcode.KeyframeTimes(full)
		if err == nil && len(kfs) > 0 {
			b := transcode.BuildBoundaries(kfs, probe.Duration, session.SegDuration)
			if transcode.MaxGap(b) <= remuxMaxSegment {
				bounds = b
			}
		}
		if bounds == nil {
			copyVideo = false // unusable keyframe layout — re-encode instead
		}
	}

	sid := h.sid(w, r)
	dir, err := os.MkdirTemp("", "mediaplayer-sess-")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess := &session.Session{
		ID:         sid,
		Input:      full,
		Duration:   probe.Duration,
		MaxHeight:  maxH,
		AudioIdx:   audioIdx,
		Dir:        dir,
		CopyVideo:  copyVideo,
		CopyAudio:  copyAudio,
		Boundaries: bounds,
	}
	h.Sessions.Adopt(sid, sess)
	mode := "transcode"
	if copyVideo {
		mode = "remux"
	}
	log.Printf("[session %s] opened path=%s dur=%.1fs q=%s audio=%d mode=%s acopy=%t segs=%d dir=%s",
		sid, rel, probe.Duration, q, audioIdx, mode, copyAudio, sess.NumSegments(), dir)
	writeJSON(w, http.StatusOK, map[string]any{
		"session":  sid,
		"playlist": "/api/stream/hls/" + sid + "/playlist.m3u8",
		"duration": probe.Duration,
		"mode":     mode,
	})
}

func (h *Handler) streamClose(w http.ResponseWriter, r *http.Request) {
	sid := h.sid(w, r)
	h.Sessions.Close(sid)
	log.Printf("[session %s] closed", sid)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// streamHLS handles both the playlist and the segments under
// /api/stream/hls/{sid}/{file}. The playlist is generated from session
// metadata; segments are transcoded on demand in ~1 min batches.
func (h *Handler) streamHLS(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/stream/hls/"
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
	sid, file := parts[0], parts[1]
	if strings.Contains(file, "..") || strings.Contains(file, "/") {
		writeErr(w, http.StatusBadRequest, "bad path")
		return
	}
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

	if !strings.HasPrefix(file, "seg_") || !strings.HasSuffix(file, ".ts") {
		writeErr(w, http.StatusBadRequest, "bad file")
		return
	}
	idxStr := strings.TrimSuffix(strings.TrimPrefix(file, "seg_"), ".ts")
	n, err := strconv.Atoi(idxStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad segment index")
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
	w.Header().Set("Content-Type", "video/mp2t")
	http.ServeFile(w, r, path)
}
