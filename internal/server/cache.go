package server

import "sync"

const defaultBoundedCacheLimit = 32

type boundedCacheEntry[K comparable, V any] struct {
	key   K
	value V
}

// boundedCache is a small in-memory LRU used only for derived API responses.
// The SQLite database remains the source of truth and every data revision is
// part of the caller's key, so eviction never changes accounting semantics.
type boundedCache[K comparable, V any] struct {
	mu      sync.Mutex
	limit   int
	entries []boundedCacheEntry[K, V]
}

func (c *boundedCache[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.entries {
		if c.entries[index].key != key {
			continue
		}
		entry := c.entries[index]
		copy(c.entries[index:], c.entries[index+1:])
		c.entries[len(c.entries)-1] = entry
		return entry.value, true
	}
	var zero V
	return zero, false
}

func (c *boundedCache[K, V]) put(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := range c.entries {
		if c.entries[index].key != key {
			continue
		}
		copy(c.entries[index:], c.entries[index+1:])
		c.entries = c.entries[:len(c.entries)-1]
		break
	}
	limit := c.limit
	if limit <= 0 {
		limit = defaultBoundedCacheLimit
	}
	if len(c.entries) >= limit {
		copy(c.entries, c.entries[1:])
		c.entries = c.entries[:len(c.entries)-1]
	}
	c.entries = append(c.entries, boundedCacheEntry[K, V]{key: key, value: value})
}

func (c *boundedCache[K, V]) clear() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

func (c *boundedCache[K, V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
