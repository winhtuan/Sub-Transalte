package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// doTranslateBatchRequest performs a batch HTTP POST to /translate.
// Returns (results, statusCode, retryAfter, error).
// retryAfter is parsed from the Retry-After header on HTTP 429.
func (c *Client) doTranslateBatchRequest(ctx context.Context, texts []string) ([]string, int, time.Duration, error) {
	reqBody := translateBatchRequest{
		Q:      texts,
		Source: "en",
		Target: "vi",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiURL+"/translate", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, 0, fmt.Errorf("reading response: %w", err)
	}

	var retryAfter time.Duration
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}

	if resp.StatusCode != http.StatusOK {
		var errResp translateErrorResponse
		_ = json.Unmarshal(respBody, &errResp)
		return nil, resp.StatusCode, retryAfter, fmt.Errorf("API error (HTTP %d): %s",
			resp.StatusCode, errResp.Error)
	}

	var result translateBatchResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, resp.StatusCode, 0, fmt.Errorf("parsing response JSON: %w", err)
	}

	if len(result.TranslatedText) != len(texts) {
		return nil, resp.StatusCode, 0, fmt.Errorf("response count mismatch: expected %d, got %d",
			len(texts), len(result.TranslatedText))
	}

	return result.TranslatedText, resp.StatusCode, 0, nil
}

// doTranslateRequest performs a single HTTP POST to /translate.
// Returns (result, statusCode, retryAfter, error).
func (c *Client) doTranslateRequest(ctx context.Context, text string) (string, int, time.Duration, error) {
	reqBody := translateRequest{
		Q:      text,
		Source: "en",
		Target: "vi",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, 0, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiURL+"/translate", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, 0, fmt.Errorf("reading response: %w", err)
	}

	var retryAfter time.Duration
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}

	if resp.StatusCode != http.StatusOK {
		var errResp translateErrorResponse
		_ = json.Unmarshal(respBody, &errResp)
		return "", resp.StatusCode, retryAfter, fmt.Errorf("API error (HTTP %d): %s",
			resp.StatusCode, errResp.Error)
	}

	var result translateResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", resp.StatusCode, 0, fmt.Errorf("parsing response JSON: %w", err)
	}

	return result.TranslatedText, resp.StatusCode, 0, nil
}

// parseRetryAfter parses the value of the Retry-After HTTP header.
// It supports two formats:
//   - Integer seconds:  "120"
//   - HTTP-date:        "Fri, 31 Dec 1999 23:59:59 GMT"
//
// Returns 0 if the header is absent, zero, or cannot be parsed.
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	// Try integer seconds first.
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// Try HTTP-date.
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
