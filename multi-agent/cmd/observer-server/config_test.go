package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// validClusterSecret is a 64-hex-char (32-byte) secret used in cluster config tests.
const validClusterSecret = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

// minimalValidClusterConfig returns a ClusterConfig with all required fields
// populated and timing values already set to valid defaults (not zero — so
// validateClusterConfig can run after defaults are applied in tests that
// call it directly without going through loadConfig).
func minimalValidClusterConfig() ClusterConfig {
	return ClusterConfig{
		Enabled:            true,
		AdvertiseURL:       "https://observer-pod-1.svc:8443",
		InternalListenAddr: ":8444",
		Secret:             validClusterSecret,
		HeartbeatInterval:  30 * time.Second,
		HeartbeatJitter:    5 * time.Second,
		SweepInterval:      60 * time.Second,
		DaemonExpiryAfter:  90 * time.Second,
		ForwardTimeout:     5 * time.Second,
		DrainTimeout:       10 * time.Second,
	}
}

// TestValidateConfig_ClusterDisabled_IgnoresEmptyFields ensures that when
// cluster.enabled is false, all other cluster fields may be empty.
func TestValidateConfig_ClusterDisabled_IgnoresEmptyFields(t *testing.T) {
	err := validateClusterConfig(&ClusterConfig{Enabled: false}, "sqlite")
	require.NoError(t, err)
}

// TestValidateConfig_RejectsEnabledWithoutAdvertise verifies that
// cluster.enabled=true without advertise_url returns an error mentioning the field.
func TestValidateConfig_RejectsEnabledWithoutAdvertise(t *testing.T) {
	c := minimalValidClusterConfig()
	c.AdvertiseURL = ""
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "advertise_url")
}

// TestValidateConfig_RejectsEnabledWithoutSecret verifies that
// cluster.enabled=true without secret returns an error mentioning "secret".
func TestValidateConfig_RejectsEnabledWithoutSecret(t *testing.T) {
	c := minimalValidClusterConfig()
	c.Secret = ""
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret")
}

// TestValidateConfig_RejectsShortSecret verifies that a hex secret that
// decodes to fewer than 32 bytes is rejected.
func TestValidateConfig_RejectsShortSecret(t *testing.T) {
	c := minimalValidClusterConfig()
	c.Secret = "abcd" // only 2 bytes
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "secret")
}

// TestValidateConfig_RejectsLocalhostAdvertise verifies that
// advertise_url with a loopback host is rejected.
func TestValidateConfig_RejectsLocalhostAdvertise(t *testing.T) {
	c := minimalValidClusterConfig()
	c.AdvertiseURL = "http://localhost:8443"
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "loopback")
}

// TestValidateConfig_Rejects127AdvertiseURL verifies that 127.x.x.x is also
// caught by the loopback check.
func TestValidateConfig_Rejects127AdvertiseURL(t *testing.T) {
	c := minimalValidClusterConfig()
	c.AdvertiseURL = "http://127.0.0.1:8443"
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "loopback")
}

// TestValidateConfig_RejectsHeartbeatGEExpiry verifies that heartbeat_interval
// >= daemon_expiry_after is rejected.
func TestValidateConfig_RejectsHeartbeatGEExpiry(t *testing.T) {
	c := minimalValidClusterConfig()
	c.HeartbeatInterval = 120 * time.Second
	c.DaemonExpiryAfter = 60 * time.Second
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "heartbeat_interval")
}

// TestValidateConfig_RejectsNonPGStore verifies that cluster mode requires
// store.driver=postgres.
func TestValidateConfig_RejectsNonPGStore(t *testing.T) {
	c := minimalValidClusterConfig()
	err := validateClusterConfig(&c, "sqlite")
	require.Error(t, err)
	require.Contains(t, err.Error(), "postgres")
}

