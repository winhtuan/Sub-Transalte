package tui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// promptMenu displays an interactive menu with options.
// Returns the index of the selected option, or -1 if cancelled.
func promptMenu(title string, options []string) (int, bool) {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return -1, false
	}
	defer term.Restore(fd, oldState)

	cursor := 0
	lastRenderedLines := 0

	for {
		// Render
		clearLines(lastRenderedLines)
		lineCount := 0

		fmt.Printf("\r  %s%s%s%s\r\n", colorCyan, colorBold, title, colorReset)
		lineCount++

		fmt.Printf("\r  %s%s%s\r\n", colorDim, strings.Repeat("─", 48), colorReset)
		lineCount++

		for i, opt := range options {
			if i == cursor {
				fmt.Printf("\r  %s%s▸ %s%s\r\n", colorGreen, colorBold, opt, colorReset)
			} else {
				fmt.Printf("\r    %s%s%s\r\n", colorDim, opt, colorReset)
			}
			lineCount++
		}

		fmt.Printf("\r  %s%s%s\r\n", colorDim, strings.Repeat("─", 48), colorReset)
		lineCount++

		fmt.Printf("\r  %s↑↓: Chọn  Enter: Xác nhận  Esc: Hủy%s\r\n", colorDim, colorReset)
		lineCount++

		lastRenderedLines = lineCount

		key := readKey(fd)
		switch key.code {
		case keyUp:
			if cursor > 0 {
				cursor--
			}
		case keyDown:
			if cursor < len(options)-1 {
				cursor++
			}
		case keyEnter:
			clearLines(lastRenderedLines)
			return cursor, true
		case keyEsc:
			clearLines(lastRenderedLines)
			return -1, false
		}
	}
}
