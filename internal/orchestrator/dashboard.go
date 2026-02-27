// Package orchestrator coordinates the subtitle translation pipeline.
// This file (dashboard.go) manages the dashboard state, worker progress tracking,
// and the logic for rendering the live terminal UI.
package orchestrator

import (
	"fmt"
	"sync"
	"time"

	"subtranslate/internal/config"
	"subtranslate/internal/translator"
)

// ═══════════════════════════════════════════════════════════════════════
// Dashboard
// ═══════════════════════════════════════════════════════════════════════

type fileJob struct {
	index    int
	filePath string
}

type workerState struct {
	state     string
	fileName  string
	completed int
	total     int
}

type dashboard struct {
	mu sync.Mutex

	// Config (immutable).
	workerCount  int
	totalFiles   int
	batchSize    int
	rateLimit    float64
	requestDelay time.Duration

	// State.
	workers             []workerState
	doneFiles           int
	startTime           time.Time
	totalLinesProcessed int
	totalCacheHits      int
	client              *translator.Client

	// Rendering.
	lastPrintedLines int
	rendered         bool
	lastRender       time.Time
	logLines         []string
}

func newDashboard(cfg *config.Config, workerCount, totalFiles int, client *translator.Client) *dashboard {
	workers := make([]workerState, workerCount)
	for i := range workers {
		workers[i].state = stateIdle
	}
	return &dashboard{
		workerCount:  workerCount,
		totalFiles:   totalFiles,
		batchSize:    cfg.BatchSize,
		rateLimit:    cfg.RateLimit,
		requestDelay: cfg.RequestDelay,
		workers:      workers,
		startTime:    time.Now(),
		client:       client,
	}
}

func (d *dashboard) setWorkerFile(workerID int, fileName string, totalLines int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := &d.workers[workerID-1]
	w.state = stateTranslating
	w.fileName = fileName
	w.completed = 0
	w.total = totalLines
	d.doRender(false)
}

func (d *dashboard) setWorkerProgress(workerID int, completed, total int, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := &d.workers[workerID-1]
	w.completed = completed
	w.total = total
	if state != "" {
		w.state = state
	}
	d.doRender(true) // throttled
}

func (d *dashboard) markWorkerDone(workerID int, lines, cacheHits int, logMsg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := &d.workers[workerID-1]
	w.state = stateDone
	d.doneFiles++
	d.totalLinesProcessed += lines
	d.totalCacheHits += cacheHits
	if logMsg != "" {
		d.logLines = append(d.logLines, logMsg)
	}
	d.doRender(false)
}

func (d *dashboard) setWorkerIdle(workerID int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.workers[workerID-1] = workerState{state: stateIdle}
}

// ═══════════════════════════════════════════════════════════════════════
// Render
// ═══════════════════════════════════════════════════════════════════════

