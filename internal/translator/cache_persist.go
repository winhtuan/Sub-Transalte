package translator

import (
	"bufio"
	"encoding/json"
	"os"
)

// walLine is the per-entry WAL record written as a single JSON line.
type walLine struct {
	K string `json:"k"`
	V string `json:"v"`
	T int64  `json:"t,omitempty"` // CreatedAt UnixNano
}

// cacheFile is the JSON snapshot format.
// Entries are stored MRU-first so Load can stop early once capacity is reached.
type cacheFile struct {
	Entries []*entry `json:"entries"`
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
