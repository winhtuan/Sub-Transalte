package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/term"
)

// ─── ANSI helpers ───────────────────────────────────────────────────

const (
	escClear     = "\033[2J\033[H" // clear screen + cursor home
	escCursorUp  = "\033[%dA"
	escClearLine = "\033[2K"
)

// ─── Key codes ──────────────────────────────────────────────────────

type keyCode int

const (
	keyUp keyCode = iota
	keyDown
	keyEnter
	keyBackspace
	keyEsc
	keyChar
)

type keyEvent struct {
	code keyCode
	ch   byte
}

// readKey reads a single key press from raw terminal.
// On Windows, non-keyboard events (mouse, focus) can produce 0-byte reads;
// we retry those instead of treating them as Esc.
func readKey(fd int) keyEvent {
	for {
		buf := make([]byte, 3)
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return keyEvent{code: keyEsc}
		}
		if n == 0 {
			// Windows console non-keyboard event — ignore and retry.
			continue
		}

		switch {
		case buf[0] == 13 || buf[0] == 10: // Enter
			return keyEvent{code: keyEnter}
		case buf[0] == 27: // Escape sequence
			if n == 1 {
				return keyEvent{code: keyEsc}
			}
			if n >= 3 && buf[1] == '[' {
				switch buf[2] {
				case 'A':
					return keyEvent{code: keyUp}
				case 'B':
					return keyEvent{code: keyDown}
				}
			}
			return keyEvent{code: keyEsc}
		case buf[0] == 127 || buf[0] == 8: // Backspace
			return keyEvent{code: keyBackspace}
		default:
			return keyEvent{code: keyChar, ch: buf[0]}
		}
	}
}

// ─── Directory browser ─────────────────────────────────────────────

// browseDirectory opens an interactive folder browser starting at startPath.
// If startWithDrives is true on Windows, it jumps to the drive picker immediately.
// Returns the selected directory path, or empty string if cancelled.
func browseDirectory(startPath string, startWithDrives bool) (string, bool) {
	// Save and restore terminal state.
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return "", false
	}
	defer term.Restore(fd, oldState)

	current := startPath
	if current == "" || current == "." {
		current, _ = os.Getwd()
	}

	// Normalize path.
	current, _ = filepath.Abs(current)

	initialSelection := ""
	if startPath != "" && startPath != "." {
		// We have a specific previously selected path.
		// Skip drive picker because the user is already navigating a specific tree.
		startWithDrives = false

		// Try to jump to the parent so the user can easily select sibling folders.
		parent := filepath.Dir(current)
		if parent != current {
			initialSelection = filepath.Base(current)
			current = parent
		}
	}

	cursor := 0
	scrollOffset := 0
	maxVisible := 15 // max folders visible at once
	backStack := []string{}
	fwdStack := []string{}
	lastRenderedLines := 0

	// If startWithDrives is true and we're on Windows, jump to drive picker first.
	if startWithDrives && runtime.GOOS == "windows" {
		drives := listDrives()
		if len(drives) > 0 {
			current = pickDrive(fd, drives, 0)
			// Reset rendered lines because pickDrive might have printed.
			lastRenderedLines = 0
		}
	}

	for {
		// List contents.
		entries := listFolders(current)

		// Set initial cursor if provided.
		if initialSelection != "" {
			for i, name := range entries {
				if name == initialSelection {
					cursor = i
					break
				}
			}
			initialSelection = "" // only do this once
		}

		// Clamp cursor.
		if cursor >= len(entries) {
			cursor = len(entries) - 1
		}
		if cursor < 0 {
			cursor = 0
		}

		// Adjust scroll.
		if cursor < scrollOffset {
			scrollOffset = cursor
		}
		if cursor >= scrollOffset+maxVisible {
			scrollOffset = cursor - maxVisible + 1
		}

		// Render.
		lastRenderedLines = renderBrowser(current, entries, cursor, scrollOffset, maxVisible, lastRenderedLines)

		// Read key.
		key := readKey(fd)
		switch key.code {
		case keyUp:
			if cursor > 0 {
				cursor--
			}

		case keyDown:
			if cursor < len(entries)-1 {
				cursor++
			}

		case keyEnter:
			if len(entries) > 0 && cursor < len(entries) {
				selected := entries[cursor]
				newPath := filepath.Join(current, selected)
				info, err := os.Stat(newPath)
				if err == nil && info.IsDir() {
					backStack = append(backStack, current)
					fwdStack = fwdStack[:0]
					current = newPath
					cursor = 0
					scrollOffset = 0
				}
			}

		case keyChar:
			switch key.ch {
			case 'b', 'B': // Back
				parent := filepath.Dir(current)
				if parent != current {
					backStack = append(backStack, current)
					fwdStack = fwdStack[:0]
					current = parent
					cursor = 0
					scrollOffset = 0
				} else if len(backStack) > 0 {
					fwdStack = append(fwdStack, current)
					current = backStack[len(backStack)-1]
					backStack = backStack[:len(backStack)-1]
					cursor = 0
					scrollOffset = 0
				}

			case 'n', 'N': // Forward
				if len(fwdStack) > 0 {
					backStack = append(backStack, current)
					current = fwdStack[len(fwdStack)-1]
					fwdStack = fwdStack[:len(fwdStack)-1]
					cursor = 0
					scrollOffset = 0
				}

			case ' ', 's', 'S': // Select current directory
				// Clear and show selection.
				clearLines(lastRenderedLines)
				fmt.Printf("\r%s✓ Đã chọn: %s%s%s\r\n", colorGreen, colorBold, current, colorReset)
				return current, true

			case 'q', 'Q': // Cancel
				clearLines(lastRenderedLines)
				fmt.Printf("\r%s⚠ Đã hủy chọn thư mục%s\r\n", colorYellow, colorReset)
				return "", false

			case 'd', 'D': // Drive selection (Windows)
				if runtime.GOOS == "windows" {
					drives := listDrives()
					if len(drives) > 0 {
						backStack = append(backStack, current)
						fwdStack = fwdStack[:0]
						// Show drive picker — reuse same mechanism.
						current = pickDrive(fd, drives, lastRenderedLines)
						cursor = 0
						scrollOffset = 0
					}
				}
			}

		case keyEsc:
			clearLines(lastRenderedLines)
			fmt.Printf("\r%s⚠ Đã hủy chọn thư mục%s\r\n", colorYellow, colorReset)
			return "", false

		case keyBackspace: // Go to parent
			parent := filepath.Dir(current)
			if parent != current {
				backStack = append(backStack, current)
				fwdStack = fwdStack[:0]
				current = parent
				cursor = 0
				scrollOffset = 0
			}
		}
	}
}

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

