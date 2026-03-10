// Package translator provides a LibreTranslate HTTP client with batching,
// retry, rate limiting, and an in-memory LRU translation cache.
package translator

import (
	"log/slog"
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

// MetricsSnapshot is a point-in-time copy of all metrics.
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
// If logger is nil, slog.Default() is used.
func NewClient(apiURL string, ratePerSec float64, maxRetries, batchSize int, requestDelay, batchTimeout time.Duration, cache *Cache, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
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
