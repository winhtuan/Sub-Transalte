// Package srt provides parsing and writing for SubRip (.srt) subtitle files.
// It handles UTF-8 BOM, multi-line subtitles, and HTML formatting tags.
package srt

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

// utf8BOM is the byte order mark for UTF-8 encoded files.
var utf8BOM = "\xEF\xBB\xBF"

// timestampRegex matches SRT timestamp lines like "00:01:23,456 --> 00:01:25,789".
var timestampRegex = regexp.MustCompile(
	`^\d{2,}:\d{2}:\d{2},\d{3}\s*-->\s*\d{2,}:\d{2}:\d{2},\d{3}`,
)

// Subtitle represents a single subtitle block in an SRT file.
type Subtitle struct {
	Index     int      // Sequence number (1-based)
	Timestamp string   // Timestamp line, preserved exactly as-is
	Lines     []string // Text content lines (may contain HTML tags)
}

// Parse reads an SRT file from the reader and returns a slice of Subtitle blocks.
// It handles UTF-8 BOM detection, multi-line text, and preserves HTML formatting.
//
// Malformed blocks (bad index or bad/out-of-range timestamp) are logged and
// skipped rather than aborting the parse, so a single corrupt block does not
// prevent the rest of the file from being processed.
func Parse(r io.Reader) ([]Subtitle, error) {
	scanner := bufio.NewScanner(r)
	// Increase buffer for potentially large lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var subtitles []Subtitle
	var current *Subtitle
	firstLine := true

	// Parser states.
	const (
		stateIndex     = iota // Expecting subtitle index
		stateTimestamp        // Expecting timestamp line
		stateText             // Reading text lines
		stateSkipping         // Skipping a malformed block until the next blank line
	)
	state := stateIndex

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Strip UTF-8 BOM from the very first line if present.
		if firstLine {
			line = strings.TrimPrefix(line, utf8BOM)
			firstLine = false
		}

		// Normalize: strip carriage return (handle \r\n on any OS).
		line = strings.TrimRight(line, "\r")

		switch state {
		case stateIndex:
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			_, err := strconv.Atoi(trimmed)
			if err != nil {
				slog.Default().Warn("srt: bad subtitle index — skipping block",
					"line", lineNum, "content", line)
				state = stateSkipping
				continue
			}
			idx, _ := strconv.Atoi(trimmed)
			current = &Subtitle{Index: idx}
			state = stateTimestamp

		case stateTimestamp:
			trimmed := strings.TrimSpace(line)
			if !timestampRegex.MatchString(trimmed) {
				slog.Default().Warn("srt: bad timestamp format — skipping block",
					"line", lineNum, "content", line)
				current = nil
				state = stateSkipping
				continue
			}
			if err := validateTimestamp(trimmed); err != nil {
				slog.Default().Warn("srt: timestamp out of range — skipping block",
					"line", lineNum, "content", line, "error", err)
				current = nil
				state = stateSkipping
				continue
			}
			current.Timestamp = trimmed
			state = stateText

		case stateText:
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				// Empty line marks end of this subtitle block.
				if current != nil && len(current.Lines) > 0 {
					subtitles = append(subtitles, *current)
				}
				current = nil
				state = stateIndex
			} else {
				// Accumulate text lines (preserving HTML tags, formatting).
				current.Lines = append(current.Lines, line)
			}

		case stateSkipping:
			// Consume lines until a blank line, then resume normal parsing.
			if strings.TrimSpace(line) == "" {
				current = nil
				state = stateIndex
			}
		}
	}

	// Handle the last subtitle block (file may not end with blank line).
	if current != nil && len(current.Lines) > 0 {
		subtitles = append(subtitles, *current)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading SRT: %w", err)
	}

	return subtitles, nil
}

// validateTimestamp checks that both timestamps in a line like
// "HH:MM:SS,mmm --> HH:MM:SS,mmm" contain in-range component values.
//
// Validation rules (per SRT spec):
//   - HH: < 100 (no strict 24 h limit; subtitle files may span long recordings)
//   - MM: < 60
//   - SS: < 60
//   - mmm: < 1000
func validateTimestamp(ts string) error {
	parts := strings.SplitN(ts, "-->", 2)
	if len(parts) != 2 {
		return fmt.Errorf("missing --> separator")
	}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		var h, m, s, ms int
		n, err := fmt.Sscanf(part, "%d:%d:%d,%d", &h, &m, &s, &ms)
		if err != nil || n != 4 {
			return fmt.Errorf("cannot parse timestamp %q: %v", part, err)
		}
		if h >= 100 || m >= 60 || s >= 60 || ms >= 1000 {
			return fmt.Errorf("timestamp component out of range in %q (H=%d M=%d S=%d ms=%d)",
				part, h, m, s, ms)
		}
	}
	return nil
}