// ─── Render ─────────────────────────────────────────────────────────

func renderBrowser(currentPath string, entries []string, cursor, scrollOffset, maxVisible, prevLines int) int {
	// Clear previous output.
	clearLines(prevLines)

	lineCount := 0

	// Current path.
	fmt.Printf("\r  %sThư mục hiện tại: %s%s%s\r\n", colorCyan, colorBold, currentPath, colorReset)
	lineCount++

	fmt.Printf("\r  %s%s%s\r\n", colorDim, strings.Repeat("─", 48), colorReset)
	lineCount++

	if len(entries) == 0 {
		fmt.Printf("\r  %s(Thư mục trống)%s\r\n", colorDim, colorReset)
		lineCount++
	} else {
		end := scrollOffset + maxVisible
		if end > len(entries) {
			end = len(entries)
		}

		for i := scrollOffset; i < end; i++ {
			name := entries[i]
			if i == cursor {
				fmt.Printf("\r  %s%s▸ %s%s\r\n", colorGreen, colorBold, name, colorReset)
			} else {
				fmt.Printf("\r    %s%s%s\r\n", colorDim, name, colorReset)
			}
			lineCount++
		}

		// Scroll indicators.
		if scrollOffset > 0 {
			fmt.Printf("\r  %s  ↑ thêm %d mục%s\r\n", colorDim, scrollOffset, colorReset)
			lineCount++
		}
		if end < len(entries) {
			fmt.Printf("\r  %s  ↓ thêm %d mục%s\r\n", colorDim, len(entries)-end, colorReset)
			lineCount++
		}
	}

	fmt.Printf("\r  %s%s%s\r\n", colorDim, strings.Repeat("─", 48), colorReset)
	lineCount++

	// Help bar.
	help := "Navigate: ↑↓  Open: Enter  Select: Space History: b/n  Drive: d  Quit: q"
	fmt.Printf("\r  %s%s%s\r\n", colorDim, help, colorReset)
	lineCount++

	return lineCount
}

func clearLines(n int) {
	for i := 0; i < n; i++ {
		fmt.Printf(escCursorUp, 1)
		fmt.Printf("%s\r", escClearLine)
	}
}

// ─── Drive / folder listing ─────────────────────────────────────────

func listFolders(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()

	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil
	}

	var folders []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			folders = append(folders, e.Name())
		}
	}
	sort.Strings(folders)
	return folders
}

func listDrives() []string {
	var drives []string
	for c := 'A'; c <= 'Z'; c++ {
		drive := string(c) + ":\\"
		if _, err := os.Stat(drive); err == nil {
			drives = append(drives, drive)
		}
	}
	return drives
}

func pickDrive(fd int, drives []string, prevLines int) string {
	cursor := 0

	for {
		clearLines(prevLines)

		lineCount := 0
		fmt.Printf("\r  %sChọn ổ đĩa%s\r\n", colorCyan, colorReset)
		lineCount++

		fmt.Printf("\r  %s%s%s\r\n", colorDim, strings.Repeat("─", 48), colorReset)
		lineCount++

		for i, d := range drives {
			if i == cursor {
				fmt.Printf("\r  %s%s▸ %s%s\r\n", colorGreen, colorBold, d, colorReset)
			} else {
				fmt.Printf("\r    %s%s%s\r\n", colorDim, d, colorReset)
			}
			lineCount++
		}

		fmt.Printf("\r  %s%s%s\r\n", colorDim, strings.Repeat("─", 48), colorReset)
		lineCount++

		fmt.Printf("\r  %s↑↓: Chọn  Enter: Mở  Esc: Hủy%s\r\n", colorDim, colorReset)
		lineCount++

		prevLines = lineCount

		key := readKey(fd)
		switch key.code {
		case keyUp:
			if cursor > 0 {
				cursor--
			}
		case keyDown:
			if cursor < len(drives)-1 {
				cursor++
			}
		case keyEnter:
			return drives[cursor]
		case keyEsc:
			return drives[0]
		}
	}
}
