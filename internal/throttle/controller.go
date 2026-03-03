package throttle

import (
	"sync/atomic"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// State constants
// ═══════════════════════════════════════════════════════════════════════

const (
	stateWarmup      int32 = 0 // Skipping first N requests (model loading)
	stateCalibrating int32 = 1 // Collecting baseline latency samples
	stateNormal      int32 = 2 // Active throttling
	stateCooldown    int32 = 3 // Post-backoff hysteresis pause
)

// ═══════════════════════════════════════════════════════════════════════
// Params — lock-free values read by workers on the hot path
// ═══════════════════════════════════════════════════════════════════════

// Params holds live-adjustable throttling parameters.
// Workers read these atomically before each request; Run() writes them.
type Params struct {
	Delay     atomic.Int64 // nanoseconds
	BatchSize atomic.Int32
}

// ═══════════════════════════════════════════════════════════════════════
// Controller
// ═══════════════════════════════════════════════════════════════════════

// Controller is the adaptive throttle controller.
//
// Workers call RecordRequest() which does a non-blocking send on metricsCh.
// The Run() goroutine is the sole owner of all EWMA/counter/state variables;
// it processes the channel in its select loop, eliminating the CAS spin loops
// that existed in the previous design.
//
// Params (Delay, BatchSize) are written by Run() and read by workers via
// atomic operations — the worker hot path remains fully lock-free.
type Controller struct {
	cfg       Config
	Params    Params
	metricsCh chan requestMetric // buffered 256; RecordRequest does non-blocking send
}

// New creates a Controller with the given config and initial parameter values.
func New(cfg Config, initialDelay time.Duration, initialBatch int) *Controller {
	c := &Controller{
		cfg:       cfg,
		metricsCh: make(chan requestMetric, 256),
	}
	c.Params.Delay.Store(int64(initialDelay))
	c.Params.BatchSize.Store(int32(initialBatch))
	return c
}
