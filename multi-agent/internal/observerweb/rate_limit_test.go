package observerweb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTelemetryLimiterUsesTokenBucketRateAndBurst(t *testing.T) {
	start := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	limiter := newTelemetryLimiter(2, 4)

	require.True(t, limiter.allow("agent", start))
	require.True(t, limiter.allow("agent", start))
	require.True(t, limiter.allow("agent", start))
	require.True(t, limiter.allow("agent", start))
	require.False(t, limiter.allow("agent", start))

	require.True(t, limiter.allow("agent", start.Add(30*time.Second)))
	require.False(t, limiter.allow("agent", start.Add(30*time.Second)))

	require.True(t, limiter.allow("agent", start.Add(time.Minute)))
	require.False(t, limiter.allow("agent", start.Add(time.Minute)))

	idle := start.Add(10 * time.Minute)
	for i := 0; i < 4; i++ {
		require.True(t, limiter.allow("agent", idle))
	}
	require.False(t, limiter.allow("agent", idle))
}
