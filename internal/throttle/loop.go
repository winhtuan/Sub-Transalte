package throttle

import (
	"context"
	"math"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// Run — background decision loop
// ═══════════════════════════════════════════════════════════════════════

// Run starts the adaptive throttling decision loop. It blocks until ctx is
// cancelled.  Call as: go controller.Run(ctx)
//
// All mutable state (EWMA, counters, state machine, cooldown timer) is owned
// exclusively by this goroutine — no locks needed for that state.
// Only Params (Delay, BatchSize) are shared with workers via atomic ops.
//
// # State machine
//
//	stateWarmup → stateCalibrating → stateNormal ⇄ stateCooldown
//
// Transitions happen on two triggers:
//  1. A requestMetric arrives on metricsCh (warmup/calibration transitions,
//     immediate back-off for HTTP 429/5xx).
//  2. The decision ticker fires (AIMD recovery/backoff, cooldown expiry check,
//     periodic baseline recalibration).
//
// # Work-stealing note
//
// Full dynamic work-stealing is not implemented here.  Subtitle files are
// uniform in size, so a static worker pool with a fixed file queue delivers
// near-optimal utilisation without the complexity of work-stealing queues.
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.DecisionTick)
	defer ticker.Stop()

	decayTicker := time.NewTicker(c.cfg.DecayInterval)
	defer decayTicker.Stop()

	// ── Local state (owned exclusively by this goroutine) ──────────────
	var (
		ewmaLatencyNs  int64   = 0
		decayReqsX1k   int64   = 0
		decayErrsX1k   int64   = 0
		consecSuccess  int64   = 0
		baselineNs     int64   = 0
		state          int32   = stateWarmup
		reqCount       int32   = 0
		calibMinNs     int64   = math.MaxInt64
		stableCycles   int     = 0
		cooldownUntil  time.Time
		lastRecalib    time.Time
	)

	for {
		select {
		case <-ctx.Done():
			return

		// ── Process an incoming metric ────────────────────────────────
		case m := <-c.metricsCh:
			ns := m.latency.Nanoseconds()
			success := m.statusCode >= 200 && m.statusCode < 300

			// Update EWMA latency.
			alpha := c.cfg.Alpha
			if ewmaLatencyNs == 0 {
				ewmaLatencyNs = ns
			} else {
				ewmaLatencyNs = int64(alpha*float64(ns) + (1-alpha)*float64(ewmaLatencyNs))
			}

			// Decayed counters (fixed-point ×1000).
			decayReqsX1k += 1000
			if !success {
				decayErrsX1k += 1000
			}

			// Consecutive success tracker.
			if success {
				consecSuccess++
			} else {
				consecSuccess = 0
			}

			reqCount++
			curState := state

			// ── Warmup → Calibrating ──
			if curState == stateWarmup && int(reqCount) >= c.cfg.WarmupSkip {
				ewmaLatencyNs = ns // Reset EWMA for clean calibration start.
				reqCount = 0
				state = stateCalibrating
				continue
			}

			// ── Calibrating → Normal ──
			if curState == stateCalibrating {
				if ewmaLatencyNs < calibMinNs {
					calibMinNs = ewmaLatencyNs
				}
				if int(reqCount) >= c.cfg.CalibrationN {
					// baseline = min(EWMA during calibration) × 1.1 safety margin.
					baselineNs = int64(float64(calibMinNs) * 1.1)
					state = stateNormal
					lastRecalib = time.Now()
					c.cfg.Logger.Info("[throttle] calibration complete",
						"baseline", time.Duration(baselineNs),
						"ewma", time.Duration(ewmaLatencyNs),
					)
				}
			}

			// ── Immediate back-off for 429 / 5xx ──
			// (only after calibration is done)
			if state == stateNormal || state == stateCooldown {
				if m.statusCode == 429 {
					cooldownUntil = applyBackoff(&c.Params, baselineNs, c.cfg, 3.0, false, "http_429",
						ewmaLatencyNs)
					state = stateCooldown
				} else if m.statusCode >= 500 {
					cooldownUntil = applyBackoff(&c.Params, baselineNs, c.cfg, c.cfg.BackoffFactor, false, "http_5xx",
						ewmaLatencyNs)
					state = stateCooldown
				}
			}

		// ── Decay error / request counters ───────────────────────────
		case <-decayTicker.C:
			decayReqsX1k = int64(float64(decayReqsX1k) * c.cfg.DecayFactor)
			decayErrsX1k = int64(float64(decayErrsX1k) * c.cfg.DecayFactor)

		// ── Decision tick ─────────────────────────────────────────────
		case <-ticker.C:
			if state == stateWarmup || state == stateCalibrating {
				continue
			}

			errorRate := 0.0
			if decayReqsX1k > 0 {
				errorRate = float64(decayErrsX1k) / float64(decayReqsX1k)
			}

			// ── Cooldown expiry check ──
			if state == stateCooldown {
				if time.Now().After(cooldownUntil) {
					// Recovery validation: must meet both conditions to exit cooldown.
					if ewmaLatencyNs < int64(float64(baselineNs)*1.5) && errorRate < 0.05 {
						state = stateNormal
						stableCycles = 0
					} else {
						// Conditions not met — extend cooldown.
						cooldownUntil = time.Now().Add(c.cfg.CooldownTime)
						c.cfg.Logger.Warn("[throttle] extending cooldown — conditions not met",
							"ewma", time.Duration(ewmaLatencyNs),
							"baseline", time.Duration(baselineNs),
							"errorRate", errorRate,
						)
					}
				}
				continue
			}

			// ── Normal state: BACK-OFF checks ──
			curDelay := time.Duration(c.Params.Delay.Load())
			curBatch := int(c.Params.BatchSize.Load())

			if ewmaLatencyNs > 2*baselineNs {
				stableCycles = 0
				cooldownUntil = applyBackoff(&c.Params, baselineNs, c.cfg, c.cfg.BackoffFactor, false, "latency_spike",
					ewmaLatencyNs)
				state = stateCooldown
				continue
			}
			if errorRate > 0.10 {
				stableCycles = 0
				cooldownUntil = applyBackoff(&c.Params, baselineNs, c.cfg, c.cfg.BackoffFactor, true, "error_rate",
					ewmaLatencyNs)
				state = stateCooldown
				continue
			}

			// ── RECOVERY check (all conditions for 5 consecutive cycles) ──
			if ewmaLatencyNs < int64(float64(baselineNs)*1.2) &&
				errorRate == 0 &&
				consecSuccess >= 10 {
				stableCycles++
			} else {
				stableCycles = 0
			}

			if stableCycles >= 5 {
				stableCycles = 0

				// Adaptive delay decrease: max(MinIncreaseStep, 10% of baseline).
				step := c.cfg.IncreaseStep
				baselineStep := time.Duration(float64(baselineNs) * 0.1)
				if baselineStep > step {
					step = baselineStep
				}
				newDelay := curDelay - step
				if newDelay < c.cfg.MinDelay {
					newDelay = c.cfg.MinDelay
				}
				c.Params.Delay.Store(int64(newDelay))

				// Batch: additive +1.
				newBatch := curBatch + 1
				if newBatch > c.cfg.MaxBatchSize {
					newBatch = c.cfg.MaxBatchSize
				}
				c.Params.BatchSize.Store(int32(newBatch))

				c.cfg.Logger.Info("[throttle] recovery",
					"delay", newDelay,
					"batch", newBatch,
					"ewma", time.Duration(ewmaLatencyNs),
					"baseline", time.Duration(baselineNs),
					"errorRate", errorRate,
				)
			} else {
				c.cfg.Logger.Debug("[throttle] status",
					"state", "normal",
					"delay", curDelay,
					"batch", curBatch,
					"ewma", time.Duration(ewmaLatencyNs),
					"baseline", time.Duration(baselineNs),
					"errorRate", errorRate,
					"stable", stableCycles,
					"consec", consecSuccess,
				)
			}

			// ── Periodic baseline recalibration ──
			// Only recalibrate if stable (low error rate) and interval has elapsed.
			if c.cfg.RecalibrationInterval > 0 &&
				time.Since(lastRecalib) >= c.cfg.RecalibrationInterval &&
				errorRate < 0.05 &&
				ewmaLatencyNs > 0 {

				// Smooth update: new = 0.9*old + 0.1*(ewma*1.1).
				candidate := int64(0.9*float64(baselineNs) + 0.1*float64(ewmaLatencyNs)*1.1)
				// Only lower the baseline (never make it worse).
				if candidate < baselineNs {
					baselineNs = candidate
					c.cfg.Logger.Info("[throttle] baseline recalibrated",
						"baseline", time.Duration(baselineNs),
						"ewma", time.Duration(ewmaLatencyNs),
					)
				}
				lastRecalib = time.Now()
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// applyBackoff — multiplicative decrease with hysteresis
// ═══════════════════════════════════════════════════════════════════════

// applyBackoff updates Params (delay, optionally batch) multiplicatively
// and returns the time when cooldown should expire.
// This is called exclusively from the Run() goroutine, so no locking is needed.
func applyBackoff(params *Params, baselineNs int64, cfg Config,
	delayFactor float64, reduceBatch bool, trigger string,
	ewmaLatencyNs int64) time.Time {

	curDelay := time.Duration(params.Delay.Load())
	curBatch := int(params.BatchSize.Load())

	// Delay: multiplicative increase.
	newDelay := time.Duration(float64(curDelay) * delayFactor)
	maxDelay := effectiveMaxDelay(baselineNs, cfg)
	if newDelay > maxDelay {
		newDelay = maxDelay
	}
	params.Delay.Store(int64(newDelay))

	// Batch: hybrid decrease (optional).
	newBatch := curBatch
	if reduceBatch {
		if curBatch < 10 {
			newBatch = curBatch - 1
		} else {
			newBatch = int(float64(curBatch) * cfg.BatchBackoff)
		}
		if newBatch < cfg.MinBatchSize {
			newBatch = cfg.MinBatchSize
		}
		params.BatchSize.Store(int32(newBatch))
	}

	cfg.Logger.Warn("[throttle] backoff",
		"trigger", trigger,
		"delay", newDelay,
		"batch", newBatch,
		"ewma", time.Duration(ewmaLatencyNs),
		"baseline", time.Duration(baselineNs),
	)

	return time.Now().Add(cfg.CooldownTime)
}

// effectiveMaxDelay returns the dynamic ceiling: max(cfg.MaxDelay, 3×baseline).
func effectiveMaxDelay(baselineNs int64, cfg Config) time.Duration {
	baseline := time.Duration(baselineNs)
	dynamic := 3 * baseline
	if dynamic > cfg.MaxDelay {
		return dynamic
	}
	return cfg.MaxDelay
}
