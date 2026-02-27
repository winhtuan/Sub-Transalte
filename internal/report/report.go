// Package report provides a summary report printer for the translation run.
package report

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	cReset  = "\033[0m"
	cRed    = "\033[31m"
	cGreen  = "\033[32m"
	cYellow = "\033[33m"
	cCyan   = "\033[36m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
)

const boxInner = 51

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func visibleLen(s string) int {
	return utf8.RuneCountInString(ansiRe.ReplaceAllString(s, ""))
}

func boxTop() string {
	return fmt.Sprintf("%s%s╔%s╗%s", cCyan, cBold, strings.Repeat("═", boxInner), cReset)
}
func boxMid() string {
	return fmt.Sprintf("%s%s╠%s╣%s", cCyan, cBold, strings.Repeat("═", boxInner), cReset)
}
func boxBot() string {
	return fmt.Sprintf("%s%s╚%s╝%s", cCyan, cBold, strings.Repeat("═", boxInner), cReset)
}

func boxRow(content string) string {
	vLen := visibleLen(content)
	pad := boxInner - 2 - vLen
	if pad < 0 {
		pad = 0
	}
	return fmt.Sprintf("%s%s║%s %s%s %s%s║%s",
		cCyan, cBold, cReset,
		content, strings.Repeat(" ", pad),
		cCyan, cBold, cReset)
}

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
