package srt

import (
	"fmt"
	"io"
	"strings"
)

// Write outputs a slice of Subtitle blocks to the writer in standard SRT format.
// Uses UTF-8 encoding without BOM, with \r\n line endings for maximum compatibility.
func Write(w io.Writer, subtitles []Subtitle) error {
	for i, sub := range subtitles {
		// Write index.
		if _, err := fmt.Fprintf(w, "%d\r\n", sub.Index); err != nil {
			return fmt.Errorf("writing subtitle %d index: %w", sub.Index, err)
		}

		// Write timestamp.
		if _, err := fmt.Fprintf(w, "%s\r\n", sub.Timestamp); err != nil {
			return fmt.Errorf("writing subtitle %d timestamp: %w", sub.Index, err)
		}

		// Write text lines.
		text := strings.Join(sub.Lines, "\r\n")
		if _, err := fmt.Fprintf(w, "%s\r\n", text); err != nil {
			return fmt.Errorf("writing subtitle %d text: %w", sub.Index, err)
		}

		// Blank line between subtitle blocks (except after the last one).
		if i < len(subtitles)-1 {
			if _, err := fmt.Fprint(w, "\r\n"); err != nil {
				return fmt.Errorf("writing separator after subtitle %d: %w", sub.Index, err)
			}
		}
	}

	return nil
}
