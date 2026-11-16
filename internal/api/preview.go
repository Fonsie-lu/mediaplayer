package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"mediaplayer/internal/session"
)

// thumbGen serializes generation per cache path so concurrent requests for
// the same thumbnail don't spawn duplicate ffmpegthumbnailer runs.
var thumbGen sync.Map // cachePath -> *sync.Mutex

const (
	// previewCacheMax caps the preview cache's size on disk. The cache is
	// stable across restarts and nothing else ever removed from it, so it grew
	// by one PNG per video ever looked at — and os.TempDir is commonly a
	// tmpfs, where that is RAM.
	previewCacheMax = 512 << 20
	// previewTouchEvery throttles the mtime refresh a cache hit does: recency
	// only has to be good enough to rank files for eviction, and a held cursor
	// requests the same thumbnail many times a minute.
	previewTouchEvery = time.Hour
)

// thumbCache is the on-disk preview cache: a directory of <key>.png files
// kept under a size cap by evicting the least recently used.
//
// Recency is the file's mtime, which a hit refreshes (see touch) — the files
// themselves are the only record, so the ranking survives a restart like the
// cache does. That is also why preview serves them with an ETag rather than
// through http.ServeFile: a Last-Modified that moved on every hit would never
// revalidate.
type thumbCache struct {
	dir string
	max int64

	mu sync.Mutex
	// size is the cache's size as of the last trim plus everything added
	// since; measured says a trim has run, i.e. that size started from a real
	// count. From then on it only errs high (a file removed behind our back),
	// which costs an early rescan, never an overshoot.
	size     int64
	measured bool
}

// previews is the process-wide cache, under a stable path in TempDir so the
// thumbnails survive restarts (keys are content-derived, so reuse is safe) and
// server starts don't stack up `mediaplayer-previews-XXXX` dirs.
var previews = sync.OnceValue(func() *thumbCache {
	c := &thumbCache{dir: filepath.Join(os.TempDir(), "mediaplayer-previews"), max: previewCacheMax}
	_ = os.MkdirAll(c.dir, 0o755)
	return c
})

// TrimPreviewCache brings the preview cache under its cap. Call once at
// startup: a cache left by an earlier run (or an earlier build, which had no
// cap) is otherwise only measured when the next thumbnail is generated.
func TrimPreviewCache() {
	if freed, n := previews().trim(); n > 0 {
		log.Printf("preview cache: evicted %d thumbnails (%d MB) to stay under %d MB",
			n, freed>>20, int64(previewCacheMax)>>20)
	}
}

// path is where the thumbnail for key lives.
func (c *thumbCache) path(key string) string {
	return filepath.Join(c.dir, key+".png")
}

// touch marks a cached thumbnail as just used, so it is the last to be
// evicted. Best-effort: a thumbnail that can't be touched just ages.
func (c *thumbCache) touch(path string, st os.FileInfo) {
	if time.Since(st.ModTime()) > previewTouchEvery {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
	}
}

// added accounts for a freshly generated thumbnail of n bytes and trims the
// cache when that takes it past the cap.
func (c *thumbCache) added(n int64) {
	c.mu.Lock()
	c.size += n
	due := !c.measured || c.size > c.max
	c.mu.Unlock()
	if due {
		c.trim()
	}
}

// trim measures the cache and, when it is over the cap, removes the least
// recently used thumbnails until it is down to three quarters of it — a
// margin, so a cache sitting at the cap doesn't rescan the directory for
// every thumbnail added. It reports what it removed.
//
// A request that finds its thumbnail evicted between the lookup and the open
// regenerates it (see preview), so trimming needs no coordination with
// readers beyond that.
func (c *thumbCache) trim() (freed int64, removed int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, 0
	}
	type thumb struct {
		path string
		size int64
		used time.Time
	}
	var thumbs []thumb
	var total int64
	for _, e := range entries {
		// Only what the cache itself writes: whatever else sits in the dir is
		// neither counted nor removed.
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".png" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		thumbs = append(thumbs, thumb{filepath.Join(c.dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total > c.max {
		slices.SortFunc(thumbs, func(a, b thumb) int { return a.used.Compare(b.used) })
		for _, t := range thumbs {
			if total <= c.max/4*3 {
				break
			}
			if os.Remove(t.path) == nil {
				total -= t.size
				freed += t.size
				removed++
			}
		}
	}
	c.size, c.measured = total, true
	return freed, removed
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
func (c *thumbCache) ensureThumb(ctx context.Context, input, cachePath string, size int) error {
	if st, err := os.Stat(cachePath); err == nil {
		c.touch(cachePath, st)
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
	scratch, err := session.MkdirTemp(session.ThumbTempPrefix)
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
	st, err := os.Stat(tmpPath)
	if err != nil {
		return err
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		return err
	}
	c.added(st.Size())
	return nil
}

// open returns the cached thumbnail for input, generating it first if needed.
// The second attempt is for a thumbnail the cache evicted between ensureThumb
// finding it and the open — rare (it had to be the least recently used, and
// more than previewTouchEvery stale), but a 500 for it would show as a
// missing preview.
func (c *thumbCache) open(ctx context.Context, input, key string, size int) (*os.File, error) {
	cachePath := c.path(key)
	for attempt := 0; ; attempt++ {
		if err := c.ensureThumb(ctx, input, cachePath, size); err != nil {
			return nil, err
		}
		f, err := os.Open(cachePath)
		if err == nil || attempt > 0 || !errors.Is(err, fs.ErrNotExist) {
			return f, err
		}
	}
}

// previewKey names a thumbnail by what it was made from — source path, the
// source's mtime, the size asked for — so an edited or replaced file misses
// the cache instead of showing a stale frame.
func previewKey(full string, st os.FileInfo, size int) string {
	sum := sha1.Sum([]byte(full + "|" + strconv.FormatInt(st.ModTime().UnixNano(), 10) + "|" + strconv.Itoa(size)))
	return hex.EncodeToString(sum[:])
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
	key := previewKey(full, st, size)
	f, err := previews().open(r.Context(), full, key, size)
	if err != nil {
		if r.Context().Err() != nil {
			return // client gone (the cursor moved on); nobody to answer
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	// The key names the content (source path, its mtime, the size asked for),
	// so it is the validator. Not the cache file's mtime, which is the
	// eviction clock and moves on hits.
	w.Header().Set("ETag", `"`+key+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}
