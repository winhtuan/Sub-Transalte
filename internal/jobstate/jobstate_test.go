package jobstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWALReplayWithoutCompact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s1.Close)

	_ = s1.MarkSuccess("/a/file1_en.srt")
	_ = s1.MarkFailed("/a/file2_en.srt")
	s1.Close() // close before loading second instance

	// Load a second instance — should replay WAL.
	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)

	if !s2.IsDone("/a/file1_en.srt") {
		t.Error("expected file1 to be done (success) after WAL replay")
	}
	if s2.IsDone("/a/file2_en.srt") {
		t.Error("expected file2 NOT done (failed) after WAL replay")
	}
}

func TestSuccessFailDistinction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	_ = s.MarkSuccess("success.srt")
	_ = s.MarkFailed("failed.srt")

	if !s.IsDone("success.srt") {
		t.Error("success.srt should be IsDone=true")
	}
	if s.IsDone("failed.srt") {
		t.Error("failed.srt should be IsDone=false (will be retried on --resume)")
	}
}

func TestResumeRetriesFailedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	_ = s.MarkFailed("retry_me.srt")
	_ = s.MarkSuccess("skip_me.srt")

	if s.IsDone("retry_me.srt") {
		t.Error("failed file should be retried (IsDone=false)")
	}
	if !s.IsDone("skip_me.srt") {
		t.Error("successful file should be skipped (IsDone=true)")
	}
}

func TestOldFormatCompatibility(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	oldJSON := `{"completed":["old_file1.srt","old_file2.srt"]}`
	if err := os.WriteFile(path, []byte(oldJSON), 0644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	if !s.IsDone("old_file1.srt") {
		t.Error("old format: file1 should be done")
	}
	if !s.IsDone("old_file2.srt") {
		t.Error("old format: file2 should be done")
	}
}

func TestCompact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.MarkSuccess("a.srt")
	_ = s1.MarkFailed("b.srt")

	if err := s1.Compact(); err != nil {
		t.Fatal("Compact:", err)
	}
	// Compact closes the WAL file, so no explicit Close needed here.

	// WAL should be gone after compact.
	walPath := path + ".wal"
	if _, err := os.Stat(walPath); err == nil {
		t.Error("WAL file should be removed after Compact")
	}

	// New Load should still see both entries (from snapshot).
	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Close)

	if !s2.IsDone("a.srt") {
		t.Error("a.srt should be success after compact+reload")
	}
	if s2.IsDone("b.srt") {
		t.Error("b.srt should still be failed after compact+reload")
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.MarkSuccess("x.srt")

	if err := s.Delete(); err != nil {
		t.Fatal("Delete:", err)
	}
	// Delete closes the WAL file.

	if _, err := os.Stat(path); err == nil {
		t.Error("snapshot should be deleted")
	}
	if _, err := os.Stat(path + ".wal"); err == nil {
		t.Error("WAL should be deleted")
	}
}
