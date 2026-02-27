package throttle

import (
	"context"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════
// Run — background decision loop
// ═══════════════════════════════════════════════════════════════════════

// Run starts the adaptive throttling decision loop. It blocks until ctx is cancelled.
// Call as: go controller.Run(ctx)
func (c *Controller) Run(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.DecisionTick)
	defer ticker.Stop()

	decayTicker := time.NewTicker(c.cfg.DecayInterval)
	defer decayTicker.Stop()

	stableCycles := 0

	for {
		select {
		case <-ctx.Done():
			return

		case <-decayTicker.C:
			// Exponential decay of error/request counters.
			factor := c.cfg.DecayFactor
			for {
				old := c.decayReqsX1k.Load()
				newVal := int64(float64(old) * factor)
				if c.decayReqsX1k.CompareAndSwap(old, newVal) {
					break
				}
			}
			for {
				old := c.decayErrsX1k.Load()
				newVal := int64(float64(old) * factor)
				if c.decayErrsX1k.CompareAndSwap(old, newVal) {
					break
				}
			}

		case <-ticker.C:
			curState := c.state.Load()

			// Skip if still calibrating.
			if curState == stateWarmup || curState == stateCalibrating {
				continue
			}

			// Check cooldown expiration.
			if curState == stateCooldown {
				// lastBackoff is checked in applyBackoff; here we just check time.
				stableCycles = 0
				// Allow transition back to normal after cooldown.
				c.state.CompareAndSwap(stateCooldown, stateNormal)
				continue
			}

			// ── Read metrics ──
			baseline := c.baselineNs.Load()
			ewma := c.ewmaLatencyNs.Load()
			curDelay := time.Duration(c.Params.Delay.Load())
			curBatch := int(c.Params.BatchSize.Load())
			reqs := c.decayReqsX1k.Load()
			errs := c.decayErrsX1k.Load()

			errorRate := 0.0
			if reqs > 0 {
				errorRate = float64(errs) / float64(reqs)
			}

			// ── BACK-OFF check ──
			if ewma > 2*baseline {
				stableCycles = 0
				c.applyBackoff(c.cfg.BackoffFactor, false, "latency_spike")
				continue
			}
			if errorRate > 0.10 {
				stableCycles = 0
				c.applyBackoff(c.cfg.BackoffFactor, true, "error_rate")
				continue
			}

			// ── RECOVERY check (all conditions must hold for 5 cycles) ──
			if ewma < int64(float64(baseline)*1.2) &&
				errorRate == 0 &&
				c.consecSuccess.Load() >= 10 {
				stableCycles++
			} else {
				stableCycles = 0
			}

			if stableCycles >= 5 {
				stableCycles = 0

				// Adaptive delay decrease: max(20ms, 10% of baseline)
				step := c.cfg.IncreaseStep
				baselineStep := time.Duration(float64(baseline) * 0.1)
				if baselineStep > step {
					step = baselineStep
				}
				newDelay := curDelay - step
				if newDelay < c.cfg.MinDelay {
					newDelay = c.cfg.MinDelay
				}
				c.Params.Delay.Store(int64(newDelay))

				// Batch: additive +1
				newBatch := curBatch + 1
				if newBatch > c.cfg.MaxBatchSize {
					newBatch = c.cfg.MaxBatchSize
				}
				c.Params.BatchSize.Store(int32(newBatch))

				c.cfg.Logger.Info("[throttle] recovery",
					"delay", newDelay,
					"batch", newBatch,
					"ewma", time.Duration(ewma),
					"baseline", time.Duration(baseline),
					"errorRate", errorRate,
				)
			} else {
				// Periodic telemetry even when no action is taken.
				c.cfg.Logger.Debug("[throttle] status",
					"state", "normal",
					"delay", curDelay,
					"batch", curBatch,
					"ewma", time.Duration(ewma),
					"baseline", time.Duration(baseline),
					"errorRate", errorRate,
					"stable", stableCycles,
					"consec", c.consecSuccess.Load(),
				)
			}
		}
	}
}

// ═══════════════════════════════════════════════════════════════════════
// applyBackoff — multiplicative decrease with hysteresis
// ═══════════════════════════════════════════════════════════════════════

func (c *Controller) applyBackoff(delayFactor float64, reduceBatch bool, trigger string) {
	curDelay := time.Duration(c.Params.Delay.Load())
	curBatch := int(c.Params.BatchSize.Load())

	// ── Delay: multiplicative increase ──
	newDelay := time.Duration(float64(curDelay) * delayFactor)
	maxDelay := c.effectiveMaxDelay()
	if newDelay > maxDelay {
		newDelay = maxDelay
	}
	c.Params.Delay.Store(int64(newDelay))

	// ── Batch: hybrid decrease ──
	if reduceBatch {
		var newBatch int
		if curBatch < 10 {
			// Small batches: subtract 1 to avoid jumping below minimum.
			newBatch = curBatch - 1
		} else {
			// Larger batches: multiplicative decrease.
			newBatch = int(float64(curBatch) * c.cfg.BatchBackoff)
		}
		if newBatch < c.cfg.MinBatchSize {
			newBatch = c.cfg.MinBatchSize
		}
		c.Params.BatchSize.Store(int32(newBatch))

		c.cfg.Logger.Warn("[throttle] backoff",
			"trigger", trigger,
			"delay", newDelay,
			"batch", newBatch,
			"ewma", time.Duration(c.ewmaLatencyNs.Load()),
			"baseline", time.Duration(c.baselineNs.Load()),
		)
	} else {
		c.cfg.Logger.Warn("[throttle] backoff",
			"trigger", trigger,
			"delay", newDelay,
			"batch", curBatch,
			"ewma", time.Duration(c.ewmaLatencyNs.Load()),
			"baseline", time.Duration(c.baselineNs.Load()),
		)
	}

	// Enter cooldown.
	c.state.Store(stateCooldown)

	// Schedule cooldown expiration: the decision loop will transition
	// back to normal after cfg.CooldownTime elapses (checked via cycle count).
	go func() {
		time.Sleep(c.cfg.CooldownTime)
		c.state.CompareAndSwap(stateCooldown, stateNormal)
	}()
}

// effectiveMaxDelay returns the dynamic ceiling: max(cfg.MaxDelay, 3×baseline).
func (c *Controller) effectiveMaxDelay() time.Duration {
	baseline := time.Duration(c.baselineNs.Load())
	dynamic := 3 * baseline
	if dynamic > c.cfg.MaxDelay {
		return dynamic
	}
	return c.cfg.MaxDelay
}
