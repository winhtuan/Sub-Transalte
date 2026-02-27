// Package scanner provides recursive file scanning for SRT subtitle files.
package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skippedDirs are directory names that should be skipped during scanning.
var skippedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".vscode":      true,
	".idea":        true,
	"vendor":       true,
}

// Result holds scanning results including found files and skipped files.
type Result struct {
	Files   []string // Paths to *_en.srt files to translate
	Skipped []string // Paths skipped because *_en.vi.srt already exists
}

// Scan recursively walks the root directory and collects all *_en.srt files.
// If overwrite is false, files that already have a corresponding *_en.vi.srt
// are placed in the Skipped list instead.
func Scan(root string, overwrite bool) (*Result, error) {
	result := &Result{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("accessing %q: %w", path, err)
		}

		// Skip hidden and known irrelevant directories.
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			if skippedDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}

		// Match *_en.srt files (case-insensitive).
		if !d.Type().IsRegular() {
			return nil
		}
		lower := strings.ToLower(d.Name())
		if !strings.HasSuffix(lower, "_en.srt") {
			return nil
		}

		// Check if translated file already exists.
		outputPath := OutputPath(path)
		if !overwrite {
			if _, err := os.Stat(outputPath); err == nil {
				result.Skipped = append(result.Skipped, path)
				return nil
			}
		}

		result.Files = append(result.Files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning directory %q: %w", root, err)
	}

	return result, nil
}

// OutputPath converts a source SRT path to its translated output path.
// Example: "movie_en.srt" → "movie_en.vi.srt"
func OutputPath(srcPath string) string {
	ext := filepath.Ext(srcPath)             // ".srt"
	base := strings.TrimSuffix(srcPath, ext) // "movie_en"
	return base + ".vi" + ext                // "movie_en.vi.srt"
}
