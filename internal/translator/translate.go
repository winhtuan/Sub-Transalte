package translator

import (
	"context"
	"fmt"
)

// TranslateTexts translates a slice of texts from English to Vietnamese.
func (c *Client) TranslateTexts(ctx context.Context, texts []string, onProgress ProgressFunc) ([]string, int, error) {
	results := make([]string, len(texts))
	cacheHits := 0

	var uncachedIndices []int
	var uncachedTexts []string

	for i, text := range texts {
		if cached, ok := c.cache.Get(text); ok {
			results[i] = cached
			cacheHits++
		} else {
			uncachedIndices = append(uncachedIndices, i)
			uncachedTexts = append(uncachedTexts, text)
		}
	}

	if len(uncachedTexts) > 0 {
		translated, err := c.translateBatched(ctx, uncachedTexts, onProgress, len(texts), cacheHits)
		if err != nil {
			return nil, cacheHits, err
		}

		for j, idx := range uncachedIndices {
			results[idx] = translated[j]
			c.cache.Put(uncachedTexts[j], translated[j])
		}
	}

	return results, cacheHits, nil
}

// translateBatched splits texts into batches and translates each batch.
// The batch size is read fresh on every iteration so that adaptive throttle
// adjustments take effect immediately for subsequent batches.
func (c *Client) translateBatched(ctx context.Context, texts []string, onProgress ProgressFunc, totalLines, cacheHits int) ([]string, error) {
	allResults := make([]string, 0, len(texts))

	for i := 0; i < len(texts); {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("translation cancelled: %w", err)
		}

		// Dynamic batch size: read fresh every iteration so throttle adjustments
		// apply to the very next batch without waiting for the file to finish.
		bs := c.getEffectiveBatchSize()
		end := i + bs
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		// Apply per-batch timeout (nested inside the per-file timeout).
		batchCtx, batchCancel := context.WithTimeout(ctx, c.batchTimeout)

		if onProgress != nil {
			onProgress(cacheHits+len(allResults), totalLines, "translating")
		}

		translated, err := c.translateBatch(batchCtx, batch, onProgress, cacheHits+len(allResults), totalLines)
		batchCancel() // Always cancel immediately after use — NOT deferred (avoid leak in loop).
		if err != nil {
			// Binary split fallback: isolates failing line in O(log N) calls instead
			// of O(N) one-by-one retries.  Uses ctx (not the already-expired batchCtx).
			c.logger.Warn("batch translation failed, falling back to binary split",
				"batch_start", i, "batch_size", len(batch), "error", err)

			split, sErr := c.translateSplit(ctx, batch)
			if sErr != nil {
				c.metrics.TotalErrors.Add(1)
				return nil, fmt.Errorf("translating batch at offset %d: %w", i, sErr)
			}
			allResults = append(allResults, split...)
			i += len(batch)

			if onProgress != nil {
				onProgress(cacheHits+len(allResults), totalLines, "translating")
			}
			continue
		}

		allResults = append(allResults, translated...)
		i += len(batch)

		if onProgress != nil {
			onProgress(cacheHits+len(allResults), totalLines, "translating")
		}
	}

	return allResults, nil
}

// translateSplit recursively halves a batch to isolate failing lines.
// This replaces the O(N) one-by-one fallback with an O(log N) binary search.
// Uses ctx (not batchCtx which is already cancelled when this is called).
func (c *Client) translateSplit(ctx context.Context, texts []string) ([]string, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) == 1 {
		result, err := c.translateSingle(ctx, texts[0])
		return []string{result}, err
	}
	mid := len(texts) / 2
	left, err := c.translateSplit(ctx, texts[:mid])
	if err != nil {
		return nil, err
	}
	right, err := c.translateSplit(ctx, texts[mid:])
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}
