package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"subtranslate/internal/throttle"
)

// Client is a LibreTranslate HTTP client with batching, retry, rate limiting,
// request delay, and metrics tracking.
type Client struct {
	httpClient   *http.Client
	apiURL       string
	rateLimiter  *rate.Limiter
	requestDelay time.Duration
	maxRetries   int
	batchSize    int
	batchTimeout time.Duration
	cache        *Cache
	logger       *slog.Logger
	metrics      Metrics
	Throttle     *throttle.Controller // nil = static mode (adaptive disabled)
}

// getEffectiveDelay returns the current request delay — adaptive or static.
func (c *Client) getEffectiveDelay() time.Duration {
	if c.Throttle != nil {
		return time.Duration(c.Throttle.Params.Delay.Load())
	}
	return c.requestDelay
}

// getEffectiveBatchSize returns the current batch size — adaptive or static.
func (c *Client) getEffectiveBatchSize() int {
	if c.Throttle != nil {
		return int(c.Throttle.Params.BatchSize.Load())
	}
	return c.batchSize
}

// Metrics tracks operational statistics for monitoring and reporting.
type Metrics struct {
	TotalAPICalls  atomic.Int64 // Total API requests sent
	TotalRetries   atomic.Int64 // Total retry attempts
	TotalErrors    atomic.Int64 // Total non-recoverable errors
	TotalLatencyNs atomic.Int64 // Cumulative API latency in nanoseconds
}

// Snapshot returns a point-in-time copy of all metrics.
type MetricsSnapshot struct {
	APICalls   int64
	Retries    int64
	Errors     int64
	AvgLatency time.Duration
}

// Snapshot returns a point-in-time copy of all metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	calls := m.TotalAPICalls.Load()
	latency := time.Duration(0)
	if calls > 0 {
		latency = time.Duration(m.TotalLatencyNs.Load() / calls)
	}
	return MetricsSnapshot{
		APICalls:   calls,
		Retries:    m.TotalRetries.Load(),
		Errors:     m.TotalErrors.Load(),
		AvgLatency: latency,
	}
}

// translateRequest is the JSON body for a single-text POST /translate.
type translateRequest struct {
	Q      string `json:"q"`
	Source string `json:"source"`
	Target string `json:"target"`
}

// translateBatchRequest is the JSON body for a batch POST /translate.
type translateBatchRequest struct {
	Q      []string `json:"q"`
	Source string   `json:"source"`
	Target string   `json:"target"`
}

// translateResponse is the JSON body returned for a single-text request.
type translateResponse struct {
	TranslatedText string `json:"translatedText"`
}

// translateBatchResponse is the JSON body returned for a batch request.
type translateBatchResponse struct {
	TranslatedText []string `json:"translatedText"`
}

// translateErrorResponse is returned when the API returns an error.
type translateErrorResponse struct {
	Error string `json:"error"`
}

// ProgressFunc is called after each batch completes.
// completed/total = line counts, state = "translating"/"retrying"/"waiting".
type ProgressFunc func(completed, total int, state string)

// NewClient creates a new LibreTranslate client with connection pooling
// and CPU-friendly request throttling.
func NewClient(apiURL string, ratePerSec float64, maxRetries, batchSize int, requestDelay, batchTimeout time.Duration, cache *Cache, logger *slog.Logger) *Client {
	burst := max(int(ratePerSec), 3)

	transport := &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	return &Client{
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
		apiURL:       strings.TrimRight(apiURL, "/"),
		rateLimiter:  rate.NewLimiter(rate.Limit(ratePerSec), burst),
		requestDelay: requestDelay,
		maxRetries:   maxRetries,
		batchSize:    batchSize,
		batchTimeout: batchTimeout,
		cache:        cache,
		logger:       logger,
	}
}

