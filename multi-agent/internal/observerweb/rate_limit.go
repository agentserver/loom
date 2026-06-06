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
		buckets:   map[string]telemetryBucket{},
	}
}

func (l *telemetryLimiter) allow(key string, now time.Time) bool {
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
		return false
	}
	b.tokens--
	l.buckets[key] = b
	return true
}
