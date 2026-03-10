package tui

import (
	"os"
)

// ─── ANSI escape sequences ───────────────────────────────────────────

const (
	escClear     = "\033[2J\033[H" // clear screen + cursor home
	escCursorUp  = "\033[%dA"
	escClearLine = "\033[2K"
)

// ─── Key codes ───────────────────────────────────────────────────────

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
