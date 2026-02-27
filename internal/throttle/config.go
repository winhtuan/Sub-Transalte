// Package throttle provides adaptive request throttling for the translation
// client. It dynamically adjusts request delay and batch size based on runtime
// API latency and error signals using an AIMD (Additive Increase / Multiplicative
// Decrease) strategy — the same algorithm that powers TCP congestion control.
//
// The controller runs a background decision loop (every 2s) and exposes
// parameters via atomic values so workers read them lock-free.
package throttle

import (
	"log/slog"
	"time"
)

// Config sets the boundaries and tuning knobs for adaptive throttling.
type Config struct {
	MinDelay      time.Duration // Floor for request delay (e.g., 50ms)
	MaxDelay      time.Duration // Ceiling floor (actual ceiling = max(this, 3×baseline))
	MinBatchSize  int           // Smallest batch allowed (e.g., 5)
	MaxBatchSize  int           // Original cfg.BatchSize
	Alpha         float64       // EWMA smoothing factor (0.3)
	BackoffFactor float64       // Delay multiplier on back-off (2.0)
	BatchBackoff  float64       // Batch multiplier on back-off (0.7)
	IncreaseStep  time.Duration // Min additive delay decrease per recovery (20ms)
	CooldownTime  time.Duration // Hysteresis pause after back-off (10s)
	DecisionTick  time.Duration // How often the decision loop runs (2s)
	DecayInterval time.Duration // Error counter decay interval (5s)
	DecayFactor   float64       // Error counter decay multiplier (0.8)
	WarmupSkip    int           // Requests to skip during warm-up (3)
	CalibrationN  int           // Requests to calibrate over (15)
	Logger        *slog.Logger
}

// DefaultConfig returns a Config with production-ready defaults.
func DefaultConfig(maxBatchSize int, logger *slog.Logger) Config {
	return Config{
		MinDelay:      50 * time.Millisecond,
		MaxDelay:      5 * time.Second,
		MinBatchSize:  5,
		MaxBatchSize:  maxBatchSize,
		Alpha:         0.3,
		BackoffFactor: 2.0,
		BatchBackoff:  0.7,
		IncreaseStep:  20 * time.Millisecond,
		CooldownTime:  10 * time.Second,
		DecisionTick:  2 * time.Second,
		DecayInterval: 5 * time.Second,
		DecayFactor:   0.8,
		WarmupSkip:    3,
		CalibrationN:  15,
		Logger:        logger,
	}
}
