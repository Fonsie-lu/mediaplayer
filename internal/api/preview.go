package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"mediaplayer/internal/session"
)

var (
	previewDir     string
	previewDirOnce sync.Once
	// thumbGen serializes generation per cache path so concurrent requests
	// for the same thumbnail don't spawn duplicate ffmpegthumbnailer runs.
	thumbGen sync.Map // cachePath -> *sync.Mutex
)

// ensurePreviewDir uses a stable path under TempDir so previews survive
// restarts (cache keys are content-hashed, so reuse is safe) and we don't
// stack up `mediaplayer-previews-XXXX` dirs on every server start.
func ensurePreviewDir() string {
	previewDirOnce.Do(func() {
		previewDir = filepath.Join(os.TempDir(), "mediaplayer-previews")
		_ = os.MkdirAll(previewDir, 0755)
	})
	return previewDir
}

// runThumbnailer is the single place that knows ffmpegthumbnailer's CLI: both
// the cached previews here and the per-request sheet frames go through it, so
// the flags and the "errors come back on combined stdout+stderr" behaviour
// can't drift apart. at is an hh:mm:ss seek, or "" for the tool's own pick —
// a bare number would be read as a *percentage* of the runtime.
func runThumbnailer(ctx context.Context, input, out, format string, size int, at string) error {
	args := []string{
		"-i", input,
		"-o", out,
		"-s", strconv.Itoa(size),
		"-q", "8",
		"-c", format,
	}
	if at != "" {
		args = append(args, "-t", at)
	}
	if b, err := exec.CommandContext(ctx, "ffmpegthumbnailer", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s", bytes.TrimSpace(b))
	}
	return nil
}

// thumbSlots bounds concurrent preview generations. A cursor held down a
// folder of uncached videos asks for one per row, and unbounded that was
// dozens of decoders at once on the server — while the browser's six
// connections per host sat behind them, stalling the listing calls too.
var thumbSlots = make(chan struct{}, 4)

// thumbTimeout bounds one generation: a hung run on a stalled mount would
// otherwise hold its slot, and the per-path lock every later request for
// that thumbnail queues on, forever.
const thumbTimeout = time.Minute

// ensureThumb generates the thumbnail at cachePath if missing. Concurrent
// callers for the same path block on one generation instead of racing. ctx
// only bounds the wait for a free slot — a client that leaves before its turn
// costs nothing; once a run has started it is not tied to the request.
func ensureThumb(ctx context.Context, input, cachePath string, size int) error {
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}
	muAny, _ := thumbGen.LoadOrStore(cachePath, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer func() {
		mu.Unlock()
		thumbGen.Delete(cachePath)
	}()
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}
	select {
	case thumbSlots <- struct{}{}:
		defer func() { <-thumbSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// Generated in a scratch dir and renamed into place, so the cache only
	// ever holds whole files: it is stable across restarts, and a run cut
	// short by a crash or kill used to leave a truncated PNG that every later
	// request treated as a hit. The scratch dir carries ThumbTempPrefix, so
	// the one a crash leaves behind is swept at startup — a temp file beside
	// the cache would stay forever. Both live under os.TempDir, so the rename
	// stays on one filesystem.
	scratch, err := os.MkdirTemp("", session.ThumbTempPrefix)
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	tmpPath := filepath.Join(scratch, "thumb.png")
	// Deliberately not the request's context: other requests for the same
	// thumbnail are queued on this generation, and a client that went away
	// shouldn't fail theirs.
	runCtx, cancel := context.WithTimeout(context.Background(), thumbTimeout)
	defer cancel()
	if err := runThumbnailer(runCtx, input, tmpPath, "png", size, ""); err != nil {
		return fmt.Errorf("thumbnail failed: %w", err)
	}
	return os.Rename(tmpPath, cachePath)
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	size, _ := strconv.Atoi(r.URL.Query().Get("size"))
	if size <= 0 || size > 1024 {
		size = 300
	}
	_, full, ok := h.queryTarget(w, r)
	if !ok {
		return
	}
	st, err := os.Stat(full)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	// cache key: path + mtime + size
	key := sha1.Sum([]byte(full + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10) + "|" + strconv.Itoa(size)))
	cachePath := filepath.Join(ensurePreviewDir(), hex.EncodeToString(key[:])+".png")
	if err := ensureThumb(r.Context(), full, cachePath, size); err != nil {
		if r.Context().Err() != nil {
			return // client gone (the cursor moved on); nobody to answer
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, cachePath)
}