// GetMetrics returns a snapshot of the client's operational metrics.
func (c *Client) GetMetrics() MetricsSnapshot {
	return c.metrics.Snapshot()
}

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
func (c *Client) translateBatched(ctx context.Context, texts []string, onProgress ProgressFunc, totalLines, cacheHits int) ([]string, error) {
	allResults := make([]string, 0, len(texts))

	bs := c.getEffectiveBatchSize()
	for i := 0; i < len(texts); i += bs {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("translation cancelled: %w", err)
		}

		end := i + bs
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		// Apply per-batch timeout (nested inside the per-file timeout).
		batchCtx, batchCancel := context.WithTimeout(ctx, c.batchTimeout)

		// Report state: translating.
		if onProgress != nil {
			onProgress(cacheHits+len(allResults), totalLines, "translating")
		}

		translated, err := c.translateBatch(batchCtx, batch, onProgress, cacheHits+len(allResults), totalLines)
		batchCancel() // Always cancel immediately after use — NOT deferred (avoid leak in loop).
		if err != nil {
			// Fallback: translate one-by-one.
			c.logger.Warn("batch translation failed, falling back to one-by-one",
				"batch_start", i, "batch_size", len(batch), "error", err)

			for _, text := range batch {
				if err := ctx.Err(); err != nil {
					return nil, fmt.Errorf("translation cancelled: %w", err)
				}
				single, sErr := c.translateSingle(ctx, text)
				if sErr != nil {
					c.metrics.TotalErrors.Add(1)
					return nil, fmt.Errorf("translating text: %w", sErr)
				}
				allResults = append(allResults, single)

				if onProgress != nil {
					onProgress(cacheHits+len(allResults), totalLines, "translating")
				}
			}
			continue
		}

		allResults = append(allResults, translated...)

		if onProgress != nil {
			onProgress(cacheHits+len(allResults), totalLines, "translating")
		}
	}

	return allResults, nil
}

// translateBatch sends a batch with retry logic.
func (c *Client) translateBatch(ctx context.Context, texts []string, onProgress ProgressFunc, currentCompleted, totalLines int) ([]string, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitBeforeRequest(ctx, onProgress, currentCompleted, totalLines); err != nil {
			return nil, err
		}

		start := time.Now()
		result, statusCode, err := c.doTranslateBatchRequest(ctx, texts)
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
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second

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

// translateSingle sends a single translation request with retry logic.
func (c *Client) translateSingle(ctx context.Context, text string) (string, error) {
	var lastErr error

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := c.waitBeforeRequest(ctx, nil, 0, 0); err != nil {
			return "", err
		}

		start := time.Now()
		result, statusCode, err := c.doTranslateRequest(ctx, text)
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
			backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
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

// doTranslateBatchRequest performs a batch HTTP POST to /translate.
func (c *Client) doTranslateBatchRequest(ctx context.Context, texts []string) ([]string, int, error) {
	reqBody := translateBatchRequest{
		Q:      texts,
		Source: "en",
		Target: "vi",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiURL+"/translate", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp translateErrorResponse
		_ = json.Unmarshal(respBody, &errResp)
		return nil, resp.StatusCode, fmt.Errorf("API error (HTTP %d): %s",
			resp.StatusCode, errResp.Error)
	}

	var result translateBatchResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parsing response JSON: %w", err)
	}

	if len(result.TranslatedText) != len(texts) {
		return nil, resp.StatusCode, fmt.Errorf("response count mismatch: expected %d, got %d",
			len(texts), len(result.TranslatedText))
	}

	return result.TranslatedText, resp.StatusCode, nil
}

// doTranslateRequest performs a single HTTP POST to /translate.
func (c *Client) doTranslateRequest(ctx context.Context, text string) (string, int, error) {
	reqBody := translateRequest{
		Q:      text,
		Source: "en",
		Target: "vi",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiURL+"/translate", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp translateErrorResponse
		_ = json.Unmarshal(respBody, &errResp)
		return "", resp.StatusCode, fmt.Errorf("API error (HTTP %d): %s",
			resp.StatusCode, errResp.Error)
	}

	var result translateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", resp.StatusCode, fmt.Errorf("parsing response JSON: %w", err)
	}

	return result.TranslatedText, resp.StatusCode, nil
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
