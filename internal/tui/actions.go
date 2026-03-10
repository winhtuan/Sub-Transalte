package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"subtranslate/internal/orchestrator"
	"subtranslate/internal/report"
	"subtranslate/internal/scanner"
)

// ─── Feature: Scan Files ─────────────────────────────────────────────

func (a *App) scanFiles() {
	fmt.Printf("\n%sTÌM FILE SUBTITLE%s\n", colorBold, colorReset)
	fmt.Printf("%s─────────────────────────────────────%s\n", colorDim, colorReset)

	dir, ok := a.chooseDirectory()
	if !ok || dir == "" {
		return
	}

	// Validate directory.
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Printf("%s✗ Thư mục không tồn tại: %s%s\n", colorRed, dir, colorReset)
		return
	}

	// Update config.
	a.cfg.InputDir = dir

	fmt.Printf("\n%sĐang quét thư mục: %s%s\n", colorDim, dir, colorReset)
	result, err := scanner.Scan(dir, a.cfg.Overwrite)
	if err != nil {
		fmt.Printf("%s✗ Lỗi khi quét: %v%s\n", colorRed, err, colorReset)
		return
	}

	// Display results.
	totalFound := len(result.Files)
	totalSkipped := len(result.Skipped)

	fmt.Printf("\n%sKết quả quét:%s\n", colorBold, colorReset)
	fmt.Printf("   Tổng file tìm thấy:  %s%d%s\n", colorGreen, totalFound+totalSkipped, colorReset)
	fmt.Printf("   Cần dịch:            %s%d%s\n", colorGreen, totalFound, colorReset)
	fmt.Printf("   Đã dịch (bỏ qua):   %s%d%s\n", colorYellow, totalSkipped, colorReset)

	if totalFound > 0 {
		fmt.Printf("\n%sDanh sách file cần dịch:%s\n", colorBold, colorReset)
		for i, f := range result.Files {
			relPath, _ := filepath.Rel(dir, f)
			if relPath == "" {
				relPath = f
			}
			fmt.Printf("   %s%d.%s %s\n", colorCyan, i+1, colorReset, relPath)
		}
	}

	if totalSkipped > 0 {
		fmt.Printf("\n%s⊘ File đã dịch (bỏ qua):%s\n", colorDim, colorReset)
		for _, f := range result.Skipped {
			relPath, _ := filepath.Rel(dir, f)
			if relPath == "" {
				relPath = f
			}
			fmt.Printf("   %s⊘ %s%s\n", colorDim, relPath, colorReset)
		}
	}

	fmt.Println()
}

// ─── Feature: Translate Files ────────────────────────────────────────

func (a *App) translateFiles() {
	fmt.Printf("\n%sDỊCH FILE SUBTITLE%s\n", colorBold, colorReset)
	fmt.Printf("%s─────────────────────────────────────%s\n", colorDim, colorReset)

	dir, ok := a.chooseDirectory()
	if !ok || dir == "" {
		return
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Printf("%s✗ Thư mục không tồn tại: %s%s\n", colorRed, dir, colorReset)
		return
	}
	a.cfg.InputDir = dir

	// Quick scan preview.
	result, err := scanner.Scan(dir, a.cfg.Overwrite)
	if err != nil {
		fmt.Printf("%s✗ Lỗi khi quét: %v%s\n", colorRed, err, colorReset)
		return
	}

	if len(result.Files) == 0 {
		fmt.Printf("\n%s⚠ Không tìm thấy file *_en.srt nào cần dịch.%s\n", colorYellow, colorReset)
		return
	}

	// Show summary and confirm.
	fmt.Printf("\n%sSẽ dịch %d file:%s\n", colorBold, len(result.Files), colorReset)
	for i, f := range result.Files {
		relPath, _ := filepath.Rel(dir, f)
		if relPath == "" {
			relPath = f
		}
		fmt.Printf("   %s%d.%s %s → %s\n", colorCyan, i+1, colorReset,
			filepath.Base(f), filepath.Base(scanner.OutputPath(f)))
	}

	confirmOptions := []string{"Bắt đầu dịch ngay", "Hủy bỏ"}
	confirmIdx, ok := promptMenu("XÁC NHẬN DỊCH", confirmOptions)
	if !ok || confirmIdx != 0 {
		fmt.Printf("%s⚠ Đã hủy.%s\n", colorYellow, colorReset)
		return
	}

	// Validate config before running.
	if err := a.cfg.Validate(); err != nil {
		fmt.Printf("%s✗ Lỗi cấu hình: %v%s\n", colorRed, err, colorReset)
		return
	}

	// Run translation with a fresh context for graceful shutdown.
	// We create a new signal context here instead of reusing the parent one,
	// because on Windows, stdin reading during prompts can inadvertently
	// trigger signal events that cancel the parent context.
	translateCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println()
	stats, err := orchestrator.Run(translateCtx, a.cfg, a.logger)
	if err != nil {
		fmt.Printf("\n%s✗ Lỗi: %v%s\n", colorRed, err, colorReset)
	}

	// Print report.
	report.Print(os.Stdout, stats)
	a.pauseForEnter()
}