func (d *dashboard) doRender(throttle bool) {
	now := time.Now()
	if throttle && d.rendered && now.Sub(d.lastRender) < 200*time.Millisecond {
		return
	}
	d.lastRender = now

	// Move cursor up to overwrite previous render.
	if d.rendered && d.lastPrintedLines > 0 {
		fmt.Printf(escCursorUp, d.lastPrintedLines)
	}

	lineCount := 0

	// ── Log lines (scroll above the box) ─────────────────────────
	for _, line := range d.logLines {
		fmt.Printf("%s\r%s\n", escClearLine, line)
		lineCount++
	}
	d.logLines = d.logLines[:0]

	elapsed := time.Since(d.startTime)
	metrics := d.client.GetMetrics()

	// ── ZONE 1: Header ──────────────────────────────────────────
	fmt.Printf("%s\r%s\n", escClearLine, boxTop())
	lineCount++

	fmt.Printf("%s\r%s\n", escClearLine, boxRow(
		fmt.Sprintf("Sub-Translate %s▸%s Đang dịch...  %s%s%s",
			cCyan, cReset, cDim, fmtElapsed(elapsed), cReset)))
	lineCount++

	fmt.Printf("%s\r%s\n", escClearLine, boxRow(
		fmt.Sprintf("W:%s%d%s  B:%s%d%s  R:%s%.0f%s/s  D:%s%s%s  F:%s%d%s/%s%d%s",
			cBold, d.workerCount, cReset,
			cBold, d.batchSize, cReset,
			cBold, d.rateLimit, cReset,
			cBold, d.requestDelay, cReset,
			cGreen, d.doneFiles, cReset,
			cBold, d.totalFiles, cReset)))
	lineCount++

	// ── ZONE 2: Workers ──────────────────────────────────────────
	fmt.Printf("%s\r%s\n", escClearLine, boxMid())
	lineCount++

	for i, w := range d.workers {
		icon, clr := stateDisplay(w.state)
		var content string

		switch w.state {
		case stateIdle:
			content = fmt.Sprintf("%sW%d%s %s%s%s  %sĐang chờ...%s",
				cMagenta, i+1, cReset, clr, icon, cReset, cDim, cReset)

		case stateDone:
			name := truncate(w.fileName, 30)
			content = fmt.Sprintf("%sW%d%s %s%s%s  %s%s%s  %s✓%s",
				cMagenta, i+1, cReset, clr, icon, cReset, cDim, name, cReset, cGreen, cReset)

		default: // translating, retrying, waiting
			pct := 0.0
			if w.total > 0 {
				pct = float64(w.completed) / float64(w.total) * 100
			}
			bar := renderBar(10, pct, clr)
			counts := fmt.Sprintf("%d/%d", w.completed, w.total)
			// W1 ● [██████████]  41% 60/148 filename
			// Fixed: 2+1+1+1+12+1+4+1+len(counts)+1 = 24+len(counts)
			maxName := 49 - 24 - len(counts)
			if maxName < 8 {
				maxName = 8
			}
			name := truncate(w.fileName, maxName)
			content = fmt.Sprintf("%sW%d%s %s%s%s %s %3.0f%% %s%s%s %s",
				cMagenta, i+1, cReset, clr, icon, cReset,
				bar, pct,
				cDim, counts, cReset, name)
		}

		fmt.Printf("%s\r%s\n", escClearLine, boxRow(content))
		lineCount++
	}

	// ── ZONE 3: Metrics ──────────────────────────────────────────
	fmt.Printf("%s\r%s\n", escClearLine, boxMid())
	lineCount++

	// Overall progress.
	overallPct := 0.0
	if d.totalFiles > 0 {
		overallPct = float64(d.doneFiles) / float64(d.totalFiles) * 100
	}
	barColor := cCyan
	if d.doneFiles >= d.totalFiles {
		barColor = cGreen
	}
	bar := renderBar(20, overallPct, barColor)
	fmt.Printf("%s\r%s\n", escClearLine, boxRow(
		fmt.Sprintf("%s  %s%3.0f%%%s  %s(%d/%d files)%s",
			bar, cBold, overallPct, cReset, cDim, d.doneFiles, d.totalFiles, cReset)))
	lineCount++

	// Speed + ETA.
	speed := 0.0
	if elapsed.Seconds() > 0 {
		speed = float64(d.totalLinesProcessed) / elapsed.Seconds()
	}
	eta := "—"
	if d.doneFiles > 0 && d.doneFiles < d.totalFiles {
		avgPerFile := elapsed / time.Duration(d.doneFiles)
		remaining := avgPerFile * time.Duration(d.totalFiles-d.doneFiles)
		eta = "~" + fmtElapsed(remaining)
	}
	fmt.Printf("%s\r%s\n", escClearLine, boxRow(
		fmt.Sprintf("Lines: %s%d%s  Speed: %s%.0f%s/s  ETA: %s%s%s",
			cBold, d.totalLinesProcessed, cReset,
			cGreen, speed, cReset,
			cYellow, eta, cReset)))
	lineCount++

	// Cache + Retries + Errors + Latency.
	cacheRatio := 0.0
	total := d.totalLinesProcessed + d.totalCacheHits
	if total > 0 {
		cacheRatio = float64(d.totalCacheHits) / float64(total) * 100
	}
	avgLat := "—"
	if metrics.AvgLatency > 0 {
		avgLat = fmt.Sprintf("%dms", metrics.AvgLatency.Milliseconds())
	}
	fmt.Printf("%s\r%s\n", escClearLine, boxRow(
		fmt.Sprintf("Cache: %s%.0f%%%s(%d)  Retry: %s%d%s  Err: %s%d%s  Lat: %s%s%s",
			cGreen, cacheRatio, cReset, d.totalCacheHits,
			cYellow, metrics.Retries, cReset,
			cRed, metrics.Errors, cReset,
			cDim, avgLat, cReset)))
	lineCount++

	// Bottom border.
	fmt.Printf("%s\r%s\n", escClearLine, boxBot())
	lineCount++

	// Clear any leftover lines from a previous longer render.
	for lineCount < d.lastPrintedLines {
		fmt.Printf("%s\r\n", escClearLine)
		lineCount++
	}

	d.lastPrintedLines = lineCount
	d.rendered = true
}

func (d *dashboard) finish() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.workers {
		if d.workers[i].state != stateDone {
			d.workers[i].state = stateIdle
		}
	}
	d.doRender(false)
}
