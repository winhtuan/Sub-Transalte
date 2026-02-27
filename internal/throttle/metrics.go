package throttle

import (
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// RecordRequest — called by workers after each HTTP round-trip
// ═══════════════════════════════════════════════════════════════════════

// RecordRequest records the outcome of an HTTP request for the throttler.
// statusCode is the HTTP status code (0 if the request errored before receiving a response).
func (c *Controller) RecordRequest(latency time.Duration, statusCode int) {
	ns := latency.Nanoseconds()
	success := statusCode >= 200 && statusCode < 300

	// ── Update EWMA latency ──
	alpha := c.cfg.Alpha
	for {
		old := c.ewmaLatencyNs.Load()
		if old == 0 {
			if c.ewmaLatencyNs.CompareAndSwap(0, ns) {
				break
			}
			continue
		}
		newVal := int64(alpha*float64(ns) + (1-alpha)*float64(old))
		if c.ewmaLatencyNs.CompareAndSwap(old, newVal) {
			break
		}
	}

	// ── Decayed counters (fixed-point × 1000) ──
	c.decayReqsX1k.Add(1000)
	if !success {
		c.decayErrsX1k.Add(1000)
	}

	// ── Consecutive success tracker ──
	if success {
		c.consecSuccess.Add(1)
	} else {
		c.consecSuccess.Store(0)
	}

	// ── State machine: warmup → calibrating → normal ──
	count := c.reqCount.Add(1)
	curState := c.state.Load()

	if curState == stateWarmup && int(count) >= c.cfg.WarmupSkip {
		// Warm-up done, reset EWMA for clean calibration.
		c.ewmaLatencyNs.Store(ns)
		c.reqCount.Store(0)
		c.state.CompareAndSwap(stateWarmup, stateCalibrating)
		return
	}

	if curState == stateCalibrating {
		// Track the minimum EWMA during calibration.
		ewma := c.ewmaLatencyNs.Load()
		for {
			curMin := c.calibMinNs.Load()
			if ewma >= curMin {
				break
			}
			if c.calibMinNs.CompareAndSwap(curMin, ewma) {
				break
			}
		}

		if int(c.reqCount.Load()) >= c.cfg.CalibrationN {
			// baseline = min(EWMA during calibration) × 1.1 (safety margin)
			baseline := int64(float64(c.calibMinNs.Load()) * 1.1)
			c.baselineNs.Store(baseline)
			c.state.CompareAndSwap(stateCalibrating, stateNormal)

			c.cfg.Logger.Info("[throttle] calibration complete",
				"baseline", time.Duration(baseline),
				"ewma", time.Duration(c.ewmaLatencyNs.Load()),
			)
		}
	}

	// ── Immediate back-off for 429 / 5xx during normal operation ──
	if curState == stateNormal || curState == stateCooldown {
		if statusCode == 429 {
			c.applyBackoff(3.0, false, "http_429")
		} else if statusCode >= 500 {
			c.applyBackoff(c.cfg.BackoffFactor, false, "http_5xx")
		}
	}
}