// ─── Feature: Settings ───────────────────────────────────────────────

func (a *App) showSettings() {
	fmt.Printf(`
%sCÀI ĐẶT HIỆN TẠI%s
%s─────────────────────────────────────%s
   Thư mục:          %s%s%s
   API URL:          %s%s%s
   Batch size:       %s%d%s dòng/request
   Concurrency:      %s%d%s workers
   Max retries:      %s%d%s
   Rate limit:       %s%.0f%s req/s
   Request delay:    %s%s%s
   Timeout:          %s%s%s
   Overwrite:        %s%v%s
   Verbose:          %s%v%s
%s─────────────────────────────────────%s
`,
		colorBold, colorReset,
		colorDim, colorReset,
		colorGreen, a.cfg.InputDir, colorReset,
		colorGreen, a.cfg.APIUrl, colorReset,
		colorCyan, a.cfg.BatchSize, colorReset,
		colorCyan, a.cfg.MaxConcurrency, colorReset,
		colorCyan, a.cfg.MaxRetries, colorReset,
		colorCyan, a.cfg.RateLimit, colorReset,
		colorCyan, a.cfg.RequestDelay, colorReset,
		colorCyan, a.cfg.Timeout, colorReset,
		colorCyan, a.cfg.Overwrite, colorReset,
		colorCyan, a.cfg.Verbose, colorReset,
		colorDim, colorReset,
	)
}

func (a *App) changeSettings() {
	fmt.Printf("\n%sTHAY ĐỔI CÀI ĐẶT%s\n", colorBold, colorReset)
	fmt.Printf("%s─────────────────────────────────────%s\n", colorDim, colorReset)
	options := []string{
		"Thư mục (" + a.cfg.InputDir + ")",
		"API URL (" + a.cfg.APIUrl + ")",
		"Batch size (" + fmt.Sprintf("%d", a.cfg.BatchSize) + ")",
		"Concurrency (" + fmt.Sprintf("%d", a.cfg.MaxConcurrency) + ")",
		"Rate limit (" + fmt.Sprintf("%.0f", a.cfg.RateLimit) + ")",
		"Request delay (" + a.cfg.RequestDelay.String() + ")",
		"Overwrite (" + fmt.Sprintf("%v", a.cfg.Overwrite) + ")",
		"Verbose (" + fmt.Sprintf("%v", a.cfg.Verbose) + ")",
	}

	choiceIdx, ok := promptMenu("THAY ĐỔI CÀI ĐẶT", options)
	if !ok {
		return
	}

	switch choiceIdx {
	case 0:
		dir, ok := browseDirectory(a.cfg.InputDir, true)
		if ok && dir != "" {
			a.cfg.InputDir = dir
		}
	case 1:
		v, _ := a.promptWithDefault("API URL mới", a.cfg.APIUrl)
		a.cfg.APIUrl = v
		fmt.Printf("%s✓ Đã cập nhật API URL: %s%s\n", colorGreen, v, colorReset)
	case 2:
		v, _ := a.promptWithDefault("Batch size mới", fmt.Sprintf("%d", a.cfg.BatchSize))
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			a.cfg.BatchSize = n
			fmt.Printf("%s✓ Đã cập nhật batch size: %d%s\n", colorGreen, n, colorReset)
		} else {
			fmt.Printf("%s✗ Giá trị không hợp lệ%s\n", colorRed, colorReset)
		}
	case 3:
		v, _ := a.promptWithDefault("Concurrency mới", fmt.Sprintf("%d", a.cfg.MaxConcurrency))
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			a.cfg.MaxConcurrency = n
			fmt.Printf("%s✓ Đã cập nhật concurrency: %d%s\n", colorGreen, n, colorReset)
		} else {
			fmt.Printf("%s✗ Giá trị không hợp lệ%s\n", colorRed, colorReset)
		}
	case 4:
		v, _ := a.promptWithDefault("Rate limit mới (req/s)", fmt.Sprintf("%.0f", a.cfg.RateLimit))
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			a.cfg.RateLimit = n
			fmt.Printf("%s✓ Đã cập nhật rate limit: %.0f%s\n", colorGreen, n, colorReset)
		} else {
			fmt.Printf("%s✗ Giá trị không hợp lệ%s\n", colorRed, colorReset)
		}
	case 5:
		v, _ := a.promptWithDefault("Request delay mới (vd: 200ms, 1s)", a.cfg.RequestDelay.String())
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			a.cfg.RequestDelay = d
			fmt.Printf("%s✓ Đã cập nhật request delay: %s%s\n", colorGreen, d, colorReset)
		} else {
			fmt.Printf("%s✗ Giá trị không hợp lệ (vd: 200ms, 500ms, 1s)%s\n", colorRed, colorReset)
		}
	case 6:
		a.cfg.Overwrite = !a.cfg.Overwrite
		fmt.Printf("%s✓ Overwrite: %v%s\n", colorGreen, a.cfg.Overwrite, colorReset)
	case 7:
		a.cfg.Verbose = !a.cfg.Verbose
		fmt.Printf("%s✓ Verbose: %v%s\n", colorGreen, a.cfg.Verbose, colorReset)
	}
}
