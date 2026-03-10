// Package ui provides shared ANSI terminal styling constants and helpers
// used by the orchestrator dashboard and the report printer.
package ui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ANSI color / style escape sequences.
const (
	Reset   = "\033[0m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"

	ClearLine = "\033[2K"
	CursorUp  = "\033[%dA"

	// BoxInner is the number of visible characters between the box borders
	// (including the 1-space margin on each side).
	BoxInner = 51
)

// ansiRe matches ANSI escape sequences for stripping purposes.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// VisibleLen returns the number of visible (non-ANSI) rune characters in s.
func VisibleLen(s string) int {
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

// BoxTop / BoxMid / BoxBot draw the horizontal box borders with an optional
// leading indent string (e.g. "  " for dashboard, "" for report).
func BoxTop(indent string) string {
	return fmt.Sprintf("%s%s%s╔%s╗%s", indent, Cyan, Bold, strings.Repeat("═", BoxInner), Reset)
}

func BoxMid(indent string) string {
	return fmt.Sprintf("%s%s%s╠%s╣%s", indent, Cyan, Bold, strings.Repeat("═", BoxInner), Reset)
}

func BoxBot(indent string) string {
	return fmt.Sprintf("%s%s%s╚%s╝%s", indent, Cyan, Bold, strings.Repeat("═", BoxInner), Reset)
}

// BoxRow wraps content in ║ … ║ with correct right-padding.
// indent is prepended before the left border.
// content that exceeds the box width is NOT truncated here; callers that need
// truncation (e.g. the live dashboard) should pre-truncate before calling.
func BoxRow(indent, content string) string {
	maxContent := BoxInner - 2 // 1-space margin each side
	vLen := VisibleLen(content)
	pad := maxContent - vLen
	if pad < 0 {
		pad = 0
	}
	return fmt.Sprintf("%s%s%s║%s %s%s %s%s║%s",
		indent, Cyan, Bold, Reset,
		content, strings.Repeat(" ", pad),
		Cyan, Bold, Reset)
}
