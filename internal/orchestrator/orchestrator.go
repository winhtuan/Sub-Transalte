// Package orchestrator coordinates the subtitle translation pipeline.
// This file (orchestrator.go) contains the main Run() entry point,
// which orchestrates scanning, caching, the worker pool, and adaptive throttling.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"subtranslate/internal/config"
	"subtranslate/internal/jobstate"
	"subtranslate/internal/report"
	"subtranslate/internal/scanner"
	"subtranslate/internal/throttle"
	"subtranslate/internal/translator"
)

// ═══════════════════════════════════════════════════════════════════════
// Run
// ═══════════════════════════════════════════════════════════════════════

// Run coordinates the overall translation process.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*report.Stats, error) {
	stats := &report.Stats{StartTime: time.Now()}

	// ── Phase 0: Health Check ──────────────────────────────────────────
	fmt.Printf("  %s↻ Đang kiểm tra API...%s\n", cDim, cReset)
	hc := &http.Client{Timeout: 5 * time.Second}
	result0, hcErr := translator.HealthCheck(ctx, cfg.APIUrl, hc)
	if hcErr != nil {
		fmt.Printf("  %s✗ API health check thất bại: %v%s\n", cRed, hcErr, cReset)
		fmt.Printf("  %sHãy đảm bảo LibreTranslate đang chạy tại %s%s\n", cYellow, cfg.APIUrl, cReset)
		return stats, fmt.Errorf("health check: %w", hcErr)
	}
	if result0.HighLatency {
		fmt.Printf("  %s⚠ API latency cao: %s (có thể chậm)%s\n", cYellow, result0.Latency, cReset)
	} else {
		fmt.Printf("  %s✓ API OK%s  %s(%s)%s\n", cGreen, cReset, cDim, result0.Latency, cReset)
	}

	// ── Phase 1: Scan ─────────────────────────────────────────────────
	fmt.Printf("  %s⟳ Đang quét thư mục...%s\n", cDim, cReset)
	result, err := scanner.Scan(cfg.InputDir, cfg.Overwrite)
	if err != nil {
		return stats, fmt.Errorf("scanning: %w", err)
	}

	stats.TotalFiles = len(result.Files) + len(result.Skipped)
	stats.Skipped = len(result.Skipped)

	// ── Phase 1b: Job Resume ──────────────────────────────────────────
	jobState, err := jobstate.Load(cfg.StateFile)
	if err != nil {
		logger.Warn("could not load job state file, starting fresh", "error", err)
	}

	var filesToProcess []string
	resumedCount := 0
	if cfg.Resume {
		for _, fp := range result.Files {
			if jobState.IsDone(fp) {
				resumedCount++
			} else {
				filesToProcess = append(filesToProcess, fp)
			}
		}
		if resumedCount > 0 {
			fmt.Printf("  %s↩ Resume mode: bỏ qua %d file đã dịch%s\n", cCyan, resumedCount, cReset)
			stats.Skipped += resumedCount
		}
	} else {
		filesToProcess = result.Files
	}

	fmt.Printf("  %s✓ Quét xong:%s  %s%d%s file cần dịch  ·  %s%d%s đã bỏ qua\n\n",
		cGreen, cReset,
		cBold, len(filesToProcess), cReset,
		cDim, len(result.Skipped)+resumedCount, cReset)

	if len(filesToProcess) == 0 {
		fmt.Printf("  %sKhông còn file nào cần dịch.%s\n", cYellow, cReset)
		return stats, nil
	}

	if cfg.DryRun {
		fmt.Printf("  %sChế độ dry-run — chỉ liệt kê, không dịch.%s\n", cYellow, cReset)
		for _, f := range filesToProcess {
			logger.Info("would translate", "file", f)
		}
		return stats, nil
	}

	totalFiles := len(filesToProcess)
	workers := cfg.MaxConcurrency
	if workers > totalFiles {
		workers = totalFiles
	}

	// ── Phase 2: Cache + Translator ───────────────────────────────────
	cache := translator.NewCacheWithTTL(10000, cfg.CacheTTL)
	if loadErr := cache.Load(cfg.CacheFile); loadErr != nil {
		logger.Warn("could not load translation cache, starting fresh", "error", loadErr)
	} else {
		fmt.Printf("  %s✓ Cache loaded:%s  %s%s%s\n", cGreen, cReset, cDim, cfg.CacheFile, cReset)
	}

	client := translator.NewClient(
		cfg.APIUrl, cfg.RateLimit, cfg.MaxRetries, cfg.BatchSize,
		cfg.RequestDelay, cfg.BatchTimeout, cache, logger,
	)

	// ── Adaptive throttling (opt-in) ─────────────────────────────────
	if cfg.Adaptive {
		tcfg := throttle.DefaultConfig(cfg.BatchSize, logger)
		ctrl := throttle.New(tcfg, cfg.RequestDelay, cfg.BatchSize)
		client.Throttle = ctrl
		go ctrl.Run(ctx)
		fmt.Printf("  %s✓ Adaptive throttling enabled%s\n", cGreen, cReset)
	}

	// Periodic cache flush goroutine.
	cacheFlushDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := cache.Save(cfg.CacheFile); err != nil {
					logger.Warn("periodic cache save failed", "error", err)
				}
			case <-cacheFlushDone:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// ── Phase 3: Job queue ────────────────────────────────────────────
	jobs := make(chan fileJob, totalFiles)
	for i, fp := range filesToProcess {
		jobs <- fileJob{index: i, filePath: fp}
	}
	close(jobs)

	// ── Phase 4: Workers + live dashboard ────────────────────────────
	dash := newDashboard(cfg, workers, totalFiles, client)
	dash.doRender(false)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for job := range jobs {
				if ctx.Err() != nil {
					return
				}

				baseName := filepath.Base(job.filePath)

				onProgress := func(completed, total int, state string) {
					dash.setWorkerProgress(workerID, completed, total, state)
				}

				fileCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
				lines, hits, err := processFile(fileCtx, job.filePath, client, baseName, onProgress, dash, workerID, logger.With("worker", workerID))
				cancel()

				// Build log message and persist job state outside the mutex to
				// minimise lock hold time (5.2).
				var logMsg string
				if err != nil {
					logMsg = fmt.Sprintf("  %s✗ W%d%s  %s  %s→ %v%s",
						cRed, workerID, cReset, baseName, cDim, err, cReset)
					_ = jobState.MarkFailed(job.filePath)
				} else {
					outName := filepath.Base(scanner.OutputPath(job.filePath))
					logMsg = fmt.Sprintf("  %s✓ W%d%s  %s  →  %s%s%s  %s(%d dòng, %d cache)%s",
						cGreen, workerID, cReset, baseName,
						cCyan, outName, cReset,
						cDim, lines, hits, cReset)
					_ = jobState.MarkSuccess(job.filePath)
				}

				mu.Lock()
				if err != nil {
					stats.Failed++
					stats.FailedFiles = append(stats.FailedFiles, job.filePath)
					dash.markWorkerDone(workerID, 0, 0, logMsg)
				} else {
					stats.Succeeded++
					stats.TotalLines += lines
					stats.CacheHits += hits
					dash.markWorkerDone(workerID, lines, hits, logMsg)
				}
				mu.Unlock()

				dash.setWorkerIdle(workerID)
			}
		}(w)
	}

	wg.Wait()
	dash.finish()

	// ── Phase 5: Persist cache + clean up state ───────────────────────
	close(cacheFlushDone) // Stop periodic flusher
	if err := cache.Save(cfg.CacheFile); err != nil {
		logger.Warn("final cache save failed", "error", err)
	} else {
		logger.Info("cache saved", "file", cfg.CacheFile, "entries", cache.Size())
	}

	// If all files succeeded, delete the state file.
	if stats.Failed == 0 && ctx.Err() == nil {
		_ = jobState.Delete()
	}

	m := client.GetMetrics()
	stats.TotalRetries = int(m.Retries)
	stats.TotalAPICalls = int(m.APICalls)
	stats.AvgLatency = m.AvgLatency

	fmt.Println()
	return stats, nil
}
