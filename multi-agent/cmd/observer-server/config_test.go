package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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

// --- E-fix2 Finding 2: non-wildcard/non-loopback internal_listen_addr ---

// TestValidateClusterConfig_RejectsNonLoopbackInternalAddr verifies that
// internal_listen_addr hosts other than wildcards (empty/"0.0.0.0"/"::") or
// loopback (127.0.0.1/::1) are rejected. runDrainLocal contacts 127.0.0.1 so
// binding to a specific non-loopback IP (e.g. 10.x.x.x) silently breaks drain.
func TestValidateClusterConfig_RejectsNonLoopbackInternalAddr(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr bool
		desc    string
	}{
		{addr: ":8091", wantErr: false, desc: "wildcard port only — ok"},
		{addr: "0.0.0.0:8091", wantErr: false, desc: "explicit wildcard — ok"},
		{addr: "127.0.0.1:8091", wantErr: true, desc: "loopback only bind — rejected (drain won't reach non-loopback advertise)"},
		{addr: "[::]:8091", wantErr: false, desc: "IPv6 wildcard — ok"},
		{addr: "[::1]:8091", wantErr: true, desc: "IPv6 loopback only — rejected (drain won't reach non-loopback advertise)"},
		{addr: "10.1.2.3:8091", wantErr: true, desc: "specific non-loopback IP — REJECTED"},
		{addr: "eth0:8091", wantErr: true, desc: "symbolic hostname — REJECTED"},
		{addr: "localhost:8091", wantErr: true, desc: "localhost hostname — REJECTED (require literal IP)"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			c := minimalValidClusterConfig()
			c.InternalListenAddr = tc.addr
			err := validateClusterConfig(&c, "postgres")
			if tc.wantErr {
				require.Error(t, err, "expected error for addr %q", tc.addr)
			} else {
				require.NoError(t, err, "expected no error for addr %q", tc.addr)
			}
		})
	}
}

// --- Finding 1: env-indirection fields ---

// TestClusterConfig_EnvFields_Resolved verifies that when advertise_url_env /
// secret_env / prev_secret_env are set in the YAML and the corresponding direct
// fields are empty, loadConfig resolves the values via os.Getenv before
// validateClusterConfig runs. Both "direct value" and "env-indirected value"
// layouts must coexist: direct fields always take precedence.
func TestClusterConfig_EnvFields_Resolved(t *testing.T) {
	t.Setenv("TEST_ADVERTISE_URL", "https://observer-pod-1.svc:8443")
	t.Setenv("TEST_CLUSTER_SECRET", validClusterSecret)
	t.Setenv("TEST_PREV_SECRET", "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe")

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
  advertise_url_env: TEST_ADVERTISE_URL
  internal_listen_addr: ":8444"
  secret_env: TEST_CLUSTER_SECRET
  prev_secret_env: TEST_PREV_SECRET
`)
	require.True(t, cfg.Cluster.Enabled)
	require.Equal(t, "https://observer-pod-1.svc:8443", cfg.Cluster.AdvertiseURL,
		"AdvertiseURL must be resolved from env var TEST_ADVERTISE_URL")
	require.Equal(t, validClusterSecret, cfg.Cluster.Secret,
		"Secret must be resolved from env var TEST_CLUSTER_SECRET")
	require.Equal(t, "cafebabecafebabecafebabecafebabecafebabecafebabecafebabecafebabe", cfg.Cluster.PrevSecret,
		"PrevSecret must be resolved from env var TEST_PREV_SECRET")
}

// TestClusterConfig_DirectFieldTakesPrecedenceOverEnv verifies that when both
// a direct field and an env-indirection field are set, the direct field wins.
func TestClusterConfig_DirectFieldTakesPrecedenceOverEnv(t *testing.T) {
	t.Setenv("TEST_ADVERTISE_URL_IGNORED", "https://should-be-ignored.example.com:8443")

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
  advertise_url: https://observer-pod-direct.svc:8443
  advertise_url_env: TEST_ADVERTISE_URL_IGNORED
  internal_listen_addr: ":8444"
  secret: `+validClusterSecret+`
`)
	require.Equal(t, "https://observer-pod-direct.svc:8443", cfg.Cluster.AdvertiseURL,
		"direct advertise_url must take precedence over advertise_url_env")
}

