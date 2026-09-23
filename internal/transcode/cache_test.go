package transcode

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLRUEvictsOldestNotEverything(t *testing.T) {
	// The whole point of replacing the flush-all map: filling past the cap must
	// cost one entry, not the entire warm working set.
	c := newLRU[int](3)
	for i := 0; i < 3; i++ {
		c.put("k"+strconv.Itoa(i), i)
	}
	c.put("k3", 3) // evicts k0

	if c.len() != 3 {
		t.Errorf("len = %d, want the cap 3", c.len())
	}
	if _, ok := c.get("k0"); ok {
		t.Error("k0 survived; it was least recently used")
	}
	for _, k := range []string{"k1", "k2", "k3"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s was evicted; only the oldest should have been", k)
		}
	}
}

func TestLRUGetPromotes(t *testing.T) {
	c := newLRU[string](2)
	c.put("a", "A")
	c.put("b", "B")
	// Touching "a" makes "b" the eviction candidate.
	if _, ok := c.get("a"); !ok {
		t.Fatal("a missing")
	}
	c.put("c", "C")

	if _, ok := c.get("a"); !ok {
		t.Error("a evicted despite being used most recently")
	}
	if _, ok := c.get("b"); ok {
		t.Error("b survived despite being least recently used")
	}
}

func TestLRUPutRefreshesExisting(t *testing.T) {
	c := newLRU[int](2)
	c.put("a", 1)
	c.put("a", 2)
	if got, _ := c.get("a"); got != 2 {
		t.Errorf("get(a) = %d, want the refreshed 2", got)
	}
	if c.len() != 1 {
		t.Errorf("len = %d, want 1 — a re-put must not add an entry", c.len())
	}
}

// An unstattable file yields an empty key, which must be neither stored nor
// matched — otherwise every such file would share one cache slot.
func TestLRUIgnoresEmptyKey(t *testing.T) {
	c := newLRU[int](2)
	c.put("", 1)
	if c.len() != 0 {
		t.Errorf("len = %d, want the empty key dropped", c.len())
	}
	if _, ok := c.get(""); ok {
		t.Error("get(\"\") reported a hit")
	}
}

func TestLRUCapacityFloor(t *testing.T) {
	// A zero or negative cap would make put loop forever or store nothing
	// useful; it clamps to 1.
	c := newLRU[int](0)
	c.put("a", 1)
	if c.len() != 1 {
		t.Errorf("len = %d, want 1", c.len())
	}
}

// Both caches are package-level and shared by concurrent requests, so the
// locking has to hold up under -race.
func TestLRUConcurrent(t *testing.T) {
	c := newLRU[int](32)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				k := "k" + strconv.Itoa((n*j)%64)
				c.put(k, j)
				c.get(k)
				c.len()
			}
		}(i)
	}
	wg.Wait()
	if c.len() > 32 {
		t.Errorf("len = %d, want the cap respected under concurrency", c.len())
	}
}

func TestStatKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.mkv")
	if err := os.WriteFile(path, []byte("aa"), 0o644); err != nil {
		t.Fatal(err)
	}

	k1 := statKey(path)
	if k1 == "" {
		t.Fatal("statKey on an existing file returned empty")
	}
	if statKey(path) != k1 {
		t.Error("statKey is not stable for an unchanged file")
	}

	// A file whose size changed must not hit the old entry — that is the whole
	// reason size and mtime are in the key.
	if err := os.WriteFile(path, []byte("aaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if statKey(path) == k1 {
		t.Error("statKey unchanged after the file was rewritten")
	}

	if got := statKey(filepath.Join(dir, "missing.mkv")); got != "" {
		t.Errorf("statKey(missing) = %q, want empty so it isn't cached", got)
	}
}

func (c *lru[V]) inflightLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.inflight)
}

// Concurrent loads of one key are one fn call: the player's probe and its
// stream-open land together, and a duplicated keyframe scan reads the whole
// file twice.
func TestLRULoadSharesInflightCall(t *testing.T) {
	c := newLRU[int](4)
	release := make(chan struct{})
	var calls atomic.Int32
	fn := func() (int, error) {
		calls.Add(1)
		<-release
		return 42, nil
	}
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.load("k", fn)
			if err != nil {
				t.Error(err)
			}
			results[i] = v
		}()
	}
	// Let every goroutine reach load before the one call returns.
	for c.inflightLen() == 0 {
		runtime.Gosched()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Errorf("fn ran %d times, want 1", got)
	}
	for i, v := range results {
		if v != 42 {
			t.Errorf("caller %d got %d, want the shared 42", i, v)
		}
	}
	if v, ok := c.get("k"); !ok || v != 42 {
		t.Error("loaded value not cached")
	}
}

// A failed load reaches its callers but is not cached: the next call retries.
func TestLRULoadDoesNotCacheErrors(t *testing.T) {
	c := newLRU[int](4)
	boom := errors.New("boom")
	if _, err := c.load("k", func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	v, err := c.load("k", func() (int, error) { return 7, nil })
	if err != nil || v != 7 {
		t.Errorf("retry = %d, %v; want 7", v, err)
	}
}

// KeyframeTimes rebases the cached raw PTS onto the caller's origin, and hands
// out a copy — the cache is shared, and the raw values must survive.
func TestKeyframeTimesRebasesOntoOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.ts")
	if err := os.WriteFile(path, []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyframeCache.put(statKey(path), []float64{5001.4, 5003.4, 5005.4})

	got, err := KeyframeTimes(path, 5001.4)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0, 2, 4}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("KeyframeTimes = %v, want %v", got, want)
		}
	}
	got[0] = -1
	if again, _ := KeyframeTimes(path, 0); again[0] != 5001.4 {
		t.Errorf("caller's edit reached the cache: %v", again)
	}
}

// A panicking load must reach its caller as a panic and leave nothing cached:
// before the completed flag, the deferred cleanup stored the zero value as a
// successful result.
func TestLRULoadPanicNotCached(t *testing.T) {
	c := newLRU[*int](4)
	func() {
		defer func() { _ = recover() }()
		_, _ = c.load("k", func() (*int, error) { panic("boom") })
	}()
	if _, ok := c.get("k"); ok {
		t.Fatal("a panicked load was cached")
	}
	v := 3
	got, err := c.load("k", func() (*int, error) { return &v, nil })
	if err != nil || got == nil || *got != 3 {
		t.Errorf("reload after panic = %v, %v; want 3", got, err)
	}
}
