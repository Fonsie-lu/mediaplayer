package api

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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

// PreviewDir returns the cache dir, or "" if it hasn't been created yet.
func PreviewDir() string { return previewDir }

// ensureThumb generates the thumbnail at cachePath if missing. Concurrent
// callers for the same path block on one generation instead of racing.
func ensureThumb(input, cachePath string, size int) error {
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
	cmd := exec.Command("ffmpegthumbnailer",
		"-i", input,
		"-o", cachePath,
		"-s", strconv.Itoa(size),
		"-q", "8",
		"-c", "png",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("thumbnail failed: %s", out)
	}
	return nil
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
	if err := ensureThumb(full, cachePath, size); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, cachePath)
}
