// Tests for the in-memory cache.
package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// cached asserts that key holds want at the given time.
func cached(t *testing.T, c *Cache, key string, now time.Time, want string) {
	t.Helper()
	got, ok := c.Get(key, now)
	if !ok {
		t.Fatalf("key %q is missing", key)
	}
	eq(t, got, want)
}

// notCached asserts that key is absent at the given time.
func notCached(t *testing.T, c *Cache, key string, now time.Time) {
	t.Helper()
	if got, ok := c.Get(key, now); ok {
		t.Fatalf("key %q is present with value %q", key, got)
	}
}

func TestCacheGetSet(t *testing.T) {
	c := NewCache(10)
	c.Set("a", "1", t0.Add(time.Hour))
	cached(t, c, "a", t0, "1")
	notCached(t, c, "b", t0)
}

func TestCacheExpires(t *testing.T) {
	c := NewCache(10)
	c.Set("a", "1", t0.Add(time.Minute))
	cached(t, c, "a", t0.Add(59*time.Second), "1")
	notCached(t, c, "a", t0.Add(time.Minute))
	eq(t, c.Len(), 0)
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewCache(2)
	later := t0.Add(time.Hour)
	c.Set("a", "1", later)
	c.Set("b", "2", later)
	cached(t, c, "a", t0, "1") // "a" is now the most recently used
	c.Set("c", "3", later)

	notCached(t, c, "b", t0)
	cached(t, c, "a", t0, "1")
	cached(t, c, "c", t0, "3")
	eq(t, c.Len(), 2)
}

func TestCacheOverwrite(t *testing.T) {
	c := NewCache(10)
	c.Set("a", "1", t0.Add(time.Minute))
	c.Set("a", "2", t0.Add(time.Hour))
	eq(t, c.Len(), 1)
	cached(t, c, "a", t0.Add(30*time.Minute), "2")
}

func TestCacheDelete(t *testing.T) {
	c := NewCache(10)
	c.Set("a", "1", t0.Add(time.Hour))
	c.Delete("a")
	c.Delete("a") // deleting a missing key is harmless
	notCached(t, c, "a", t0)
	eq(t, c.Len(), 0)
}

func TestCacheReadDoesNotExtendExpiry(t *testing.T) {
	c := NewCache(10)
	c.Set("a", "1", t0.Add(time.Minute))
	cached(t, c, "a", t0.Add(50*time.Second), "1")
	notCached(t, c, "a", t0.Add(61*time.Second))
}

// createdKey asserts that request finds key at the given time.
func createdKey(t *testing.T, c *Cache, request uint64, now time.Time, want string) {
	t.Helper()
	got, ok := c.CreatedKey(request, now)
	if !ok {
		t.Fatalf("request %d is missing", request)
	}
	eq(t, got, want)
}

// notCreated asserts that request finds nothing at the given time.
func notCreated(t *testing.T, c *Cache, request uint64, now time.Time) {
	t.Helper()
	if got, ok := c.CreatedKey(request, now); ok {
		t.Fatalf("request %d finds key %q", request, got)
	}
}

// indexedRequests checks that every request in the index points at a live
// entry stored for that request, and returns how many requests there are.
func indexedRequests(t *testing.T, c *Cache) int {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for request, r := range c.requests {
		entry := r.el.Value.(cacheEntry)
		if entry.request != request || c.items[entry.key] != r.el {
			t.Fatalf("request %d points at a dropped or different entry %q", request, entry.key)
		}
	}
	return len(c.requests)
}

func TestCreatedKey(t *testing.T) {
	c := NewCache(10)
	c.SetCreated("d/aaaaa", testURL, t0.Add(6*time.Hour), 42, t0.Add(10*time.Minute))
	createdKey(t, c, 42, t0.Add(9*time.Minute), "d/aaaaa")
	notCreated(t, c, 42, t0.Add(10*time.Minute))
	cached(t, c, "d/aaaaa", t0.Add(10*time.Minute), testURL)
}

func TestCreatedKeyFollowsTheEntry(t *testing.T) {
	later, until := t0.Add(time.Hour), t0.Add(10*time.Minute)

	t.Run("delete", func(t *testing.T) {
		c := NewCache(10)
		c.SetCreated("d/aaaaa", testURL, later, 42, until)
		c.Delete("d/aaaaa")
		notCreated(t, c, 42, t0)
		eq(t, indexedRequests(t, c), 0)
	})
	t.Run("set", func(t *testing.T) {
		c := NewCache(10)
		c.SetCreated("d/aaaaa", testURL, later, 42, until)
		c.Set("d/aaaaa", testURL, later)
		notCreated(t, c, 42, t0)
		eq(t, indexedRequests(t, c), 0)
	})
	t.Run("evict", func(t *testing.T) {
		c := NewCache(2)
		c.SetCreated("d/aaaaa", testURL, later, 42, until)
		c.Set("d/bbbbb", testURL, later)
		c.Set("d/ccccc", testURL, later)
		notCached(t, c, "d/aaaaa", t0)
		notCreated(t, c, 42, t0)
		eq(t, indexedRequests(t, c), 0)
	})
	t.Run("expire", func(t *testing.T) {
		c := NewCache(10)
		c.SetCreated("d/aaaaa", testURL, t0.Add(time.Minute), 42, until)
		notCached(t, c, "d/aaaaa", t0.Add(time.Minute)) // drops the expired entry
		notCreated(t, c, 42, t0)                        // even at a time it was live
		eq(t, indexedRequests(t, c), 0)
	})
}

