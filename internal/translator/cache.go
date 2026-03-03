// Package translator provides a LibreTranslate HTTP client with batching,
// retry, rate limiting, and an in-memory LRU translation cache.
package translator

import (
	"bufio"
	"container/list"
	"encoding/json"
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

/* ---------- KEY ROUTING ---------- */

// shardFor maps a normalized key to a shard index using FNV-32a.
// No external dependencies — hash/fnv is in the standard library.
func shardFor(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % numShards
}

/* ---------- NORMALIZE ---------- */

// normalize produces a canonical cache key from raw subtitle text.
// It lowercases and collapses multiple whitespace runs to a single space,
// preserving word boundaries ("Thank you" != "Thankyou").
//
// OLD behaviour stripped all internal whitespace, causing collisions:
//
//	"Thank you" → "thankyou"
//	"Thankyou"  → "thankyou"  (collision)
//
// NEW behaviour uses strings.Fields + Join so each word is kept separated.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

/* ---------- GET ---------- */

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

/* ---------- PUT ---------- */

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

/* ---------- STATS ---------- */

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

/* ---------- WAL ---------- */

// walLine is the per-entry WAL record written as a single JSON line.
type walLine struct {
	K string `json:"k"`
	V string `json:"v"`
	T int64  `json:"t,omitempty"` // CreatedAt UnixNano
}

// appendWAL appends a single entry to the WAL file.
// Serialised by walMu — one writer at a time, non-blocking from callers' perspective.
func (c *Cache) appendWAL(key, value string, createdAt int64) {
	c.walMu.Lock()
	defer c.walMu.Unlock()
	if c.walFile == nil {
		return // WAL not active (Load was not called)
	}
	line := walLine{K: key, V: value, T: createdAt}
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = c.walFile.Write(data)
}

/* ---------- PERSISTENCE ---------- */

// cacheFile is the JSON snapshot format.
// Entries are stored MRU-first so Load can stop early once capacity is reached.
type cacheFile struct {
	Entries []*entry `json:"entries"`
}

// Load reads the cache from disk (snapshot + WAL replay).
// If no snapshot exists the call is a no-op (fresh start).
// A corrupt snapshot is quarantined (renamed to *.corrupt) and ignored.
// After loading, the WAL file is opened for appending so subsequent Puts
// are persisted incrementally without rewriting the full snapshot.
func (c *Cache) Load(path string) error {
	// 1. Load snapshot.
	if err := c.loadSnapshot(path); err != nil {
		return err
	}

	walPath := path + ".wal"
	c.walPath = walPath

	// 2. Replay WAL on top of snapshot.
	if err := c.replayWAL(walPath); err != nil {
		// WAL is corrupt — quarantine it and start fresh from snapshot.
		_ = os.Rename(walPath, walPath+".corrupt")
	}

	// 3. Open WAL for subsequent appends.
	c.walMu.Lock()
	defer c.walMu.Unlock()
	f, err := os.OpenFile(walPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		c.walFile = f
	}
	return nil
}

// loadSnapshot reads the JSON snapshot file into the cache.
func (c *Cache) loadSnapshot(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // No snapshot yet — fresh start.
	}
	if err != nil {
		return err
	}

	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		_ = os.Rename(path, path+".corrupt")
		return nil // Corrupt snapshot — start fresh.
	}

	// Replay entries in reverse MRU order (LRU first) so PushFront
	// leaves MRU entries at the front of each shard's list.
	for i := len(cf.Entries) - 1; i >= 0; i-- {
		e := cf.Entries[i]
		idx := shardFor(e.Key)
		sh := &c.shards[idx]
		sh.mu.Lock()
		if sh.ll.Len() < sh.cap {
			el := sh.ll.PushFront(e)
			sh.data[e.Key] = el
		}
		sh.mu.Unlock()
	}
	return nil
}

// replayWAL applies WAL entries on top of the in-memory cache.
// Malformed lines are silently skipped (partial WAL is acceptable).
func (c *Cache) replayWAL(walPath string) error {
	f, err := os.Open(walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var wl walLine
		if err := json.Unmarshal(scanner.Bytes(), &wl); err != nil {
			continue // Skip malformed WAL lines.
		}
		c.putInternal(wl.K, wl.V, wl.T)
	}
	return scanner.Err()
}

// putInternal inserts a pre-normalized key without appending to the WAL.
// Used during snapshot/WAL replay to avoid recursive WAL writes.
func (c *Cache) putInternal(key, value string, createdAt int64) {
	idx := shardFor(key)
	sh := &c.shards[idx]
	sh.mu.Lock()
	defer sh.mu.Unlock()

	if el, ok := sh.data[key]; ok {
		e := el.Value.(*entry)
		e.Value = value
		e.CreatedAt = createdAt
		sh.ll.MoveToFront(el)
		return
	}

	e := &entry{Key: key, Value: value, CreatedAt: createdAt}
	el := sh.ll.PushFront(e)
	sh.data[key] = el
	if sh.ll.Len() > sh.cap {
		old := sh.ll.Back()
		if old != nil {
			sh.ll.Remove(old)
			delete(sh.data, old.Value.(*entry).Key)
		}
	}
}

// Close closes the open WAL file handle.
// Call this when the cache is no longer needed (e.g. in tests) to release
// the file lock before the temporary directory is cleaned up.
func (c *Cache) Close() {
	c.walMu.Lock()
	defer c.walMu.Unlock()
	if c.walFile != nil {
		_ = c.walFile.Close()
		c.walFile = nil
	}
}

// Save writes all current cache entries to disk atomically (write to temp file
// then rename) and then truncates the WAL (compaction).
// Entries are written MRU-first per shard.
// The periodic orchestrator flush (every 5 min) acts as the compaction trigger.
func (c *Cache) Save(path string) error {
	// Snapshot all shards under per-shard locks.
	var entries []*entry
	for i := range c.shards {
		sh := &c.shards[i]
		sh.mu.Lock()
		for el := sh.ll.Front(); el != nil; el = el.Next() {
			entries = append(entries, el.Value.(*entry))
		}
		sh.mu.Unlock()
	}

	cf := cacheFile{Entries: entries}
	data, err := json.Marshal(cf)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	// Compact: truncate WAL now that the snapshot is up-to-date.
	walPath := path + ".wal"
	c.walMu.Lock()
	defer c.walMu.Unlock()
	if c.walFile != nil {
		_ = c.walFile.Close()
		c.walFile = nil
	}
	f, err := os.OpenFile(walPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		c.walFile = f
	}
	return nil
}
