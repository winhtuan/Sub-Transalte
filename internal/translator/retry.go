package translator

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"
)

// translateBatch sends a batch with retry + exponential backoff with equal jitter.
func (c *Client) translateBatch(ctx context.Context, texts []string, onProgress ProgressFunc, currentCompleted, totalLines int) ([]string, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitBeforeRequest(ctx, onProgress, currentCompleted, totalLines); err != nil {
			return nil, err
		}

		start := time.Now()
		result, statusCode, retryAfter, err := c.doTranslateBatchRequest(ctx, texts)
		latency := time.Since(start)
		c.metrics.TotalAPICalls.Add(1)
		c.metrics.TotalLatencyNs.Add(int64(latency))
		if c.Throttle != nil {
			c.Throttle.RecordRequest(latency, statusCode)
		}

		if err == nil {
			return result, nil
		}

		lastErr = err

		if !isRetryable(statusCode, err) {
			return nil, fmt.Errorf("non-retryable error (HTTP %d): %w", statusCode, err)
		}

		c.metrics.TotalRetries.Add(1)

		if attempt < c.maxRetries {
			backoff := jitterBackoff(attempt, retryAfter)

			if onProgress != nil {
				onProgress(currentCompleted, totalLines, "retrying")
			}

			c.logger.Warn("retrying batch translation",
				"attempt", attempt+1,
				"max_retries", c.maxRetries,
				"backoff", backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, fmt.Errorf("all %d retries exhausted: %w", c.maxRetries, lastErr)
}

// translateSingle sends a single translation request with retry + jitter backoff.
func (c *Client) translateSingle(ctx context.Context, text string) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitBeforeRequest(ctx, nil, 0, 0); err != nil {
			return "", err
		}

		start := time.Now()
		result, statusCode, retryAfter, err := c.doTranslateRequest(ctx, text)
		c.metrics.TotalAPICalls.Add(1)
		c.metrics.TotalLatencyNs.Add(int64(time.Since(start)))

		if err == nil {
			return result, nil
		}

		lastErr = err

		if !isRetryable(statusCode, err) {
			return "", fmt.Errorf("non-retryable error (HTTP %d): %w", statusCode, err)
		}

		c.metrics.TotalRetries.Add(1)

		if attempt < c.maxRetries {
			backoff := jitterBackoff(attempt, retryAfter)
			c.logger.Warn("retrying translation",
				"attempt", attempt+1,
				"max_retries", c.maxRetries,
				"backoff", backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}

	return "", fmt.Errorf("all %d retries exhausted: %w", c.maxRetries, lastErr)
}

// jitterBackoff computes a retry delay using the equal jitter strategy.
// If retryAfter > 0 (from a Retry-After header) it is used directly.
//
// Equal jitter: backoff = base/2 + rand(0..base/2)
// This prevents thundering herd across workers while bounding the wait time.
func jitterBackoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := time.Duration(math.Pow(2, float64(attempt))) * time.Second
	half := int64(base / 2)
	if half <= 0 {
		return base
	}
	// math/rand is auto-seeded since Go 1.20 — no explicit seed needed.
	jitter := time.Duration(rand.Int63n(half + 1))
	return base/2 + jitter
}

// waitBeforeRequest applies rate limiting and request delay.
func (c *Client) waitBeforeRequest(ctx context.Context, onProgress ProgressFunc, completed, total int) error {
	if err := c.rateLimiter.Wait(ctx); err != nil {
		return fmt.Errorf("rate limiter: %w", err)
	}

	delay := c.getEffectiveDelay()
	if delay > 0 {
		if onProgress != nil {
			onProgress(completed, total, "waiting")
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// isRetryable returns true if the request should be retried.
func isRetryable(statusCode int, err error) bool {
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if statusCode >= 500 {
		return true
	}
	if err != nil && statusCode == 0 {
		return true
	}
	return false
}