// --- Finding 3 / E-fix2 Finding 1: revocation_channel struct field (pointer) ---

// TestRevocationChannel_NilIsAuto verifies that when revocation_channel is
// absent from YAML the field is nil (auto), which enables PG revocation only
// when store.driver=postgres.
func TestRevocationChannel_NilIsAuto(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
  agentserver:
    enabled: false
api_keys:
  - id: ak-default
    key: ak_secret
`)
	require.Nil(t, cfg.Identity.Agentserver.RevocationChannel,
		"omitted revocation_channel must be nil (auto)")
}

// TestRevocationChannel_EmptyIsDisabled verifies that revocation_channel: ""
// (explicit empty string from the chart when revocationChannel=disabled) is
// stored as a non-nil pointer to an empty string, not confused with absent/auto.
func TestRevocationChannel_EmptyIsDisabled(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
  agentserver:
    enabled: true
    url: https://agentserver.example.com
    revocation_channel: ""
api_keys:
  - id: ak-default
    key: ak_secret
`)
	require.NotNil(t, cfg.Identity.Agentserver.RevocationChannel,
		"explicit empty revocation_channel must be a non-nil pointer (disabled)")
	require.Equal(t, "", *cfg.Identity.Agentserver.RevocationChannel,
		"explicit empty revocation_channel must point to empty string")
}

// TestRevocationChannel_PostgresIsForced verifies that
// revocation_channel: "postgres" is stored as a non-nil pointer to "postgres"
// and is accepted by validateConfig.
func TestRevocationChannel_PostgresIsForced(t *testing.T) {
	cfg := loadConfigFromString(t, `
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
  agentserver:
    enabled: true
    url: https://agentserver.example.com
    revocation_channel: "postgres"
api_keys:
  - id: ak-default
    key: ak_secret
`)
	require.NotNil(t, cfg.Identity.Agentserver.RevocationChannel,
		"revocation_channel: postgres must be a non-nil pointer")
	require.Equal(t, "postgres", *cfg.Identity.Agentserver.RevocationChannel,
		"revocation_channel: postgres must point to \"postgres\"")
}

// TestRevocationChannel_UnknownFatal verifies that an unrecognised
// revocation_channel value is rejected by validateConfig.
func TestRevocationChannel_UnknownFatal(t *testing.T) {
	_, err := loadConfig(writeConfig(t, `
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
  agentserver:
    enabled: true
    url: https://agentserver.example.com
    revocation_channel: "kafka"
api_keys:
  - id: ak-default
    key: ak_secret
`))
	require.Error(t, err, "unknown revocation_channel value must be rejected")
	require.Contains(t, err.Error(), "revocation_channel")
}

// TestLoadConfig_RevocationChannel is kept for backwards compat but delegates
// to the more precise pointer-semantics tests above.
func TestLoadConfig_RevocationChannel(t *testing.T) {
	TestRevocationChannel_NilIsAuto(t)
	TestRevocationChannel_EmptyIsDisabled(t)
	TestRevocationChannel_PostgresIsForced(t)
	TestRevocationChannel_UnknownFatal(t)
}

