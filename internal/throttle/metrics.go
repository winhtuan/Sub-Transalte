package throttle

import (
	"time"
)

// requestMetric carries the outcome of a single HTTP round-trip.
// Sent from workers to the Run() goroutine via metricsCh.
type requestMetric struct {
	latency    time.Duration
	statusCode int
}

// RecordRequest records the outcome of an HTTP request for the throttler.
// This is the worker hot path: it performs a non-blocking channel send and
// returns immediately.  If the channel buffer is full the metric is dropped
// rather than blocking the worker — a small amount of data loss under extreme
// load is preferable to back-pressure.
//
// statusCode is the HTTP status code; 0 means the request failed before
// receiving a response.
func (c *Controller) RecordRequest(latency time.Duration, statusCode int) {
	select {
	case c.metricsCh <- requestMetric{latency: latency, statusCode: statusCode}:
	default: // buffer full — drop metric; throttle state is still consistent
	}
}
