// Package report provides a summary report printer for the translation run.
package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"subtranslate/internal/ui"
)

// local aliases to keep call-sites unchanged.
const (
	cReset  = ui.Reset
	cRed    = ui.Red
	cGreen  = ui.Green
	cYellow = ui.Yellow
	cCyan   = ui.Cyan
	cBold   = ui.Bold
	cDim    = ui.Dim
)

func boxTop() string { return ui.BoxTop("") }
func boxMid() string { return ui.BoxMid("") }
func boxBot() string { return ui.BoxBot("") }

func boxRow(content string) string { return ui.BoxRow("", content) }

// Stats holds the statistics collected during a translation run.
type Stats struct {
	TotalFiles  int
	Succeeded   int
	Failed      int
	Skipped     int
	TotalLines  int
	CacheHits   int
	StartTime   time.Time
	Duration    time.Duration
	FailedFiles []string

	// Metrics from translator client.
	TotalRetries  int
	TotalAPICalls int
	AvgLatency    time.Duration
}

// Print writes a formatted summary report to the writer.
func Print(w io.Writer, stats *Stats) {
	stats.Duration = time.Since(stats.StartTime)

	speed := 0.0
	if stats.Duration.Seconds() > 0 {
		speed = float64(stats.TotalLines) / stats.Duration.Seconds()
	}

	cacheRatio := 0.0
	totalProcessed := stats.TotalLines + stats.CacheHits
	if totalProcessed > 0 {
		cacheRatio = float64(stats.CacheHits) / float64(totalProcessed) * 100
	}

	sep := fmt.Sprintf("  %s%s%s", cDim, strings.Repeat("─", 45), cReset)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s%sSub-Translate  ·  Summary%s\n", cCyan, cBold, cReset)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Tổng file:          %s%d%s\n", cBold, stats.TotalFiles, cReset)
	fmt.Fprintf(w, "  Thành công:         %s✓ %d%s\n", cGreen, stats.Succeeded, cReset)
	fmt.Fprintf(w, "  Thất bại:           %s✗ %d%s\n", cRed, stats.Failed, cReset)
	fmt.Fprintf(w, "  Bỏ qua:             %s⊘ %d%s\n", cYellow, stats.Skipped, cReset)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  Tổng dòng:          %s%d%s\n", cCyan, stats.TotalLines, cReset)
	fmt.Fprintf(w, "  Tốc độ:             %s%.1f lines/s%s\n", cGreen, speed, cReset)
	fmt.Fprintf(w, "  Thời gian:          %s%s%s\n", cCyan, formatDuration(stats.Duration), cReset)
	fmt.Fprintln(w, sep)
	fmt.Fprintf(w, "  API calls:          %s%d%s\n", cDim, stats.TotalAPICalls, cReset)
	fmt.Fprintf(w, "  Avg latency:        %s%s%s\n", cDim, formatLatency(stats.AvgLatency), cReset)
	fmt.Fprintf(w, "  Retries:            %s%d%s\n", cYellow, stats.TotalRetries, cReset)
	fmt.Fprintf(w, "  Cache hits:         %s%d (%.0f%%)%s\n", cGreen, stats.CacheHits, cacheRatio, cReset)
	fmt.Fprintln(w)

	if len(stats.FailedFiles) > 0 {
		fmt.Fprintf(w, "  %s%sFile thất bại:%s\n", cRed, cBold, cReset)
		for _, f := range stats.FailedFiles {
			fmt.Fprintf(w, "    %s✗ %s%s\n", cRed, f, cReset)
		}
		fmt.Fprintln(w)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %02ds", m, s)
}

func formatLatency(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}