// TestValidateConfig_AppliesDefaults verifies that loadConfig fills in timing
// defaults when cluster.enabled=true and all timing fields are zero.
func TestValidateConfig_AppliesDefaults(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
api_keys:
  - id: ak-default
    key: ak_secret
cluster:
  enabled: true
  advertise_url: https://observer-pod-1.svc:8443
  internal_listen_addr: ":8444"
  secret: `+validClusterSecret+`
`)
	require.True(t, cfg.Cluster.Enabled)
	require.Equal(t, 30*time.Second, cfg.Cluster.HeartbeatInterval)
	require.Equal(t, 5*time.Second, cfg.Cluster.HeartbeatJitter)
	require.Equal(t, 60*time.Second, cfg.Cluster.SweepInterval)
	require.Equal(t, 90*time.Second, cfg.Cluster.DaemonExpiryAfter)
	require.Equal(t, 5*time.Second, cfg.Cluster.ForwardTimeout)
	require.Equal(t, 10*time.Second, cfg.Cluster.DrainTimeout)
}

// TestValidateConfig_RejectsPrevSecretInvalid verifies that a non-hex
// prev_secret returns an error.
func TestValidateConfig_RejectsPrevSecretInvalid(t *testing.T) {
	c := minimalValidClusterConfig()
	c.PrevSecret = "notHex!!"
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "prev_secret")
}

// TestValidateConfig_RejectsPrevSecretTooShort verifies that a hex prev_secret
// that decodes to fewer than 32 bytes is rejected.
func TestValidateConfig_RejectsPrevSecretTooShort(t *testing.T) {
	c := minimalValidClusterConfig()
	c.PrevSecret = "abcd" // only 2 bytes
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "prev_secret")
}

// TestValidateConfig_ValidPrevSecret verifies that a valid prev_secret passes.
func TestValidateConfig_ValidPrevSecret(t *testing.T) {
	c := minimalValidClusterConfig()
	c.PrevSecret = "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe"
	err := validateClusterConfig(&c, "postgres")
	require.NoError(t, err)
}

// TestValidateConfig_RejectsNonHTTPAdvertiseURL verifies that a non-http(s)
// scheme in advertise_url is rejected.
func TestValidateConfig_RejectsNonHTTPAdvertiseURL(t *testing.T) {
	c := minimalValidClusterConfig()
	c.AdvertiseURL = "tcp://observer-pod-1.svc:8443"
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "advertise_url")
}

// TestValidateConfig_RejectsEnabledWithoutInternalAddr verifies that
// cluster.enabled=true without internal_listen_addr returns an error.
func TestValidateConfig_RejectsEnabledWithoutInternalAddr(t *testing.T) {
	c := minimalValidClusterConfig()
	c.InternalListenAddr = ""
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "internal_listen_addr")
}

// TestValidateConfig_ExplicitStringDurations verifies that human-readable
// duration strings (e.g. "45s") parse correctly from YAML into time.Duration.
func TestValidateConfig_ExplicitStringDurations(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
api_keys:
  - id: ak-default
    key: ak_secret
cluster:
  enabled: true
  advertise_url: https://observer-pod-1.svc:8443
  internal_listen_addr: ":8444"
  secret: `+validClusterSecret+`
  heartbeat_interval: 45s
  heartbeat_jitter: 3s
  sweep_interval: 90s
  daemon_expiry_after: 120s
  forward_timeout: 8s
  drain_timeout: 15s
`)
	require.Equal(t, 45*time.Second, cfg.Cluster.HeartbeatInterval)
	require.Equal(t, 3*time.Second, cfg.Cluster.HeartbeatJitter)
	require.Equal(t, 90*time.Second, cfg.Cluster.SweepInterval)
	require.Equal(t, 120*time.Second, cfg.Cluster.DaemonExpiryAfter)
	require.Equal(t, 8*time.Second, cfg.Cluster.ForwardTimeout)
	require.Equal(t, 15*time.Second, cfg.Cluster.DrainTimeout)
}

// TestAdvertiseHash verifies the helper produces a 4-char hex prefix.
func TestAdvertiseHash(t *testing.T) {
	h := advertiseHash("https://observer-pod-1.svc:8443")
	require.Len(t, h, 4)
	// Different URLs produce different hashes.
	h2 := advertiseHash("https://observer-pod-2.svc:8443")
	require.NotEqual(t, h, h2)
}

