package main

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
)

// Cache keeps the analyzer's findings per message text in memory, so a
// conversation's history is analyzed once instead of on every turn.
//
// The key is the SHA-256 of the text and the value is its findings (entity,
// offsets, score); the text itself is not kept. The cache is an LRU with a
// size limit and a TTL, and it lives as long as the process. A new
// configuration or a new analyzer sidecar means a new pod, so an entry cannot
// outlive the settings it was computed with.
type Cache struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	ll    *list.List // front: most recently used
	items map[[32]byte]*list.Element
	now   func() time.Time
}

type cacheEntry struct {
	key      [32]byte
	findings []Finding
	expires  time.Time
}

// NewCache returns a cache of at most max entries, each kept for ttl.
func NewCache(max int, ttl time.Duration) *Cache {
	return &Cache{max: max, ttl: ttl, ll: list.New(), items: map[[32]byte]*list.Element{}, now: time.Now}
}

// Get returns the findings of each text; found[i] is false on a miss.
// The returned slices are shared: callers must not modify them.
func (c *Cache) Get(texts []string) (findings [][]Finding, found []bool) {
	findings = make([][]Finding, len(texts))
	found = make([]bool, len(texts))
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range texts {
		el, ok := c.items[sha256.Sum256([]byte(t))]
		if !ok {
			continue
		}
		e := el.Value.(*cacheEntry)
		if now.After(e.expires) {
			c.ll.Remove(el)
			delete(c.items, e.key)
			continue
		}
		c.ll.MoveToFront(el)
		findings[i], found[i] = e.findings, true
	}
	return findings, found
}

// Set stores the findings of each text and evicts the least recently used
// entries beyond the size limit.
func (c *Cache) Set(texts []string, findings [][]Finding) {
	expires := c.now().Add(c.ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, t := range texts {
		key := sha256.Sum256([]byte(t))
		if el, ok := c.items[key]; ok {
			e := el.Value.(*cacheEntry)
			e.findings, e.expires = findings[i], expires
			c.ll.MoveToFront(el)
			continue
		}
		c.items[key] = c.ll.PushFront(&cacheEntry{key: key, findings: findings[i], expires: expires})
		for c.ll.Len() > c.max {
			old := c.ll.Back()
			c.ll.Remove(old)
			delete(c.items, old.Value.(*cacheEntry).key)
		}
	}
}

// Len is the number of entries, expired ones included until they are looked
// up or evicted.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}
