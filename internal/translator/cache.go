// Package translator provides a LibreTranslate HTTP client with batching,
// retry, rate limiting, and an in-memory LRU translation cache.
package translator

import (
	"container/list"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"unicode"
)

// entry holds a single key-value pair stored inside the LRU list.
type entry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Cache is a thread-safe, LRU-eviction translation cache backed by a
// doubly-linked list + hash map for O(1) Get and Put.
// Entries are evicted from the tail (least recently used) when the cache
// is full, so the file on disk never exceeds max entries.
type Cache struct {
	mu   sync.RWMutex
	max  int                      // Maximum number of entries allowed
	ll   *list.List               // LRU list: front = most recently used
	data map[string]*list.Element // key → list element lookup
}

// NewCache creates a Cache with the given capacity.
// If max <= 0, defaults to 10 000 entries.
func NewCache(max int) *Cache {
	if max <= 0 {
		max = 10000
	}
	return &Cache{
		max:  max,
		ll:   list.New(),
		data: make(map[string]*list.Element, max),
	}
}

/* ---------- NORMALIZE ---------- */

// normalize produces a canonical cache key from raw subtitle text:
// lowercase, whitespace stripped, so "Thank you " and "thank you" hit the same slot.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))

	var b strings.Builder
	b.Grow(len(s))

	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

/* ---------- GET ---------- */

// Get looks up a translation in the cache.
// On a hit, the entry is promoted to the front of the LRU list.
// Returns ("", false) on a miss.
func (c *Cache) Get(src string) (string, bool) {
	key := normalize(src)

	c.mu.Lock() // Full lock: MoveToFront mutates the list.
	defer c.mu.Unlock()

	if el, ok := c.data[key]; ok {
		c.ll.MoveToFront(el) // Mark as recently used.
		return el.Value.(*entry).Value, true
	}
	return "", false
}

/* ---------- PUT ---------- */

// Put stores a translation in the cache.
// If the key already exists, its value is updated and it is promoted to
// the front of the LRU list.
// When the cache is at capacity, the least recently used (tail) entry is
// evicted before inserting the new one.
func (c *Cache) Put(src, translated string) {
	key := normalize(src)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry.
	if el, ok := c.data[key]; ok {
		el.Value.(*entry).Value = translated
		c.ll.MoveToFront(el)
		return
	}

	// Insert new entry at the front.
	el := c.ll.PushFront(&entry{key, translated})
	c.data[key] = el

	// Evict LRU tail if over capacity.
	if c.ll.Len() > c.max {
		old := c.ll.Back()
		if old != nil {
			c.ll.Remove(old)
			delete(c.data, old.Value.(*entry).Key)
		}
	}
}

/* ---------- STATS ---------- */

// Size returns the current number of entries in the cache.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.data)
}

/* ---------- PERSISTENCE ---------- */

// cacheFile is the JSON on-disk format.
// Entries are stored from most-recently-used to least-recently-used
// so that Load can stop early once the capacity limit is reached.
type cacheFile struct {
	Entries []*entry `json:"entries"`
}

// Load reads the cache from disk, loading at most max entries (MRU first).
// If the file does not exist, the call is a no-op (fresh start).
// If the file is corrupt, it is renamed to *.corrupt and ignored.
// If the on-disk entry count exceeds max, an async Save is triggered to
// trim the file down to the current cap.
func (c *Cache) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // No cache file yet — that's fine.
	}
	if err != nil {
		return err
	}

	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		// Corrupted file — quarantine it and start fresh.
		_ = os.Rename(path, path+".corrupt")
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Replay entries in reverse MRU order so the final list is MRU→LRU after PushFront.
	for i := len(cf.Entries) - 1; i >= 0; i-- {
		e := cf.Entries[i]
		el := c.ll.PushFront(e)
		c.data[e.Key] = el
		if c.ll.Len() >= c.max {
			break // Reached capacity — remaining entries are dropped (LRU).
		}
	}

	// If the file was larger than max, rewrite it immediately to trim the excess.
	if len(cf.Entries) > c.max {
		go c.Save(path) // Non-blocking; failure is acceptable here.
	}

	return nil
}

// Save writes all current cache entries to disk atomically (temp file → rename).
// Entries are ordered MRU→LRU so Load can stop early at the capacity limit.
// Safe to call concurrently — holds only the read lock during the copy.
func (c *Cache) Save(path string) error {
	c.mu.RLock()

	// Snapshot the list under the read lock (no mutation path).
	entries := make([]*entry, 0, c.ll.Len())
	for el := c.ll.Front(); el != nil; el = el.Next() {
		entries = append(entries, el.Value.(*entry))
	}

	c.mu.RUnlock()

	cf := cacheFile{Entries: entries}

	data, err := json.Marshal(cf)
	if err != nil {
		return err
	}

	// Atomic write: write to a temp file first, then rename into place.
	// This prevents a partially-written file from corrupting the cache.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}
