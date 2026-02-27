// Package srt provides parsing and writing for SubRip (.srt) subtitle files.
// It handles UTF-8 BOM, multi-line subtitles, and HTML formatting tags.
package srt

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// utf8BOM is the byte order mark for UTF-8 encoded files.
var utf8BOM = "\xEF\xBB\xBF"

// timestampRegex matches SRT timestamp lines like "00:01:23,456 --> 00:01:25,789".
var timestampRegex = regexp.MustCompile(
	`^\d{2}:\d{2}:\d{2},\d{3}\s*-->\s*\d{2}:\d{2}:\d{2},\d{3}`,
)

// Subtitle represents a single subtitle block in an SRT file.
type Subtitle struct {
	Index     int      // Sequence number (1-based)
	Timestamp string   // Timestamp line, preserved exactly as-is
	Lines     []string // Text content lines (may contain HTML tags)
}

// Parse reads an SRT file from the reader and returns a slice of Subtitle blocks.
// It handles UTF-8 BOM detection, multi-line text, and preserves HTML formatting.
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
			// Skip empty lines between subtitle blocks.
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}

			index, err := strconv.Atoi(trimmed)
			if err != nil {
				return nil, fmt.Errorf("line %d: expected subtitle index, got %q", lineNum, line)
			}
			current = &Subtitle{Index: index}
			state = stateTimestamp

		case stateTimestamp:
			trimmed := strings.TrimSpace(line)
			if !timestampRegex.MatchString(trimmed) {
				return nil, fmt.Errorf("line %d: expected timestamp, got %q", lineNum, line)
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
