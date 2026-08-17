package transcode

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
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
