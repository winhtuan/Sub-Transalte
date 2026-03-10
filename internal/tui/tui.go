// Package tui provides an interactive console UI for the Sub-Translate tool.
// It offers a menu-driven interface for scanning and translating SRT files.
package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"subtranslate/internal/config"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

// App holds the interactive TUI application state.
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	reader *bufio.Reader
}

// New creates a new interactive TUI application.
func New(cfg *config.Config, logger *slog.Logger) *App {
	return &App{
		cfg:    cfg,
		logger: logger,
		reader: bufio.NewReader(os.Stdin),
	}
}

// Run starts the interactive console UI loop.
func (a *App) Run(ctx context.Context) {
	a.printBanner()

	options := []string{
		"Tìm file subtitle (.srt)",
		"Dịch file subtitle",
		"Xem cài đặt hiện tại",
		"Thay đổi cài đặt",
		"Thoát",
	}

	for {
		// Check for context cancellation.
		select {
		case <-ctx.Done():
			fmt.Printf("\n%sTạm biệt!%s\n\n", colorCyan, colorReset)
			return
		default:
		}

		choiceIdx, ok := promptMenu("MENU CHÍNH", options)
		if !ok || choiceIdx == 4 { // Esc or "Thoát"
			fmt.Printf("\n%sTạm biệt!%s\n\n", colorCyan, colorReset)
			return
		}

		switch choiceIdx {
		case 0:
			a.scanFiles()
		case 1:
			a.translateFiles()
		case 2:
			a.showSettings()
		case 3:
			a.changeSettings()
		}
	}
}

// ─── Banner ──────────────────────────────────────────────────────────

func (a *App) printBanner() {
	fmt.Printf(`
%s╔═══════════════════════════════════════════════╗
║                                               ║
║   %sSub-Translate%s                               ║
║   %sDịch subtitle EN → VI tự động%s               ║
║                                               ║
╚═══════════════════════════════════════════════╝%s
`, colorCyan, colorBold, colorCyan, colorDim, colorCyan, colorReset)
}

// ─── Directory Selection ──────────────────────────────────────────────

// chooseDirectory shows a menu to select the input directory, offering the
// previously used directory as a quick option if available.
func (a *App) chooseDirectory() (string, bool) {
	dir := a.cfg.InputDir
	hasPrev := dir != "" && dir != "."
	if hasPrev {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			hasPrev = false
		}
	}

	options := []string{
		"Chọn từ ổ đĩa/thư mục (Browse)",
		"Nhập đường dẫn thư mục thủ công",
	}

	if hasPrev {
		options = append([]string{fmt.Sprintf("Thư mục đã chọn trước đó (%s)", filepath.Base(dir))}, options...)
	}

	choiceIdx, ok := promptMenu("CHỌN PHƯƠNG THỨC NHẬP THƯ MỤC", options)
	if !ok {
		return "", false
	}

	if hasPrev {
		if choiceIdx == 0 {
			return dir, true
		}
		choiceIdx--
	}

	if choiceIdx == 0 {
		return browseDirectory(a.cfg.InputDir, true)
	}
	dirStr, err := a.promptWithDefault("Thư mục cần xử lý", a.cfg.InputDir)
	return dirStr, err == nil
}

// ─── Helpers ─────────────────────────────────────────────────────────

// prompt displays a prompt and reads user input.
// Returns an error if stdin is closed (EOF) or unreadable.
func (a *App) prompt(label string) (string, error) {
	fmt.Printf("\n%s%s ▶ %s", colorCyan, label, colorReset)
	input, err := a.reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return strings.TrimSpace(input), err
		}
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// promptWithDefault displays a prompt with a default value.
// Returns an error if stdin is closed (EOF) or unreadable.
func (a *App) promptWithDefault(label, defaultVal string) (string, error) {
	fmt.Printf("\n%s%s%s [%s%s%s] %s▶ %s",
		colorCyan, label, colorReset,
		colorDim, defaultVal, colorReset,
		colorCyan, colorReset)
	input, err := a.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return defaultVal, nil
	}
	return input, nil
}

// pauseForEnter waits for the user to press Enter.
func (a *App) pauseForEnter() {
	fmt.Printf("\n%sNhấn Enter để tiếp tục...%s", colorDim, colorReset)
	a.reader.ReadString('\n')
}
