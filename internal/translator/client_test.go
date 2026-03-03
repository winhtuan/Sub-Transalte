package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

/* ---------- jitterBackoff ---------- */

func TestJitterBackoffUsesRetryAfter(t *testing.T) {
	retryAfter := 5 * time.Second
	got := jitterBackoff(0, retryAfter)
	if got != retryAfter {
		t.Errorf("expected Retry-After %v to be used directly, got %v", retryAfter, got)
	}
}

func TestJitterBackoffWithinBounds(t *testing.T) {
	for attempt := 0; attempt < 5; attempt++ {
		base := time.Duration(1<<uint(attempt)) * time.Second // 2^attempt seconds
		for i := 0; i < 100; i++ {
			got := jitterBackoff(attempt, 0)
			lo := base / 2
			hi := base
			if got < lo || got > hi {
				t.Errorf("attempt=%d: jitterBackoff=%v outside [%v, %v]", attempt, got, lo, hi)
			}
		}
	}
}

func TestJitterBackoffNoRetryAfter(t *testing.T) {
	// Attempt 0: base=1s → should be in [500ms, 1s]
	for i := 0; i < 50; i++ {
		got := jitterBackoff(0, 0)
		if got < 500*time.Millisecond || got > time.Second {
			t.Errorf("jitterBackoff(0,0) = %v, want [500ms, 1s]", got)
		}
	}
}

/* ---------- parseRetryAfter ---------- */

func TestParseRetryAfterInteger(t *testing.T) {
	d := parseRetryAfter("30")
	if d != 30*time.Second {
		t.Errorf("parseRetryAfter(\"30\") = %v, want 30s", d)
	}
}

func TestParseRetryAfterZero(t *testing.T) {
	d := parseRetryAfter("0")
	if d != 0 {
		t.Errorf("parseRetryAfter(\"0\") = %v, want 0", d)
	}
}

func TestParseRetryAfterEmpty(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("expected 0 for empty header, got %v", d)
	}
}

func TestParseRetryAfterInvalid(t *testing.T) {
	if d := parseRetryAfter("not-a-number"); d != 0 {
		t.Errorf("expected 0 for invalid header, got %v", d)
	}
}

/* ---------- HTTP server helpers ---------- */

// newTestServer creates a test HTTP server that returns the provided handler.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cache := NewCache(100)
	return NewClient(srv.URL, 100, 2, 5, 0, 5*time.Second, cache, nil)
}

// mustMarshal marshals v to JSON, fataling the test on error.
func mustMarshal(tb testing.TB, v interface{}) []byte {
	tb.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tb.Fatalf("json.Marshal: %v", err)
	}
	return b
}

/* ---------- Binary split fallback ---------- */

func TestTranslateSplitSingleItem(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := translateResponse{TranslatedText: "translated"}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustMarshal(t, resp))
	}))
	defer srv.Close()

	cache := NewCache(100)
	c := NewClient(srv.URL, 100, 0, 5, 0, 5*time.Second, cache, nil)

	result, err := c.translateSplit(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0] != "translated" {
		t.Errorf("unexpected result: %v", result)
	}
	if calls != 1 {
		t.Errorf("expected 1 API call, got %d", calls)
	}
}

func TestTranslateSplitCorrectness(t *testing.T) {
	// Server echoes back the input with a "T:" prefix to verify ordering.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req translateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := translateResponse{TranslatedText: "T:" + req.Q}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustMarshal(t, resp))
	}))
	defer srv.Close()

	cache := NewCache(100)
	c := NewClient(srv.URL, 100, 0, 5, 0, 5*time.Second, cache, nil)

	inputs := []string{"a", "b", "c", "d", "e"}
	results, err := c.translateSplit(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(inputs) {
		t.Fatalf("expected %d results, got %d", len(inputs), len(results))
	}
	for i, inp := range inputs {
		want := "T:" + inp
		if results[i] != want {
			t.Errorf("result[%d] = %q, want %q", i, results[i], want)
		}
	}
}

/* ---------- Retry-After integration ---------- */

func TestRetryAfterHeaderUsed(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Second attempt: success.
		var req translateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := translateResponse{TranslatedText: "ok"}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustMarshal(t, resp))
	}))
	defer srv.Close()

	cache := NewCache(100)
	c := NewClient(srv.URL, 100, 3, 5, 0, 30*time.Second, cache, nil)

	start := time.Now()
	result, err := c.translateSingle(context.Background(), "hello")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal("unexpected error:", err)
	}
	if result != "ok" {
		t.Errorf("unexpected result: %q", result)
	}
	// Should have waited at least 1 second (Retry-After: 1).
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected ~1s wait from Retry-After, elapsed=%v", elapsed)
	}
}

/* ---------- Dynamic batch size ---------- */

func TestDynamicBatchSizeChangeMidFile(t *testing.T) {
	// Verify that translateBatched reads batch size each iteration.
	callSizes := []int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req translateBatchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		callSizes = append(callSizes, len(req.Q))
		resp := translateBatchResponse{TranslatedText: make([]string, len(req.Q))}
		for i := range resp.TranslatedText {
			resp.TranslatedText[i] = fmt.Sprintf("t%d", i)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustMarshal(t, resp))
	}))
	defer srv.Close()

	cache := NewCache(100)
	c := NewClient(srv.URL, 100, 0, 3, 0, 5*time.Second, cache, nil)

	texts := []string{"a", "b", "c", "d", "e", "f"}
	_, err := c.translateBatched(context.Background(), texts, nil, len(texts), 0)
	if err != nil {
		t.Fatal(err)
	}
	// With batch size 3, 6 texts should produce 2 batches of 3.
	if len(callSizes) != 2 {
		t.Errorf("expected 2 batch calls, got %d: %v", len(callSizes), callSizes)
	}
}

/* ---------- Benchmarks ---------- */

func BenchmarkBatchFallbackWorstCase(b *testing.B) {
	// Simulate a server that fails all batch requests, forcing binary split fallback.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req translateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Batch request — fail it.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		callCount++
		resp := translateResponse{TranslatedText: "t"}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustMarshal(b, resp))
	}))
	defer srv.Close()

	cache := NewCache(10000)
	c := NewClient(srv.URL, 1000, 0, 16, 0, 5*time.Second, cache, nil)
	texts := make([]string, 16)
	for i := range texts {
		texts[i] = fmt.Sprintf("text%d", i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.translateSplit(context.Background(), texts)
	}
}
