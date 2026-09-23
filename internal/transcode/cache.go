package transcode

import (
	"container/list"
	"errors"
	"os"
	"strconv"
	"sync"
)

// statKey identifies a file by path + size + mtime, so an edited or replaced
// file misses the cache instead of serving a stale answer. Empty when the file
// can't be stat'ed, which callers treat as "don't cache".
func statKey(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return path + "|" + strconv.FormatInt(st.Size(), 10) + "|" +
		strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// lru is a fixed-capacity, least-recently-used cache. Both ffprobe results and
// keyframe scans use it: same key scheme, same reason (an expensive answer about
// an immutable file), different value type.
//
// It replaces a flush-everything-when-full map. That was fine while a library
// fit in the cap, but past it the *next* request after the flush re-ran ffprobe
// on files that had just been in cache — a cliff that got worse the more you
// browsed. Evicting one entry keeps the working set warm instead.
type lru[V any] struct {
	mu       sync.Mutex
	cap      int
	items    map[string]*list.Element
	order    *list.List // front = most recently used
	inflight map[string]*flight[V]
}

type lruEntry[V any] struct {
	key string
	val V
}

// flight is one in-progress load, shared by every caller that asks for the
// same key before it finishes.
type flight[V any] struct {
	done chan struct{}
	val  V
	err  error
}

func newLRU[V any](capacity int) *lru[V] {
	if capacity < 1 {
		capacity = 1
	}
	return &lru[V]{
		cap:      capacity,
		items:    make(map[string]*list.Element, capacity),
		order:    list.New(),
		inflight: map[string]*flight[V]{},
	}
}

// load returns the cached value for key, or runs fn to produce and cache it.
// Concurrent loads of one key share a single fn call: the player's probe and
// stream-open requests land within milliseconds of each other, and two
// keyframe scans of one file would each read the whole of it — over a network
// mount, twice the minutes. Errors reach every waiter and are not cached, so
// the next call retries. An empty key (see statKey) runs fn uncached and
// unshared.
func (c *lru[V]) load(key string, fn func() (V, error)) (V, error) {
	if key == "" {
		return fn()
	}
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		v := el.Value.(*lruEntry[V]).val
		c.mu.Unlock()
		return v, nil
	}
	if f, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-f.done
		return f.val, f.err
	}
	f := &flight[V]{done: make(chan struct{})}
	c.inflight[key] = f
	c.mu.Unlock()

	// Deferred so a panicking fn still releases its waiters — with an error,
	// not a zero value that would read as a successful load and be cached.
	completed := false
	defer func() {
		if !completed {
			f.err = errors.New("cache load panicked")
		}
		c.mu.Lock()
		delete(c.inflight, key)
		if f.err == nil {
			c.putLocked(key, f.val)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.val, f.err = fn()
	completed = true
	return f.val, f.err
}

// get returns the cached value and promotes it to most-recently-used.
func (c *lru[V]) get(key string) (V, bool) {
	var zero V
	if key == "" {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return zero, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry[V]).val, true
}

// put inserts or refreshes key, evicting the least recently used entry when
// full. A key of "" is dropped (see statKey).
func (c *lru[V]) put(key string, val V) {
	if key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.putLocked(key, val)
}

// putLocked is put's body; the caller holds mu.
func (c *lru[V]) putLocked(key string, val V) {
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry[V]).val = val
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry[V]{key: key, val: val})
	for c.order.Len() > c.cap {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry[V]).key)
	}
}

func (c *lru[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