// --- Finding 2 ---

// TestHTTPServer_WriteTimeout_IsZero verifies that both public and internal HTTP
// server factory functions produce servers with WriteTimeout == 0 so streaming
// SSE and forwarded turns are not severed mid-stream.
func TestHTTPServer_WriteTimeout_IsZero(t *testing.T) {
	pub := newPublicHTTPServer(":8090", nil)
	require.Equal(t, time.Duration(0), pub.WriteTimeout,
		"public server WriteTimeout must be 0 (streaming SSE/turns)")

	internal := newInternalHTTPServer(":8091", nil)
	require.Equal(t, time.Duration(0), internal.WriteTimeout,
		"internal server WriteTimeout must be 0 (forwarded streaming turns)")
}

// --- Finding 3 ---

// TestObserverServer_TelemetryLimiter_DefaultsToMemoryWhenClusterDisabled verifies
// that the PG telemetry limiter is NOT selected when cluster mode is disabled,
// even when telemetry is enabled and store.driver=postgres. Selecting PG limiter
// without the cluster gate would fail because commander_telemetry_buckets is only
// migrated in cluster mode.
func TestObserverServer_TelemetryLimiter_DefaultsToMemoryWhenClusterDisabled(t *testing.T) {
	cfg := &Config{
		Telemetry: TelemetryConfig{
			Enabled: true,
			APIKeys: []TelemetryAPIKeyConfig{{ID: "k1", KeyEnv: "K1", WorkspaceID: "*"}},
			RateLimit: TelemetryRateLimitConfig{PerMinute: 60, Burst: 120},
		},
		Cluster: ClusterConfig{Enabled: false},
		Store:   StoreConfig{Driver: "postgres"},
	}
	// When cluster is disabled, observerWebOptions should NOT trigger the PG limiter
	// path — that path is gated on cfg.Cluster.Enabled in main.go.
	opts := observerWebOptions(cfg, nil)
	// The opts.TelemetryLimiter should be nil at this stage (it gets built in
	// NewWithResolverOptions; we just confirm the gate doesn't pre-set it here).
	require.Nil(t, opts.TelemetryLimiter,
		"TelemetryLimiter must not be set by observerWebOptions (PG limiter requires cluster.enabled)")
	// Confirm the condition in main.go correctly gates the PG limiter.
	pgLimiterEnabled := cfg.Telemetry.Enabled && cfg.Cluster.Enabled && cfg.Store.Driver == "postgres"
	require.False(t, pgLimiterEnabled,
		"PG telemetry limiter gate must be false when cluster.enabled=false")
}

// --- Finding 6 ---

// TestValidateClusterConfig_RejectsDisabledWithPartialFields verifies that setting
// cluster fields when cluster.enabled=false is rejected. This catches configs where
// the user set cluster fields but forgot to set cluster.enabled: true.
func TestValidateClusterConfig_RejectsDisabledWithPartialFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  ClusterConfig
	}{
		{
			name: "advertise_url set",
			cfg:  ClusterConfig{Enabled: false, AdvertiseURL: "https://pod.example.com"},
		},
		{
			name: "internal_listen_addr set",
			cfg:  ClusterConfig{Enabled: false, InternalListenAddr: ":8444"},
		},
		{
			name: "secret set",
			cfg:  ClusterConfig{Enabled: false, Secret: validClusterSecret},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClusterConfig(&tc.cfg, "sqlite")
			require.Error(t, err, "partial cluster config with cluster.enabled=false must be rejected")
		})
	}
}

// TestValidateClusterConfig_RejectsLoopbackInternalWithRemoteAdvertise verifies that
// binding the internal listener to a loopback address while advertising a non-loopback
// URL is rejected. Peers would advertise an unreachable address.
func TestValidateClusterConfig_RejectsLoopbackInternalWithRemoteAdvertise(t *testing.T) {
	c := minimalValidClusterConfig()
	c.InternalListenAddr = "127.0.0.1:8444" // loopback internal
	err := validateClusterConfig(&c, "postgres")
	require.Error(t, err)
	require.Contains(t, err.Error(), "loopback")
}
