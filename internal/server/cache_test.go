package server

import "testing"

func TestBoundedCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	cache := boundedCache[int, string]{limit: 3}
	cache.put(1, "one")
	cache.put(2, "two")
	cache.put(3, "three")
	if value, ok := cache.get(1); !ok || value != "one" {
		t.Fatalf("cache get(1)=(%q,%v)", value, ok)
	}
	cache.put(4, "four")
	if _, ok := cache.get(2); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if cache.len() != 3 {
		t.Fatalf("cache length=%d want 3", cache.len())
	}
	if value, ok := cache.get(4); !ok || value != "four" {
		t.Fatalf("cache get(4)=(%q,%v)", value, ok)
	}
	cache.clear()
	if cache.len() != 0 {
		t.Fatalf("cache length after clear=%d", cache.len())
	}
}

func TestBoundedCacheUsesSafeDefaultLimit(t *testing.T) {
	cache := boundedCache[int, int]{}
	for index := 0; index < defaultBoundedCacheLimit+8; index++ {
		cache.put(index, index)
	}
	if cache.len() != defaultBoundedCacheLimit {
		t.Fatalf("cache length=%d want %d", cache.len(), defaultBoundedCacheLimit)
	}
	if _, ok := cache.get(0); ok {
		t.Fatal("oldest entry survived default-limit eviction")
	}
}
