package observerweb

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTelemetryLimiterUsesTokenBucketRateAndBurst(t *testing.T) {
	start := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	limiter := newTelemetryLimiter(2, 4)
	key := telemetryKey{
		WorkspaceID:    "ws1",
		AgentID:        "agent1",
		TelemetryKeyID: "key1",
	}

	allow, err := limiter.allow(key, start)
	require.NoError(t, err)
	require.True(t, allow)

	allow, err = limiter.allow(key, start)
	require.NoError(t, err)
	require.True(t, allow)

	allow, err = limiter.allow(key, start)
	require.NoError(t, err)
	require.True(t, allow)

	allow, err = limiter.allow(key, start)
	require.NoError(t, err)
	require.True(t, allow)

	allow, err = limiter.allow(key, start)
	require.NoError(t, err)
	require.False(t, allow)

	allow, err = limiter.allow(key, start.Add(30*time.Second))
	require.NoError(t, err)
	require.True(t, allow)

	allow, err = limiter.allow(key, start.Add(30*time.Second))
	require.NoError(t, err)
	require.False(t, allow)

	allow, err = limiter.allow(key, start.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, allow)

	allow, err = limiter.allow(key, start.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, allow)

	idle := start.Add(10 * time.Minute)
	for i := 0; i < 4; i++ {
		allow, err := limiter.allow(key, idle)
		require.NoError(t, err)
		require.True(t, allow)
	}

	allow, err = limiter.allow(key, idle)
	require.NoError(t, err)
	require.False(t, allow)
}
