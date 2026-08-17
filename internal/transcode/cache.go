package transcode

import (
	"container/list"
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
	mu    sync.Mutex
	cap   int
	items map[string]*list.Element
	order *list.List // front = most recently used
}

type lruEntry[V any] struct {
	key string
	val V
}

func newLRU[V any](capacity int) *lru[V] {
	if capacity < 1 {
		capacity = 1
	}
	return &lru[V]{
		cap:   capacity,
		items: make(map[string]*list.Element, capacity),
		order: list.New(),
	}
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
