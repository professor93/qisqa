// In-memory cache: keeps the most recently used entries, each with its own
// expiry time. Safe for concurrent use.
package main

import (
	"container/list"
	"sync"
	"time"
)

// Cache is a size-limited LRU map from string to string. When it is full the
// least recently used entry is dropped. An entry also disappears once its
// expiry time is reached; reading an entry does not extend that time.
//
// An entry stored with SetCreated can also be found, until a time the caller
// picks, by a number that identifies the create request, so that a repeated
// request gets the same link back.
type Cache struct {
	mu       sync.Mutex
	max      int
	order    *list.List // front is the most recently used entry
	items    map[string]*list.Element
	requests map[uint64]requestEntry // only entries stored by SetCreated
}

// cacheEntry is the value stored in the list.
type cacheEntry struct {
	key, value string
	expires    time.Time
	request    uint64 // the request given to SetCreated; zero from Set
}

// requestEntry is one entry of the request index: the element a create
// request stored, and when the request stops finding it.
type requestEntry struct {
	el    *list.Element
	until time.Time
}

// NewCache returns an empty cache holding at most max entries.
func NewCache(max int) *Cache {
	return &Cache{
		max:      max,
		order:    list.New(),
		items:    make(map[string]*list.Element),
		requests: make(map[uint64]requestEntry),
	}
}

// Set stores value under key until expires, replacing any previous entry.
func (c *Cache) Set(key, value string, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.put(cacheEntry{key: key, value: value, expires: expires})
	c.trim()
}

// SetCreated is Set for an entry made by a create request: until the time
// until, CreatedKey also finds the entry by request. If another entry was
// stored with the same request, only the new one is found by it.
func (c *Cache) SetCreated(key, value string, expires time.Time, request uint64, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests[request] = requestEntry{c.put(cacheEntry{key, value, expires, request}), until}
	c.trim()
}

// Get returns the value stored under key if it has not expired at now.
func (c *Cache) Get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return "", false
	}
	entry := el.Value.(cacheEntry)
	if !entry.expires.After(now) {
		c.remove(el)
		return "", false
	}
	c.order.MoveToFront(el)
	return entry.value, true
}

// CreatedKey returns the key of the entry stored for request, unless at now
// the request's time or the entry's own expiry has been reached. It only
// reads: the entry keeps its place in the LRU order and nothing is removed.
func (c *Cache) CreatedKey(request uint64, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.requests[request]
	if !ok || !r.until.After(now) {
		return "", false
	}
	entry := r.el.Value.(cacheEntry)
	if !entry.expires.After(now) {
		return "", false
	}
	return entry.key, true
}

// Delete removes key; a missing key is ignored.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
}

// Clear drops every entry.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order.Init()
	clear(c.items)
	clear(c.requests)
}

// Len returns the number of stored entries, including expired ones that
// have not been read since they expired.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// put stores entry as the most recently used one, replacing any entry under
// the same key, and returns its element. The caller holds the lock.
func (c *Cache) put(entry cacheEntry) *list.Element {
	if el, ok := c.items[entry.key]; ok {
		c.forget(el)
		el.Value = entry
		c.order.MoveToFront(el)
		return el
	}
	el := c.order.PushFront(entry)
	c.items[entry.key] = el
	return el
}

// trim drops the least recently used entry if the cache holds one too many.
// The caller holds the lock.
func (c *Cache) trim() {
	if c.order.Len() > c.max {
		c.remove(c.order.Back())
	}
}

// remove drops one element. The caller holds the lock.
func (c *Cache) remove(el *list.Element) {
	c.forget(el)
	c.order.Remove(el)
	delete(c.items, el.Value.(cacheEntry).key)
}

// forget stops el from being found by its request. A later entry stored
// with the same request keeps it. The caller holds the lock.
func (c *Cache) forget(el *list.Element) {
	if request := el.Value.(cacheEntry).request; c.requests[request].el == el {
		delete(c.requests, request)
	}
}
