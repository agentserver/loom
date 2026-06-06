package observerweb

import (
	"sync"
	"time"
)

type telemetryLimiter struct {
	mu        sync.Mutex
	perMinute int
	burst     int
	buckets   map[string]telemetryBucket
}

type telemetryBucket struct {
	windowStart time.Time
	count       int
}

func newTelemetryLimiter(perMinute, burst int) *telemetryLimiter {
	if perMinute <= 0 {
		perMinute = 60
	}
	if burst <= 0 {
		burst = perMinute
	}
	return &telemetryLimiter{
		perMinute: perMinute,
		burst:     burst,
		buckets:   map[string]telemetryBucket{},
	}
}

func (l *telemetryLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b.windowStart.IsZero() || now.Sub(b.windowStart) >= time.Minute {
		b = telemetryBucket{windowStart: now}
	}
	limit := l.perMinute
	if l.burst > limit {
		limit = l.burst
	}
	if b.count >= limit {
		l.buckets[key] = b
		return false
	}
	b.count++
	l.buckets[key] = b
	return true
}
