// Package jobstate provides crash-safe job resume functionality.
// It persists the set of completed (succeeded + failed) files to disk so
// subsequent runs can skip already-processed files.
package jobstate

import (
	"encoding/json"
	"os"
	"sync"
)

// State tracks which files have been processed in a translation run.
type State struct {
	mu        sync.Mutex
	Completed map[string]bool `json:"completed"` // file path → true
	path      string
}

// New creates an empty in-memory state.
func New(path string) *State {
	return &State{
		Completed: make(map[string]bool),
		path:      path,
	}
}

// Load reads state from disk. If the file does not exist, returns an empty state.
func Load(path string) (*State, error) {
	s := New(path)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}

	type diskState struct {
		Completed []string `json:"completed"`
	}
	var ds diskState
	if err := json.Unmarshal(data, &ds); err != nil {
		// Corrupted state — treat as fresh start.
		return s, nil
	}

	for _, f := range ds.Completed {
		s.Completed[f] = true
	}
	return s, nil
}

// IsDone reports whether the file has already been processed.
func (s *State) IsDone(filePath string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Completed[filePath]
}

// MarkDone records the file as processed and flushes to disk.
func (s *State) MarkDone(filePath string) error {
	s.mu.Lock()
	s.Completed[filePath] = true
	s.mu.Unlock()
	return s.save()
}

// save writes the state to disk atomically.
func (s *State) save() error {
	s.mu.Lock()
	keys := make([]string, 0, len(s.Completed))
	for k := range s.Completed {
		keys = append(keys, k)
	}
	s.mu.Unlock()

	type diskState struct {
		Completed []string `json:"completed"`
	}
	data, err := json.MarshalIndent(diskState{Completed: keys}, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Delete removes the state file from disk (called after a clean full run).
func (s *State) Delete() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
