// Package jobstate provides crash-safe job resume functionality.
// It persists per-file status ("success" or "failed") so subsequent runs can
// skip successful files and retry failed ones.
//
// Persistence uses an append-only WAL alongside a JSON snapshot:
//   - MarkSuccess / MarkFailed → update in-memory map + append one WAL line
//   - Load → load snapshot (old or new format), then replay WAL on top
//   - Compact → write new snapshot atomically, remove WAL
//   - Delete → remove both snapshot and WAL
//
// The WAL eliminates the TOCTOU gap that existed in the original full-rewrite
// approach: map update and WAL append happen under the same lock.
package jobstate

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
)

const (
	// StatusSuccess indicates the file was translated successfully.
	StatusSuccess = "success"
	// StatusFailed indicates the file encountered an error.
	// --resume will re-process failed files automatically.
	StatusFailed = "failed"
)

// walEntry is a single WAL record.
type walEntry struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// State tracks per-file translation status.
// The mu lock protects both the in-memory map and WAL appends, eliminating
// any window between map mutation and disk write (no TOCTOU gap).
type State struct {
	mu      sync.Mutex
	entries map[string]string // path → StatusSuccess | StatusFailed
	path    string
	walFile *os.File // open WAL handle for appending
}

// New creates an empty in-memory state.
func New(path string) *State {
	return &State{
		entries: make(map[string]string),
		path:    path,
	}
}

// Load reads state from disk.  If no file exists, returns an empty state.
//
// Supports two snapshot formats for backward compatibility:
//   - Old format: {"completed":["path1","path2"]}  → all treated as StatusSuccess
//   - New format: {"entries":[{"path":"...","status":"..."}]}
//
// After loading the snapshot the WAL file is replayed on top.
func Load(path string) (*State, error) {
	s := New(path)

	if err := s.loadSnapshot(path); err != nil {
		return s, err
	}

	walPath := path + ".wal"
	if err := s.replayWAL(walPath); err != nil {
		// WAL is corrupt — quarantine and ignore (snapshot is still valid).
		_ = os.Rename(walPath, walPath+".corrupt")
	}

	// Open WAL for appending.
	f, err := os.OpenFile(walPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		s.walFile = f
	}
	return s, nil
}

func (s *State) loadSnapshot(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	// Try new format first.
	var newFmt struct {
		Entries []walEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &newFmt); err == nil && len(newFmt.Entries) > 0 {
		for _, e := range newFmt.Entries {
			s.entries[e.Path] = e.Status
		}
		return nil
	}

	// Fall back to old format: {"completed":["path",...]}
	var oldFmt struct {
		Completed []string `json:"completed"`
	}
	if err := json.Unmarshal(data, &oldFmt); err == nil {
		for _, p := range oldFmt.Completed {
			s.entries[p] = StatusSuccess
		}
	}
	// If both formats fail the file is corrupt — start fresh (no error returned).
	return nil
}

func (s *State) replayWAL(walPath string) error {
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
		var e walEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue // Skip malformed lines.
		}
		s.entries[e.Path] = e.Status
	}
	return scanner.Err()
}

// IsDone reports whether the file has been successfully translated.
// Files with StatusFailed are NOT considered done — they will be retried by
// --resume so that transient failures are automatically recovered.
func (s *State) IsDone(filePath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[filePath] == StatusSuccess
}

// MarkSuccess records the file as successfully translated.
// The map update and WAL append happen atomically under the same lock,
// eliminating the TOCTOU gap present in the original snapshot-per-write design.
func (s *State) MarkSuccess(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[filePath] = StatusSuccess
	return s.appendWALLocked(filePath, StatusSuccess)
}

// MarkFailed records the file as failed.
// On the next --resume run it will be retried automatically.
func (s *State) MarkFailed(filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[filePath] = StatusFailed
	return s.appendWALLocked(filePath, StatusFailed)
}

// appendWALLocked writes one WAL entry.  Must be called with mu held.
func (s *State) appendWALLocked(path, status string) error {
	if s.walFile == nil {
		return nil
	}
	e := walEntry{Path: path, Status: status}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.walFile.Write(data)
	return err
}

// Close closes the open WAL file handle.
// Call this when the state is no longer needed (e.g. in tests) to release
// the file lock before the temporary directory is cleaned up.
func (s *State) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.walFile != nil {
		_ = s.walFile.Close()
		s.walFile = nil
	}
}

// Compact writes a new snapshot atomically and removes the WAL.
// Call at the end of a clean run to keep the WAL small.
func (s *State) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries := make([]walEntry, 0, len(s.entries))
	for path, status := range s.entries {
		entries = append(entries, walEntry{Path: path, Status: status})
	}

	type snapshotFmt struct {
		Entries []walEntry `json:"entries"`
	}
	data, err := json.MarshalIndent(snapshotFmt{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}

	// Remove WAL now that snapshot is current.
	if s.walFile != nil {
		_ = s.walFile.Close()
		s.walFile = nil
	}
	_ = os.Remove(s.path + ".wal")
	return nil
}

// Delete removes the state snapshot and WAL from disk.
// Called after a fully successful run.
func (s *State) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.walFile != nil {
		_ = s.walFile.Close()
		s.walFile = nil
	}

	err1 := os.Remove(s.path)
	if os.IsNotExist(err1) {
		err1 = nil
	}
	err2 := os.Remove(s.path + ".wal")
	if os.IsNotExist(err2) {
		err2 = nil
	}
	if err1 != nil {
		return err1
	}
	return err2
}
