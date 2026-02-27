package throttle

import (
	"math"
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
// Params — lock-free values read by workers
// ═══════════════════════════════════════════════════════════════════════

// Params holds live-adjustable throttling parameters.
// Workers read these atomically before each request.
type Params struct {
	Delay     atomic.Int64 // nanoseconds
	BatchSize atomic.Int32
}

// ═══════════════════════════════════════════════════════════════════════
// Controller
// ═══════════════════════════════════════════════════════════════════════

// Controller is the adaptive throttle controller. It observes per-request
// metrics and adjusts Params in a background goroutine.
type Controller struct {
	cfg    Config
	Params Params

	// Metrics (lock-free, written by workers via RecordRequest)
	ewmaLatencyNs atomic.Int64
	decayReqsX1k  atomic.Int64 // decayed request count × 1000 (fixed-point)
	decayErrsX1k  atomic.Int64 // decayed error count × 1000 (fixed-point)
	consecSuccess atomic.Int64
	baselineNs    atomic.Int64

	// Calibration state
	state      atomic.Int32
	reqCount   atomic.Int32
	calibMinNs atomic.Int64 // minimum EWMA observed during calibration
}

// New creates a Controller with the given config and initial parameter values.
func New(cfg Config, initialDelay time.Duration, initialBatch int) *Controller {
	c := &Controller{cfg: cfg}
	c.Params.Delay.Store(int64(initialDelay))
	c.Params.BatchSize.Store(int32(initialBatch))
	c.state.Store(stateWarmup)
	c.calibMinNs.Store(math.MaxInt64)
	return c
}
