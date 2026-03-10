package translator

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// HealthCheckResult holds the outcome of an API health probe.
type HealthCheckResult struct {
	Latency     time.Duration
	HighLatency bool // latency > 1s
}

// HealthCheck probes the LibreTranslate API before starting the translation run.
// It retries up to maxRetries times with a delay between attempts, which is
// essential for Docker environments where LibreTranslate may still be loading
// its translation models when the client container starts.
//
// Returns an error only if all attempts fail.
// Sets HighLatency if the successful response took > 1s.
func HealthCheck(ctx context.Context, apiURL string, httpClient *http.Client) (*HealthCheckResult, error) {
	const (
		maxRetries = 30 // Total wait ≈ 30 × 2s = 60s
		retryDelay = 2 * time.Second
		perTry     = 5 * time.Second
	)

	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Check parent context first.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("health check cancelled: %w", ctx.Err())
		}

		checkCtx, cancel := context.WithTimeout(ctx, perTry)

		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, apiURL+"/languages", nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("creating health check request: %w", err)
		}

		start := time.Now()
		resp, err := httpClient.Do(req)
		latency := time.Since(start)
		cancel()

		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return &HealthCheckResult{
					Latency:     latency,
					HighLatency: latency > time.Second,
				}, nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		// Log retry (visible via orchestrator Printf before this function is called).
		if attempt < maxRetries {
			fmt.Printf("  ◌ API chưa sẵn sàng (lần %d/%d), thử lại sau %s... (%v)\n",
				attempt, maxRetries, retryDelay, lastErr)

			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return nil, fmt.Errorf("health check cancelled: %w", ctx.Err())
			}
		}
	}

	return nil, fmt.Errorf("LibreTranslate API unreachable at %s after %d attempts: %w",
		apiURL, maxRetries, lastErr)
}
