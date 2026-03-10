// Package translator provides a LibreTranslate HTTP client with batching,
// retry, rate limiting, and an in-memory LRU translation cache.
package translator

import (
	"container/list"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"time"
)

// numShards is the number of independent LRU partitions.
// Hashing keys across 16 shards reduces lock contention ~16× under concurrent load.
const numShards = 16

// entry holds a single key-value pair stored inside a shard's LRU list.
type entry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	CreatedAt int64  `json:"t,omitempty"` // UnixNano; 0 = no TTL tracking
}

// shard is one independent partition of the segmented LRU cache.
// Each shard has its own mutex, list, and map so goroutines operating on
// different shards never contend.
type shard struct {
	mu   sync.Mutex
	ll   *list.List
	data map[string]*list.Element
	cap  int // max entries for this shard
}

// Cache is a thread-safe, segmented LRU-eviction translation cache.
// It is split into numShards independent shards to eliminate global serialisation.
// Entries are evicted from each shard's tail (LRU) when that shard is full.
// Optional TTL causes expired entries to be removed lazily on Get.
//
// Persistence uses an append-only WAL (write-ahead log) alongside a JSON
// snapshot.  Each Put appends one line to the WAL; Save() compacts the WAL
// into a new snapshot and truncates it.  This eliminates full-file rewrites
// on every flush.
type Cache struct {
	shards  [numShards]shard
	ttl     time.Duration // 0 = disabled
	walPath string        // set during Load; used by appendWAL
	walMu   sync.Mutex    // serialises WAL appends
	walFile *os.File      // open file handle for appending (nil if no Load)
}

// NewCacheWithTTL creates a Cache with the given capacity and optional TTL.
// If max <= 0, defaults to 10 000 entries.
// If ttl <= 0, entries never expire.
func NewCacheWithTTL(max int, ttl time.Duration) *Cache {
	if max <= 0 {
		max = 10000
	}
	perShard := max / numShards
	if perShard < 1 {
		perShard = 1
	}
	c := &Cache{ttl: ttl}
	for i := range c.shards {
		c.shards[i] = shard{
			ll:   list.New(),
			data: make(map[string]*list.Element, perShard),
			cap:  perShard,
		}
	}
	return c
}

// NewCache creates a Cache with the given capacity and no TTL.
// This is the backward-compatible constructor for existing callers.
func NewCache(max int) *Cache {
	return NewCacheWithTTL(max, 0)
}

// shardFor maps a normalized key to a shard index using FNV-32a.
// No external dependencies — hash/fnv is in the standard library.
func shardFor(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % numShards
}

// normalize produces a canonical cache key from raw subtitle text.
// It lowercases and collapses multiple whitespace runs to a single space,
// preserving word boundaries ("Thank you" != "Thankyou").
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Get looks up a translation in the cache.
// On a hit the entry is promoted to the front of the shard's LRU list.
// Expired entries are removed lazily and treated as misses.
// Returns ("", false) on a miss.
func (c *Cache) Get(src string) (string, bool) {
	key := normalize(src)
	sh := &c.shards[shardFor(key)]

	sh.mu.Lock()
	defer sh.mu.Unlock()

	el, ok := sh.data[key]
	if !ok {
		return "", false
	}
	e := el.Value.(*entry)

	// Lazy TTL eviction.
	if c.ttl > 0 && e.CreatedAt != 0 {
		if time.Now().UnixNano()-e.CreatedAt > int64(c.ttl) {
			sh.ll.Remove(el)
			delete(sh.data, key)
			return "", false
		}
	}

	sh.ll.MoveToFront(el)
	return e.Value, true
}

// Put stores a translation in the cache.
// If the key already exists its value is updated and it is promoted to front.
// When the shard is at capacity the LRU tail entry is evicted.
// After the shard mutation the entry is appended to the WAL (if active).
func (c *Cache) Put(src, translated string) {
	key := normalize(src)
	sh := &c.shards[shardFor(key)]

	var now int64
	if c.ttl > 0 {
		now = time.Now().UnixNano()
	}

	sh.mu.Lock()
	if el, ok := sh.data[key]; ok {
		e := el.Value.(*entry)
		e.Value = translated
		if c.ttl > 0 {
			e.CreatedAt = now
		}
		sh.ll.MoveToFront(el)
		sh.mu.Unlock()
		c.appendWAL(key, translated, now)
		return
	}

	e := &entry{Key: key, Value: translated, CreatedAt: now}
	el := sh.ll.PushFront(e)
	sh.data[key] = el

	// Evict LRU tail if over capacity.
	if sh.ll.Len() > sh.cap {
		old := sh.ll.Back()
		if old != nil {
			sh.ll.Remove(old)
			delete(sh.data, old.Value.(*entry).Key)
		}
	}
	sh.mu.Unlock()

	c.appendWAL(key, translated, now)
}

// Size returns the total number of entries across all shards.
func (c *Cache) Size() int {
	total := 0
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.Lock()
		total += sh.ll.Len()
		sh.mu.Unlock()
	}
	return total
}
