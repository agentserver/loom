package observerweb

import (
	"context"
	"sync"
	"time"
)

// telemetryKey identifies a unique rate limit bucket across workspace, agent, and API key.
type telemetryKey struct {
	WorkspaceID   string
	AgentID       string
	TelemetryKeyID string
}

// telemetryAllower determines whether to allow a telemetry event.
// Returns (true, nil) to proceed, (false, nil) to reject with 429, or (_, err) to reject with 503.
type telemetryAllower interface {
	allow(ctx context.Context, key telemetryKey, now time.Time) (bool, error)
}

type telemetryLimiter struct {
	mu        sync.Mutex
	perMinute int
	burst     int
	buckets   map[telemetryKey]telemetryBucket
}

type telemetryBucket struct {
	last   time.Time
	tokens float64
}

func newTelemetryLimiter(perMinute, burst int) *telemetryLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = perMinute
	}
	if burst < 1 {
		burst = 1
	}
	return &telemetryLimiter{
		perMinute: perMinute,
		burst:     burst,
		buckets:   map[telemetryKey]telemetryBucket{},
	}
}

func (l *telemetryLimiter) allow(_ context.Context, key telemetryKey, now time.Time) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b.last.IsZero() {
		b = telemetryBucket{last: now, tokens: float64(l.burst)}
	} else if now.After(b.last) {
		b.tokens += now.Sub(b.last).Minutes() * float64(l.perMinute)
		if b.tokens > float64(l.burst) {
			b.tokens = float64(l.burst)
		}
		b.last = now
	}
	if b.tokens < 1 {
		l.buckets[key] = b
		return false, nil
	}
	b.tokens--
	l.buckets[key] = b
	return true, nil
}
