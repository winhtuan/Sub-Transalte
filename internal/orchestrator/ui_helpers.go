// Package orchestrator coordinates the subtitle translation pipeline.
// This file (ui_helpers.go) contains ANSI constants, terminal UI formatting,
// string truncation, progress bar rendering, and box drawing utilities.
package orchestrator

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// ═══════════════════════════════════════════════════════════════════════
// ANSI + Box drawing
// ═══════════════════════════════════════════════════════════════════════

const (
	cReset   = "\033[0m"
	cRed     = "\033[31m"
	cGreen   = "\033[32m"
	cYellow  = "\033[33m"
	cBlue    = "\033[34m"
	cMagenta = "\033[35m"
	cCyan    = "\033[36m"
	cBold    = "\033[1m"
	cDim     = "\033[2m"

	escClearLine = "\033[2K"
	escCursorUp  = "\033[%dA"
)

// Box inner width: visible characters between ║ and ║ (including 1-space margins).
const boxInner = 51

// ansiRe strips ANSI escape sequences for visible-width calculation.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// visibleLen returns the number of visible characters in s (ignoring ANSI codes).
func visibleLen(s string) int {
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

// boxTop / boxMid / boxBot draw the horizontal borders.
func boxTop() string {
	return fmt.Sprintf("  %s%s╔%s╗%s", cCyan, cBold, strings.Repeat("═", boxInner), cReset)
}
func boxMid() string {
	return fmt.Sprintf("  %s%s╠%s╣%s", cCyan, cBold, strings.Repeat("═", boxInner), cReset)
}
func boxBot() string {
	return fmt.Sprintf("  %s%s╚%s╝%s", cCyan, cBold, strings.Repeat("═", boxInner), cReset)
}

// boxRow wraps content in ║ ... ║ with correct padding.
// If content exceeds the box width, it is visually truncated.
func boxRow(content string) string {
	maxContent := boxInner - 2 // 1-space margin on each side
	vLen := visibleLen(content)
	if vLen > maxContent {
		// Truncate: walk runes, skip ANSI sequences, cut at maxContent-1 visible chars + "…"
		content = truncateAnsi(content, maxContent-1) + "…"
		vLen = maxContent
	}
	pad := maxContent - vLen
	if pad < 0 {
		pad = 0
	}
	return fmt.Sprintf("  %s%s║%s %s%s %s%s║%s",
		cCyan, cBold, cReset,
		content, strings.Repeat(" ", pad),
		cCyan, cBold, cReset)
}

// truncateAnsi truncates s to maxVisible visible characters, preserving ANSI codes.
func truncateAnsi(s string, maxVisible int) string {
	var b strings.Builder
	visible := 0
	i := 0
	for i < len(s) && visible < maxVisible {
		if s[i] == '\033' && i+1 < len(s) && s[i+1] == '[' {
			// Copy entire ANSI sequence.
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++ // include the final letter
			}
			b.WriteString(s[i:j])
			i = j
		} else {
			// Copy one rune.
			_, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+size])
			i += size
			visible++
		}
	}
	// Append reset to avoid color bleeding.
	b.WriteString(cReset)
	return b.String()
}

// ═══════════════════════════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════════════════════════

func renderBar(width int, pct float64, color string) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return fmt.Sprintf("%s[%s%s]%s",
		color,
		strings.Repeat("█", filled),
		strings.Repeat("░", width-filled),
		cReset)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func fmtElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// ═══════════════════════════════════════════════════════════════════════
// Worker states
// ═══════════════════════════════════════════════════════════════════════

const (
	stateIdle        = "idle"
	stateTranslating = "translating"
	stateRetrying    = "retrying"
	stateWaiting     = "waiting"
	stateDone        = "done"
)

func stateDisplay(state string) (icon, color string) {
	switch state {
	case stateTranslating:
		return "●", cGreen
	case stateRetrying:
		return "↻", cYellow
	case stateWaiting:
		return "◌", cBlue
	case stateDone:
		return "✓", cGreen
	default:
		return "○", cDim
	}
}