// Entries stored with Set never enter the request index.
func TestCreatedKeyIndexesOnlyCreatedEntries(t *testing.T) {
	c := NewCache(10)
	later := t0.Add(time.Hour)
	c.Set("d/aaaaa", testURL, later)
	c.Set("d/bbbbb", testURL, later)
	notCreated(t, c, 0, t0)
	eq(t, indexedRequests(t, c), 0)
	c.SetCreated("d/ccccc", testURL, later, 42, t0.Add(10*time.Minute))
	eq(t, indexedRequests(t, c), 1)
}

// When the same request is stored twice, the later entry is the one found.
func TestCreatedKeySameRequestTwice(t *testing.T) {
	later, until := t0.Add(time.Hour), t0.Add(10*time.Minute)
	first, second := "https://example.com/a", "https://example.org/b"

	t.Run("the later entry is found", func(t *testing.T) {
		c := NewCache(10)
		c.SetCreated("d/aaaaa", first, later, 42, until)
		c.SetCreated("d/bbbbb", second, later, 42, until)
		createdKey(t, c, 42, t0, "d/bbbbb")
		c.Delete("d/aaaaa") // dropping the earlier entry keeps the later one
		createdKey(t, c, 42, t0, "d/bbbbb")
	})
	t.Run("the earlier entry stays a normal entry", func(t *testing.T) {
		c := NewCache(10)
		c.SetCreated("d/aaaaa", first, later, 42, until)
		c.SetCreated("d/bbbbb", second, later, 42, until)
		c.Delete("d/bbbbb")
		notCreated(t, c, 42, t0)
		cached(t, c, "d/aaaaa", t0, first)
		eq(t, indexedRequests(t, c), 0)
	})
}

// CreatedKey neither counts as a use nor removes anything.
func TestCreatedKeyOnlyReads(t *testing.T) {
	later, until := t0.Add(time.Hour), t0.Add(10*time.Minute)

	t.Run("keeps the LRU order", func(t *testing.T) {
		c := NewCache(2)
		c.SetCreated("d/aaaaa", testURL, later, 42, until)
		c.Set("d/bbbbb", testURL, later)
		createdKey(t, c, 42, t0, "d/aaaaa")
		c.Set("d/ccccc", testURL, later) // "d/aaaaa" is still the least recently used
		notCached(t, c, "d/aaaaa", t0)
		cached(t, c, "d/bbbbb", t0, testURL)
	})
	t.Run("an expired entry is not returned and stays", func(t *testing.T) {
		c := NewCache(10)
		c.SetCreated("d/aaaaa", testURL, t0.Add(time.Minute), 42, until)
		notCreated(t, c, 42, t0.Add(time.Minute))
		eq(t, c.Len(), 1)
		eq(t, indexedRequests(t, c), 1)
	})
}

// SetCreated on a stored key replaces the entry and its request.
func TestSetCreatedReplaces(t *testing.T) {
	c := NewCache(10)
	later, until := t0.Add(time.Hour), t0.Add(10*time.Minute)
	c.SetCreated("d/aaaaa", "https://example.com/a", later, 42, until)
	c.SetCreated("d/aaaaa", "https://example.org/b", later, 43, until)
	eq(t, c.Len(), 1)
	cached(t, c, "d/aaaaa", t0, "https://example.org/b")
	notCreated(t, c, 42, t0)
	createdKey(t, c, 43, t0, "d/aaaaa")
	eq(t, indexedRequests(t, c), 1)
}

func TestClear(t *testing.T) {
	c := NewCache(10)
	later, until := t0.Add(time.Hour), t0.Add(10*time.Minute)
	c.Set("d/aaaaa", testURL, later)
	c.SetCreated("d/bbbbb", testURL, later, 42, until)
	c.Clear()
	eq(t, c.Len(), 0)
	notCached(t, c, "d/aaaaa", t0)
	notCached(t, c, "d/bbbbb", t0)
	notCreated(t, c, 42, t0)
}

func TestCacheConcurrent(t *testing.T) {
	c := NewCache(50)
	later, until := t0.Add(time.Hour), t0.Add(10*time.Minute)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 2000 {
				key := fmt.Sprintf("k%d", (g*31+i)%200)
				request := uint64((g*7 + i) % 100)
				switch i % 5 {
				case 0:
					c.Set(key, "v", later)
				case 1:
					c.Get(key, t0)
				case 2:
					c.SetCreated(key, "v", later, request, until)
				case 3:
					c.CreatedKey(request, t0)
				default:
					c.Delete(key)
				}
				if i == 1000 {
					c.Clear()
				}
			}
		}()
	}
	wg.Wait()
	if n := c.Len(); n > 50 {
		t.Fatalf("cache holds %d entries, limit is 50", n)
	}
	if n := indexedRequests(t, c); n > c.Len() {
		t.Fatalf("index holds %d requests for %d entries", n, c.Len())
	}
}
