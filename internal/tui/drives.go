package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

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