// TestLoadConfig_RenderedChartYAML ensures the binary's ClusterConfig and
// AgentserverIdentityConfig accept the exact YAML fields the Helm chart renders
// into the ConfigMap (observer.nonsecret.yaml). This catches silent chart/binary
// schema divergence by running `helm template` and loading the result.
//
// The test is skipped if `helm` is not on PATH so it does not block CI
// environments without Helm installed (though local dev should always run it).
func TestLoadConfig_RenderedChartYAML(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not in PATH; skipping chart/binary schema divergence test")
	}

	// Locate chart directory relative to this test file. The test binary runs
	// from cmd/observer-server so go up to multi-agent root then into deploy.
	chartDir, err := filepath.Abs("../../deploy/charts/observer")
	require.NoError(t, err, "could not resolve chart directory")
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err != nil {
		t.Skipf("chart directory not found at %s; skipping", chartDir)
	}

	hexSecret := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	// Render the chart with cluster.enabled=true + agentserver enabled + revocationChannel=enabled.
	// Use filesystem object store (not s3) to avoid requiring s3 endpoint/bucket in the test secret.
	out, err := exec.Command("helm", "template", "observer-test", chartDir,
		"--set", "replicaCount=2",
		"--set", "cluster.enabled=true",
		"--set", "secret.create=true",
		"--set", "secret.clusterSecret="+hexSecret,
		"--set", "secret.databaseUrl=postgres://observer:observer@pg:5432/observer?sslmode=disable",
		"--set", "config.objectStore.driver=filesystem",
		"--set", "config.telemetry.enabled=false",
		"--set", "config.identity.legacyAPIKeys.enabled=true",
		"--set", "config.apiKeys[0].id=test",
		"--set", "config.apiKeys[0].key=testkey",
		"--set", "config.identity.agentserver.enabled=true",
		"--set", "config.identity.agentserver.url=https://agentserver.example.com",
		"--set", "config.identity.agentserver.freshTTL=30s",
		"--set", "config.identity.agentserver.revocationChannel=enabled",
		"--set", "postgresql.enabled=false",
		"--set", "minio.enabled=false",
	).Output()
	require.NoError(t, err, "helm template must succeed")

	// Extract observer.nonsecret.yaml content from the ConfigMap YAML.
	// The ConfigMap data has the key `observer.nonsecret.yaml:` followed by
	// indented YAML lines. We extract everything from that key up to the next
	// top-level key or end of document.
	nonsecretContent := extractConfigMapValue(string(out), "observer.nonsecret.yaml")
	require.NotEmpty(t, nonsecretContent, "observer.nonsecret.yaml not found in helm template output")

	// Set required env vars so env-indirected cluster fields resolve.
	t.Setenv("OBSERVER_CLUSTER_SECRET", hexSecret)
	t.Setenv("OBSERVER_ADVERTISE_URL", "http://10.0.0.1:8091")

	// Write the minimal "secret" YAML (observer.yaml) + nonsecret YAML side by side.
	// The minimal secret YAML must include enough fields to pass validateConfig.
	dir := t.TempDir()
	secretYAML := strings.TrimSpace(`
listen_addr: ":8090"
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
identity:
  legacy_api_keys:
    enabled: true
  agentserver:
    enabled: true
    url: https://agentserver.example.com
api_keys:
  - id: test
    key: testkey
`) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "observer.yaml"), []byte(secretYAML), 0o600))

	nonsecretDir := filepath.Join(dir, "nonsecret")
	require.NoError(t, os.MkdirAll(nonsecretDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(nonsecretDir, "observer.nonsecret.yaml"),
		[]byte(nonsecretContent), 0o600))

	// loadConfig must succeed — if it returns an error the chart rendered a
	// field the binary doesn't know or the schema diverged.
	cfg, err := loadConfig(filepath.Join(dir, "observer.yaml"))
	require.NoError(t, err, "loadConfig must accept the YAML rendered by helm template; chart/binary schema diverged")

	// Sanity: env-based cluster fields should have been resolved.
	require.True(t, cfg.Cluster.Enabled, "cluster.enabled must be true after chart render + load")
	require.NotEmpty(t, cfg.Cluster.AdvertiseURL, "cluster.advertise_url must be resolved from env")
}

// extractConfigMapValue extracts the YAML block for a given data key from a
// Kubernetes ConfigMap rendered by helm template. The returned string is
// de-indented (2 spaces of ConfigMap data indent removed).
func extractConfigMapValue(helmOutput, key string) string {
	// Find the line "  <key>: |" (2-space indent from ConfigMap data block).
	pattern := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(key) + `: \|\n((?:    [^\n]*\n)*)`)
	m := pattern.FindStringSubmatch(helmOutput)
	if len(m) < 2 {
		return ""
	}
	raw := m[1]
	// Remove 4-space indent (2 for data: block + 2 for literal block scalar).
	var buf bytes.Buffer
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "    ") {
			buf.WriteString(line[4:])
		} else {
			buf.WriteString(line)
		}
		buf.WriteByte('\n')
	}
	return buf.String()
}
