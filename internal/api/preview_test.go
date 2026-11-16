package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"mediaplayer/internal/config"
)

// seedThumb writes an n-byte cache file last used `age` ago.
func seedThumb(t *testing.T, c *thumbCache, key string, n int, age time.Duration) string {
	t.Helper()
	p := c.path(key)
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

func cached(t *testing.T, c *thumbCache) []string {
	t.Helper()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// Over the cap, the least recently used thumbnails go first, down to three
// quarters of the cap; nothing the cache didn't write is counted or removed.
func TestThumbCacheTrimEvictsLeastRecentlyUsed(t *testing.T) {
	c := &thumbCache{dir: t.TempDir(), max: 600}
	// Ten 100-byte thumbnails, t0 the oldest. Seeded out of age order so the
	// directory's own ordering can't pass for the mtime sort.
	for _, i := range []int{4, 9, 0, 7, 2, 5, 1, 8, 3, 6} {
		seedThumb(t, c, "t"+string(rune('0'+i)), 100, time.Duration(20-i)*time.Hour)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "notes.txt"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(c.dir, "sub.png"), 0o755); err != nil {
		t.Fatal(err)
	}

	freed, removed := c.trim()
	// 1000 bytes against a 600 cap: evict to <= 450, so six go.
	if freed != 600 || removed != 6 {
		t.Errorf("trim freed %d bytes in %d files, want 600 in 6", freed, removed)
	}
	want := []string{"notes.txt", "sub.png", "t6.png", "t7.png", "t8.png", "t9.png"}
	if got := cached(t, c); !slices.Equal(got, want) {
		t.Errorf("left in cache: %v, want %v", got, want)
	}
	if c.size != 400 {
		t.Errorf("accounted size %d, want 400", c.size)
	}
}

func TestThumbCacheTrimLeavesACacheUnderTheCap(t *testing.T) {
	c := &thumbCache{dir: t.TempDir(), max: 600}
	for i := range 6 {
		seedThumb(t, c, "t"+string(rune('0'+i)), 100, time.Duration(i)*time.Hour)
	}
	if freed, removed := c.trim(); freed != 0 || removed != 0 {
		t.Errorf("trim at exactly the cap freed %d bytes in %d files, want nothing", freed, removed)
	}
	if n := len(cached(t, c)); n != 6 {
		t.Errorf("%d thumbnails left, want all 6", n)
	}
}

// A hit refreshes a stale mtime, so a thumbnail that is still being looked at
// outlives ones generated after it; the refresh is throttled.
func TestThumbCacheHitKeepsAThumbnailFromEviction(t *testing.T) {
	c := &thumbCache{dir: t.TempDir(), max: 250}
	old := seedThumb(t, c, "old-but-used", 100, 48*time.Hour)
	seedThumb(t, c, "newer-unused", 100, 24*time.Hour)
	recent := seedThumb(t, c, "recent", 100, 10*time.Minute)
	recentBefore, _ := os.Stat(recent)

	// Both exist, so neither call reaches ffmpegthumbnailer.
	for _, p := range []string{old, recent} {
		if err := c.ensureThumb(context.Background(), "unused", p, 300); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := os.Stat(old); time.Since(st.ModTime()) > time.Minute {
		t.Errorf("hit left the stale mtime at %s", st.ModTime())
	}
	if st, _ := os.Stat(recent); !st.ModTime().Equal(recentBefore.ModTime()) {
		t.Error("hit rewrote an mtime younger than previewTouchEvery")
	}

	c.trim() // 300 > 250: down to <= 187, i.e. one thumbnail
	if got := cached(t, c); !slices.Equal(got, []string{"old-but-used.png"}) {
		t.Errorf("left in cache: %v, want only the thumbnail that was just used", got)
	}
}

// Generating a thumbnail is what grows the cache, so that is where the cap is
// enforced while the server runs — including the first time, when the cache
// has not been measured yet.
func TestThumbCacheAddedEnforcesTheCap(t *testing.T) {
	c := &thumbCache{dir: t.TempDir(), max: 250}
	seedThumb(t, c, "a", 100, 3*time.Hour)
	seedThumb(t, c, "b", 100, 2*time.Hour)
	seedThumb(t, c, "c", 100, 0)
	c.added(100) // accounted 100, real 300: only a measurement finds that out
	if got := cached(t, c); !slices.Equal(got, []string{"c.png"}) {
		t.Errorf("left in cache: %v, want only the newest", got)
	}

	seedThumb(t, c, "d", 100, 0)
	c.added(100) // 200: under the cap, nothing to do
	if n := len(cached(t, c)); n != 2 {
		t.Errorf("%d thumbnails left after an add under the cap, want 2", n)
	}
}

// The reply is validated by an ETag naming the content, not by the cache
// file's mtime — that is the eviction clock, and moves.
func TestPreviewRevalidatesByETag(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "v.mkv")
	if err := os.WriteFile(video, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &thumbCache{dir: t.TempDir(), max: 1 << 20}
	orig := previews
	previews = func() *thumbCache { return c }
	t.Cleanup(func() { previews = orig })

	st, err := os.Stat(video)
	if err != nil {
		t.Fatal(err)
	}
	// Already cached, and stale enough that the request will touch it.
	seedThumb(t, c, previewKey(video, st, 300), 64, 3*time.Hour)

	h := &Handler{Cfg: config.New(config.Snapshot{Mounts: []config.Mount{{Name: "m", Path: root}}})}
	get := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/preview?mount=0&path=v.mkv", nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		h.preview(rec, req)
		return rec
	}

	first := get("")
	etag := first.Header().Get("ETag")
	if first.Code != http.StatusOK || first.Body.Len() != 64 || etag == "" {
		t.Fatalf("first request: status %d, %d bytes, ETag %q", first.Code, first.Body.Len(), etag)
	}
	if lm := first.Header().Get("Last-Modified"); lm != "" {
		t.Errorf("Last-Modified %q sent: the cache file's mtime is not a validator", lm)
	}
	if again := get(etag); again.Code != http.StatusNotModified {
		t.Errorf("revalidation after the hit moved the mtime: status %d, want 304", again.Code)
	}
}
