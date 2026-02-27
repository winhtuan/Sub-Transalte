// Package orchestrator coordinates the subtitle translation pipeline.
// This file (file_processor.go) handles the translation of a single SRT file,
// including parsing, extracting text, calling the API, and writing the result.
package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"subtranslate/internal/scanner"
	"subtranslate/internal/srt"
	"subtranslate/internal/translator"
)

// ═══════════════════════════════════════════════════════════════════════
// processFile
// ═══════════════════════════════════════════════════════════════════════

func processFile(ctx context.Context, filePath string, client *translator.Client, baseName string, onProgress translator.ProgressFunc, dash *dashboard, workerID int, logger *slog.Logger) (int, int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	subtitles, err := srt.Parse(file)
	if err != nil {
		return 0, 0, fmt.Errorf("parsing SRT: %w", err)
	}
	if len(subtitles) == 0 {
		return 0, 0, nil
	}

	type lineRef struct {
		subIdx  int
		lineIdx int
	}

	var allTexts []string
	var allRefs []lineRef

	for si, sub := range subtitles {
		for li, line := range sub.Lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			allTexts = append(allTexts, trimmed)
			allRefs = append(allRefs, lineRef{subIdx: si, lineIdx: li})
		}
	}

	if len(allTexts) == 0 {
		return 0, 0, nil
	}

	dash.setWorkerFile(workerID, baseName, len(allTexts))

	translated, cacheHits, err := client.TranslateTexts(ctx, allTexts, onProgress)
	if err != nil {
		return 0, 0, fmt.Errorf("translating: %w", err)
	}

	for i, ref := range allRefs {
		subtitles[ref.subIdx].Lines[ref.lineIdx] = translated[i]
	}

	outputPath := scanner.OutputPath(filePath)
	outFile, err := os.Create(outputPath)
	if err != nil {
		return 0, 0, fmt.Errorf("creating output file: %w", err)
	}
	defer outFile.Close()

	if err := srt.Write(outFile, subtitles); err != nil {
		return 0, 0, fmt.Errorf("writing SRT: %w", err)
	}

	return len(allTexts), cacheHits, nil
}
