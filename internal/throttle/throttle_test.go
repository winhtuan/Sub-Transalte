package throttle

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func defaultTestConfig() Config {
	return Config{
		MinDelay:              10 * time.Millisecond,
		MaxDelay:              2 * time.Second,
		MinBatchSize:          1,
		MaxBatchSize:          50,
		Alpha:                 0.3,
		BackoffFactor:         2.0,
		BatchBackoff:          0.7,
		IncreaseStep:          5 * time.Millisecond,
		CooldownTime:          50 * time.Millisecond,
		DecisionTick:          10 * time.Millisecond,
		DecayInterval:         20 * time.Millisecond,
		DecayFactor:           0.8,
		WarmupSkip:            1,
		CalibrationN:          3,
		RecalibrationInterval: 0, // disable for tests
		Logger:                slog.Default(),
	}
}

func TestRecordRequestNonBlocking(t *testing.T) {
	cfg := defaultTestConfig()
	ctrl := New(cfg, 200*time.Millisecond, 25)

	// Fill the channel without starting Run().
	// Must not block even when buffer is full.
	for i := 0; i < 300; i++ {
		ctrl.RecordRequest(100*time.Millisecond, 200)
	}
	// If we got here without deadlock, the non-blocking send works correctly.
}

func TestControllerTransitionsToNormal(t *testing.T) {
	cfg := defaultTestConfig()
	ctrl := New(cfg, 200*time.Millisecond, 25)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go ctrl.Run(ctx)

	// Send enough successful requests to pass warmup + calibration.
	for i := 0; i < 20; i++ {
		ctrl.RecordRequest(100*time.Millisecond, 200)
	}
	time.Sleep(100 * time.Millisecond)
	// Params should still be set (not zeroed).
	if d := ctrl.Params.Delay.Load(); d <= 0 {
		t.Errorf("expected positive delay, got %d", d)
	}
}

func TestBackoffOn429(t *testing.T) {
	cfg := defaultTestConfig()
	ctrl := New(cfg, 100*time.Millisecond, 25)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go ctrl.Run(ctx)

	// Warm up and calibrate.
	for i := 0; i < 10; i++ {
		ctrl.RecordRequest(50*time.Millisecond, 200)
	}
	time.Sleep(50 * time.Millisecond)

	initialDelay := ctrl.Params.Delay.Load()

	// Trigger a 429.
	ctrl.RecordRequest(50*time.Millisecond, 429)
	time.Sleep(50 * time.Millisecond)

	newDelay := ctrl.Params.Delay.Load()
	if newDelay <= initialDelay {
		t.Errorf("expected delay to increase after 429: initial=%d new=%d", initialDelay, newDelay)
	}
}

/* ---------- Benchmark ---------- */

// BenchmarkRecordRequest measures throughput of the non-blocking RecordRequest
// under 100 concurrent goroutines.
func BenchmarkRecordRequest(b *testing.B) {
	cfg := defaultTestConfig()
	ctrl := New(cfg, 200*time.Millisecond, 25)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ctrl.Run(ctx)

	b.ResetTimer()

	var wg sync.WaitGroup
	goroutines := 100
	perG := b.N / goroutines
	if perG < 1 {
		perG = 1
	}

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				ctrl.RecordRequest(50*time.Millisecond, 200)
			}
		}()
	}
	wg.Wait()
}
