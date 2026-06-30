package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/commanderhub"
	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/identity"
	agentidentity "github.com/yourorg/multi-agent/internal/identity/agentserver"
	"github.com/yourorg/multi-agent/internal/identity/static"
	"github.com/yourorg/multi-agent/internal/objectstore"
	"github.com/yourorg/multi-agent/internal/observerstore"
	pgobs "github.com/yourorg/multi-agent/internal/observerstore/postgres"
	"github.com/yourorg/multi-agent/internal/observerweb"
	"github.com/yourorg/multi-agent/internal/userspace"
)

type Config struct {
	ListenAddr  string            `yaml:"listen_addr"`
	DBPath      string            `yaml:"db_path"`
	APIKeys     []APIKeyConfig    `yaml:"api_keys"`
	Store       StoreConfig       `yaml:"store"`
	ObjectStore ObjectStoreConfig `yaml:"object_store"`
	Telemetry   TelemetryConfig   `yaml:"telemetry"`
	Identity    IdentityConfig    `yaml:"identity"`
	Cluster     ClusterConfig     `yaml:"cluster"`
	Production  bool              `yaml:"production"`
}

// ClusterConfig configures multi-pod cluster mode for the observer-server.
// When Enabled is false all other fields are ignored. When Enabled is true
// the server starts a second internal HTTP listener on InternalListenAddr and
// registers itself in the shared Postgres registry via AdvertiseURL.
//
// Env-indirection fields (advertise_url_env, secret_env, prev_secret_env):
// If the direct value field (e.g. AdvertiseURL) is empty but the corresponding
// *Env field is non-empty, loadConfig resolves the value via os.Getenv after
// YAML merge. This allows Kubernetes Deployments to inject per-pod values
// (e.g. POD_IP-derived advertise URL) via environment variables while keeping
// the config file in a ConfigMap rather than a Secret.
type ClusterConfig struct {
	Enabled             bool          `yaml:"enabled"`
	AdvertiseURL        string        `yaml:"advertise_url"`
	AdvertiseURLEnv     string        `yaml:"advertise_url_env,omitempty"`
	InternalListenAddr  string        `yaml:"internal_listen_addr"`
	Secret              string        `yaml:"secret"`
	SecretEnv           string        `yaml:"secret_env,omitempty"`
	PrevSecret          string        `yaml:"prev_secret,omitempty"`
	PrevSecretEnv       string        `yaml:"prev_secret_env,omitempty"`
	HeartbeatInterval   time.Duration `yaml:"heartbeat_interval"`
	HeartbeatJitter     time.Duration `yaml:"heartbeat_jitter"`
	SweepInterval       time.Duration `yaml:"sweep_interval"`
	DaemonExpiryAfter   time.Duration `yaml:"daemon_expiry_after"`
	ForwardTimeout      time.Duration `yaml:"forward_timeout"`
	DrainTimeout        time.Duration `yaml:"drain_timeout"`
	SessionListCacheTTL time.Duration `yaml:"session_list_cache_ttl"`
}

type APIKeyConfig struct {
	ID   string `yaml:"id"`
	Key  string `yaml:"key"`
	Note string `yaml:"note,omitempty"`
}

type StoreConfig struct {
	Driver                  string         `yaml:"driver"`
	SQLite                  SQLiteConfig   `yaml:"sqlite"`
	Postgres                PostgresConfig `yaml:"postgres"`
	AllowSQLiteInProduction bool           `yaml:"allow_sqlite_in_production"`
}

type SQLiteConfig struct {
	Path string `yaml:"path"`
}

type PostgresConfig struct {
	DSNEnv          string `yaml:"dsn_env"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

type ObjectStoreConfig struct {
	Driver string      `yaml:"driver"`
	S3     S3Config    `yaml:"s3"`
	Proxy  ProxyConfig `yaml:"proxy"`
}

type S3Config struct {
	Endpoint     string `yaml:"endpoint"`
	Region       string `yaml:"region"`
	Bucket       string `yaml:"bucket"`
	UseSSL       bool   `yaml:"use_ssl"`
	AccessKeyEnv string `yaml:"access_key_env"`
	SecretKeyEnv string `yaml:"secret_key_env"`
	PresignTTL   string `yaml:"presign_ttl"`
}

type ProxyConfig struct {
	Enabled  bool  `yaml:"enabled"`
	MaxBytes int64 `yaml:"max_bytes"`
}

type TelemetryConfig struct {
	Enabled       bool                     `yaml:"enabled"`
	APIKeys       []TelemetryAPIKeyConfig  `yaml:"api_keys"`
	RateLimit     TelemetryRateLimitConfig `yaml:"rate_limit"`
	MaxBodyBytes  int64                    `yaml:"max_body_bytes"`
	RetentionDays int                      `yaml:"retention_days"`
}

type TelemetryAPIKeyConfig struct {
	ID          string `yaml:"id"`
	KeyEnv      string `yaml:"key_env"`
	WorkspaceID string `yaml:"workspace_id"`
	Note        string `yaml:"note,omitempty"`
}

type TelemetryRateLimitConfig struct {
	PerMinute int `yaml:"per_minute"`
	Burst     int `yaml:"burst"`
}

type IdentityConfig struct {
	Agentserver   AgentserverIdentityConfig `yaml:"agentserver"`
	LegacyAPIKeys LegacyAPIKeysConfig       `yaml:"legacy_api_keys"`
}

type AgentserverIdentityConfig struct {
	Enabled           bool           `yaml:"enabled"`
	URL               string         `yaml:"url"`
	FreshTTL          durationConfig `yaml:"fresh_ttl"`
	StaleGrace        durationConfig `yaml:"stale_grace"`
	RequestTimeout    durationConfig `yaml:"request_timeout"`
	CacheCapacity     int            `yaml:"cache_capacity"`
	StartupProbe      bool           `yaml:"startup_probe"`
	// RevocationChannel controls which cross-pod revocation backend to use.
	// The field is a pointer so that absent (nil) and explicit-empty ("") are
	// semantically distinct:
	//
	//   nil        — "auto": attach PG revocation channel when store.driver=postgres
	//                (same as the pre-v19 behaviour; safe for single-pod deployments)
	//   ptr("")    — "disabled": never attach the PG revocation channel, even with a
	//                Postgres store. The chart emits revocation_channel: "" when
	//                revocationChannel=disabled so this is reliably distinguishable
	//                from the absent/auto case.
	//   ptr("postgres") — always attach the PG revocation channel (explicit opt-in;
	//                required when running multi-pod without cluster.enabled but with
	//                a shared Postgres store). The chart emits
	//                revocation_channel: "postgres" when revocationChannel=enabled.
	//
	// Any other non-nil value is rejected as fatal at startup.
	RevocationChannel *string `yaml:"revocation_channel"`
}

type LegacyAPIKeysConfig struct {
	Enabled bool `yaml:"enabled"`
}

type durationConfig time.Duration

func (d *durationConfig) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return err
	}
	*d = durationConfig(parsed)
	return nil
}

func (d durationConfig) Duration() time.Duration {
	return time.Duration(d)
}

func main() {
	cfgPath := flag.String("config", "observer.yaml", "path to observer config")
	migrateOnly := flag.Bool("migrate-only", false, "run database migrations and exit")
	retentionCleanup := flag.Bool("retention-cleanup", false, "delete expired telemetry events and exit")
	drainLocal := flag.Bool("drain-local", false, "POST to the local internal drain endpoint and exit (used by preStop hook)")
	internalPort := flag.Int("internal-port", 0, "internal listener port for --drain-local (overrides config cluster.internal_listen_addr port)")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath, *migrateOnly || *retentionCleanup)
	if err != nil {
		if *drainLocal {
			// On drain, config errors are fatal — the operator must fix them.
			log.Fatalf("drain-local: failed to load config: %v", err)
		}
		log.Fatal(err)
	}
	if *migrateOnly {
		if err := runMigrationsOnly(cfg); err != nil {
			log.Fatal(err)
		}
		log.Printf("observer-server migrations complete")
		return
	}
	if *retentionCleanup {
		deleted, err := runRetentionCleanup(cfg)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("observer-server retention cleanup deleted %d events", deleted)
		return
	}
	if *drainLocal {
		if err := runDrainLocal(cfg, *internalPort); err != nil {
			log.Fatalf("drain-local: %v", err)
		}
		return
	}

	st, err := openObserverStore(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	specs := make([]observerstore.APIKeySpec, 0, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		specs = append(specs, observerstore.APIKeySpec{ID: k.ID, Key: k.Key, Note: k.Note})
	}
	if cfg.Identity.LegacyAPIKeys.Enabled {
		if err := st.ReplaceAPIKeys(specs); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("observer-server loaded %d api_keys", len(specs))

	// Run authstore migration BEFORE building the identity resolver.
	// The PG revocation channel (LISTEN/NOTIFY) requires the
	// commander_identity_revocations table to exist at subscribe time. If we
	// migrate after building the resolver, the subscribe call fails once (fresh
	// DB) and is never retried — resulting in no cross-pod revocations.
	//
	// Also migrates when the cluster telemetry PG limiter is selected
	// (telemetry + cluster + postgres) so commander_telemetry_buckets is
	// present before the first telemetry request hits the table.
	if cfg.Store.Driver == "postgres" && needsCommanderDDL(cfg) {
		if err := authstore.MigratePostgres(st.DB()); err != nil {
			log.Fatalf("commanderhub authstore migrate (pre-resolver): %v", err)
		}
	}

	resolver, err := buildIdentityResolver(cfg, st)
	if err != nil {
		log.Fatal(err)
	}
	if err := probeAgentserverWhoami(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}

	if err := configureTelemetryAPIKeys(st, cfg); err != nil {
		log.Fatal(err)
	}
	if cfg.Telemetry.Enabled {
		log.Printf("observer-server loaded %d telemetry api_keys", len(cfg.Telemetry.APIKeys))
	} else {
		log.Printf("observer-server cleared telemetry api_keys")
	}
	objects, err := openObjectStore(cfg)
	if err != nil {
		log.Fatal(err)
	}

	if shouldMigrateUserspaceOnStartup(cfg.Store.Driver) {
		if err := userspace.MigrateForDriver(st.DB(), cfg.Store.Driver); err != nil {
			log.Fatalf("userspace migrate: %v", err)
		}
	}
	blobs, err := openUserspaceBlobStore(st.DB(), cfg, objects)
	if err != nil {
		log.Fatalf("userspace blob store: %v", err)
	}
	store, err := openUserspaceStore(st.DB(), cfg)
	if err != nil {
		log.Fatalf("userspace store: %v", err)
	}
	usHandler := &userspace.Handler{
		Store: store,
		Blobs: blobs,
		Resolver: func(r *http.Request) (userspace.Identity, bool) {
			ident, ok := observerweb.IdentityFromRequest(resolver, r)
			return userspace.Identity{UserID: ident.UserID, WorkspaceID: ident.WorkspaceID, AgentID: ident.AgentID}, ok
		},
	}

	// Only build the auth store + apply commander DDL when commander is
	// actually being mounted. observerweb.NewWithResolverOptionsHub guards the
	// MountAll call by AgentserverURL != "" (see internal/observerweb/server.go),
	// so a non-commander Postgres deployment has no use for commander_logins /
	// commander_sessions and shouldn't pay the migration cost or be coupled to
	// new DDL during rollouts.
	opts := observerWebOptions(cfg, objects)
	if cfg.Telemetry.Enabled && cfg.Cluster.Enabled && cfg.Store.Driver == "postgres" {
		// Use the shared-Postgres token-bucket limiter only in cluster mode.
		// The commander_telemetry_buckets table is only migrated behind the cluster
		// gate; a single-pod Postgres deployment lacks the table and would get 503s.
		// Single-pod deployments keep the in-memory limiter.
		observerweb.SetPGTelemetryLimiter(
			&opts,
			st.DB(),
			cfg.Telemetry.RateLimit.PerMinute,
			cfg.Telemetry.RateLimit.Burst,
		)
	}
	if opts.AgentserverURL != "" {
		// buildCommanderAuthStore may migrate again (idempotent) but skips it
		// if already done above (postgres: IF NOT EXISTS DDL is idempotent).
		authStore, err := buildCommanderAuthStore(cfg, st.DB())
		if err != nil {
			log.Fatal(err)
		}
		opts.AuthStore = authStore
	}

	// Wire cluster mode: when enabled, build the ClusterRuntime (with timing
	// overrides) and provide an internalMux for the dual-listener setup.
	if cfg.Cluster.Enabled {
		secret, _ := hex.DecodeString(cfg.Cluster.Secret)
		var prevSecret []byte
		if cfg.Cluster.PrevSecret != "" {
			prevSecret, _ = hex.DecodeString(cfg.Cluster.PrevSecret)
		}
		opts.Cluster = commanderhub.ClusterRuntime{
			DB:                 st.DB(),
			AdvertiseURL:       cfg.Cluster.AdvertiseURL,
			Secret:             secret,
			PrevSecret:         prevSecret,
			InternalListenAddr: cfg.Cluster.InternalListenAddr,
			// Propagate timing config values so they are used by sharedRegistry /
			// forwardClient instead of their hardcoded defaults.
			HeartbeatInterval: cfg.Cluster.HeartbeatInterval,
			SweepInterval:     cfg.Cluster.SweepInterval,
			DaemonExpiryAfter: cfg.Cluster.DaemonExpiryAfter,
			ForwardTimeout:    cfg.Cluster.ForwardTimeout,
		}
		opts.InternalMux = http.NewServeMux()
	}

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	// Use NewWithResolverOptionsHub so we can call hub.Close during shutdown
	// to drain daemon WebSocket connections before stopping the listeners.
	app, hub := observerweb.NewWithResolverOptionsHub(st, usHandler, resolver, opts)
	publicSrv := newPublicHTTPServer(cfg.ListenAddr, withHealth(app, func(ctx context.Context) error {
		return st.DB().PingContext(ctx)
	}))

	var internalSrv *http.Server
	if cfg.Cluster.Enabled && opts.InternalMux != nil {
		log.Printf("observer-server cluster mode enabled; internal listener on %s (advertise=%s)",
			cfg.Cluster.InternalListenAddr, cfg.Cluster.AdvertiseURL)
		internalSrv = newInternalHTTPServer(cfg.Cluster.InternalListenAddr, opts.InternalMux)
		go func() {
			if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("observer-server internal listener error: %v", err)
			}
		}()
	}

	go func() {
		if err := publicSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("observer-server public listener error: %v", err)
		}
	}()

	// Wait for termination signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("observer-server shutting down")

	drainTimeout := cfg.Cluster.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = 10 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout+5*time.Second)
	defer cancel()

	// Drain hub BEFORE stopping HTTP servers: closes daemon WebSocket connections
	// and removes shared-registry rows so peer pods see them as gone immediately.
	if hub != nil {
		if err := hub.Close(shutdownCtx); err != nil {
			log.Printf("observer-server hub close: %v", err)
		}
	}

	if err := publicSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("observer-server public server shutdown: %v", err)
	}
	if internalSrv != nil {
		if err := internalSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("observer-server internal server shutdown: %v", err)
		}
	}
}

func runMigrationsOnly(cfg *Config) error {
	st, err := openObserverStoreForMigration(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := userspace.MigrateForDriver(st.DB(), cfg.Store.Driver); err != nil {
		return fmt.Errorf("userspace migrate: %w", err)
	}
	// Apply commander DDL when the runtime would need it. Uses the same gate
	// as the startup path: commander enabled OR telemetry+cluster+postgres
	// (which selects the shared-PG telemetry limiter and requires the
	// commander_telemetry_buckets table to exist).
	if cfg.Store.Driver == "postgres" && needsCommanderDDL(cfg) {
		if err := authstore.MigratePostgres(st.DB()); err != nil {
			return fmt.Errorf("commanderhub authstore migrate: %w", err)
		}
	}
	return nil
}

// buildCommanderAuthStore picks the authstore.Store implementation for the
// configured driver. postgres → run MigratePostgres + NewPostgresStore so
// every observer-server pod can serve any commander request. sqlite / empty
// → fall back to in-memory: still single-pod, but explicit about it via a
// startup log line. Any other driver value is a config error.
//
// Caller MUST guard on commander being enabled (AgentserverURL != "")
// before invoking this — otherwise a non-commander Postgres deployment will
// pay the migration cost and couple to commander DDL versions for no reason.
func buildCommanderAuthStore(cfg *Config, db *sql.DB) (authstore.Store, error) {
	switch cfg.Store.Driver {
	case "postgres":
		if err := authstore.MigratePostgres(db); err != nil {
			return nil, fmt.Errorf("commanderhub authstore migrate: %w", err)
		}
		return authstore.NewPostgresStore(db), nil
	case "sqlite", "":
		log.Printf("commanderhub: using in-memory store (driver=%q is single-pod only)", cfg.Store.Driver)
		return authstore.NewInMemoryStore(), nil
	default:
		return nil, fmt.Errorf("commanderhub: unsupported store.driver %q", cfg.Store.Driver)
	}
}

func shouldMigrateUserspaceOnStartup(driver string) bool {
	return driver != "postgres" && driver != "pgx"
}

// needsCommanderDDL returns true when the commander_* tables (including
// commander_telemetry_buckets) must be present in the database. This is true
// when:
//   - Commander is enabled (AgentserverURL is set), OR
//   - The cluster telemetry PG limiter is selected (telemetry enabled AND cluster
//     enabled AND store driver is postgres). The SetPGTelemetryLimiter gate in
//     main() selects the shared-PG limiter exactly when these three conditions
//     are met; failing to migrate in that case leaves the table absent and
//     produces 503s on the first telemetry call.
func needsCommanderDDL(cfg *Config) bool {
	if strings.TrimSpace(cfg.Identity.Agentserver.URL) != "" {
		return true
	}
	if cfg.Telemetry.Enabled && cfg.Cluster.Enabled && cfg.Store.Driver == "postgres" {
		return true
	}
	return false
}

// runDrainLocal is the implementation of the --drain-local subcommand used by
// the Kubernetes preStop hook. It POSTs to the local internal drain endpoint
// so that in-flight daemon WebSocket connections are gracefully closed before
// the pod terminates. The loopback bypass on the internal listener (see C5)
// means no auth header is required.
//
// Exit behaviour (called via log.Fatalf in main):
//   - Returns non-nil on config-read errors (→ exit 1).
//   - Returns nil (success) on HTTP 200 from the drain endpoint.
//   - Returns nil on connection-refused — the server may already have stopped;
//     this is not treated as an error so Kubernetes does not mark the preStop
//     as failed (which would cause an immediate SIGKILL rather than the
//     configured terminationGracePeriodSeconds).
func runDrainLocal(cfg *Config, portOverride int) error {
	// Determine the internal port to contact.
	internalAddr := cfg.Cluster.InternalListenAddr
	if portOverride > 0 {
		internalAddr = fmt.Sprintf(":%d", portOverride)
	}
	if internalAddr == "" {
		// Cluster mode is disabled or the config is incomplete; nothing to drain.
		log.Printf("drain-local: cluster.internal_listen_addr not set; skipping drain")
		return nil
	}

	// Extract port from the listen addr (e.g. ":8091" → "8091").
	_, port, err := net.SplitHostPort(internalAddr)
	if err != nil {
		return fmt.Errorf("cannot parse cluster.internal_listen_addr %q: %w", internalAddr, err)
	}
	drainURL := fmt.Sprintf("http://127.0.0.1:%s/api/commander/_internal/drain", port)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, drainURL, nil)
	if err != nil {
		return fmt.Errorf("building drain request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// connection-refused means the server already stopped — not an error.
		if isConnectionRefused(err) {
			log.Printf("drain-local: server already stopped (connection refused); exiting cleanly")
			return nil
		}
		return fmt.Errorf("drain POST to %s: %w", drainURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("drain endpoint returned %d (expected 200)", resp.StatusCode)
	}
	log.Printf("drain-local: drain complete (HTTP 200 from %s)", drainURL)
	return nil
}

// isConnectionRefused reports whether err (from http.Client.Do) is a
// connection-refused error, which means the server has already exited.
func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "connection refused")
}

func runRetentionCleanup(cfg *Config) (int64, error) {
	return runRetentionCleanupAt(cfg, time.Now().UTC())
}

func runRetentionCleanupAt(cfg *Config, now time.Time) (int64, error) {
	if cfg.Telemetry.RetentionDays <= 0 {
		return 0, fmt.Errorf("telemetry.retention_days must be positive")
	}
	st, err := openObserverStore(cfg)
	if err != nil {
		return 0, err
	}
	defer st.Close()

	cutoff := now.UTC().Add(-time.Duration(cfg.Telemetry.RetentionDays) * 24 * time.Hour)
	var result sql.Result
	switch cfg.Store.Driver {
	case "sqlite":
		result, err = st.DB().Exec(`DELETE FROM events WHERE ts < ?`, cutoff.Format(time.RFC3339Nano))
	case "postgres":
		result, err = st.DB().Exec(`DELETE FROM events WHERE ts < $1`, cutoff)
	default:
		return 0, fmt.Errorf("unsupported store driver %q", cfg.Store.Driver)
	}
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func openUserspaceStore(db *sql.DB, cfg *Config) (*userspace.Store, error) {
	return userspace.NewStoreForDriver(db, cfg.Store.Driver)
}

func openUserspaceBlobStore(db *sql.DB, cfg *Config, objects objectstore.Store) (userspace.BlobStorage, error) {
	if cfg.Store.Driver == "postgres" {
		if objects == nil {
			return nil, fmt.Errorf("userspace object store is required when store.driver is postgres")
		}
		return userspace.NewObjectBlobStore(db, objects)
	}
	blobRoot := userspaceBlobRoot(effectiveSQLitePath(cfg))
	return userspace.NewBlobStore(db, blobRoot)
}

func observerWebOptions(cfg *Config, objects objectstore.Store) observerweb.Options {
	return observerweb.Options{
		TelemetryRateLimit: observerweb.RateLimitConfig{
			PerMinute: cfg.Telemetry.RateLimit.PerMinute,
			Burst:     cfg.Telemetry.RateLimit.Burst,
		},
		MaxEventBodyBytes:   cfg.Telemetry.MaxBodyBytes,
		Objects:             objects,
		DisableObjectProxy:  !cfg.ObjectStore.Proxy.Enabled,
		MaxObjectProxyBytes: cfg.ObjectStore.Proxy.MaxBytes,
		RegisterDisabled:    !cfg.Identity.LegacyAPIKeys.Enabled,
		AgentserverURL:      strings.TrimSpace(cfg.Identity.Agentserver.URL),
	}
}

func configureTelemetryAPIKeys(st observerstore.ManagedStore, cfg *Config) error {
	if !cfg.Telemetry.Enabled {
		return st.ReplaceTelemetryAPIKeys(nil)
	}
	keys := make([]observerstore.TelemetryAPIKeySpec, 0, len(cfg.Telemetry.APIKeys))
	for _, k := range cfg.Telemetry.APIKeys {
		value := os.Getenv(k.KeyEnv)
		if value == "" {
			return fmt.Errorf("%s is required", k.KeyEnv)
		}
		keys = append(keys, observerstore.TelemetryAPIKeySpec{
			ID: k.ID, Key: value, WorkspaceID: k.WorkspaceID, Note: k.Note, Enabled: true,
		})
	}
	return st.ReplaceTelemetryAPIKeys(keys)
}

func openObserverStore(cfg *Config) (observerstore.ManagedStore, error) {
	return openObserverStoreWithOptions(cfg, true)
}

func openObserverStoreForMigration(cfg *Config) (observerstore.ManagedStore, error) {
	return openObserverStoreWithOptions(cfg, false)
}

func openObserverStoreWithOptions(cfg *Config, skipPostgresMigrate bool) (observerstore.ManagedStore, error) {
	switch cfg.Store.Driver {
	case "sqlite":
		return observerstore.Open(cfg.Store.SQLite.Path)
	case "postgres":
		pgCfg, err := postgresStoreConfig(cfg, skipPostgresMigrate)
		if err != nil {
			return nil, err
		}
		return pgobs.Open(pgCfg)
	default:
		return nil, fmt.Errorf("unsupported store driver %q", cfg.Store.Driver)
	}
}

func postgresStoreConfig(cfg *Config, skipMigrate bool) (pgobs.Config, error) {
	dsn := os.Getenv(cfg.Store.Postgres.DSNEnv)
	if dsn == "" {
		return pgobs.Config{}, fmt.Errorf("%s is required", cfg.Store.Postgres.DSNEnv)
	}
	lifetime, err := time.ParseDuration(cfg.Store.Postgres.ConnMaxLifetime)
	if err != nil {
		return pgobs.Config{}, fmt.Errorf("store.postgres.conn_max_lifetime: %w", err)
	}
	return pgobs.Config{
		DSN:             dsn,
		MaxOpenConns:    cfg.Store.Postgres.MaxOpenConns,
		MaxIdleConns:    cfg.Store.Postgres.MaxIdleConns,
		ConnMaxLifetime: lifetime,
		SkipMigrate:     skipMigrate,
	}, nil
}

func openObjectStore(cfg *Config) (objectstore.Store, error) {
	switch cfg.ObjectStore.Driver {
	case "", "filesystem":
		return nil, nil
	case "memory":
		return objectstore.NewMemory(), nil
	case "s3":
		accessKey := os.Getenv(cfg.ObjectStore.S3.AccessKeyEnv)
		if accessKey == "" {
			return nil, fmt.Errorf("%s is required", cfg.ObjectStore.S3.AccessKeyEnv)
		}
		secretKey := os.Getenv(cfg.ObjectStore.S3.SecretKeyEnv)
		if secretKey == "" {
			return nil, fmt.Errorf("%s is required", cfg.ObjectStore.S3.SecretKeyEnv)
		}
		return objectstore.NewS3(objectstore.S3Config{
			Endpoint:  cfg.ObjectStore.S3.Endpoint,
			Region:    cfg.ObjectStore.S3.Region,
			Bucket:    cfg.ObjectStore.S3.Bucket,
			UseSSL:    cfg.ObjectStore.S3.UseSSL,
			AccessKey: accessKey,
			SecretKey: secretKey,
		})
	default:
		return nil, fmt.Errorf("unsupported object store driver %q", cfg.ObjectStore.Driver)
	}
}

func loadConfig(path string, jobMode bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	cfg := Config{
		Identity: IdentityConfig{
			Agentserver: AgentserverIdentityConfig{
				FreshTTL:       durationConfig(180 * time.Second),
				StaleGrace:     durationConfig(15 * time.Minute),
				RequestTimeout: durationConfig(2 * time.Second),
				CacheCapacity:  65536,
				StartupProbe:   true,
			},
			LegacyAPIKeys: LegacyAPIKeysConfig{Enabled: true},
		},
	}
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	// v4: also merge a sibling nonsecret/observer.nonsecret.yaml when present.
	// This allows the cluster: block and identity cache overrides to be
	// delivered via ConfigMap rather than Secret, which is required for
	// existingSecret deployments where secret.create=false.
	nonsecretPath := filepath.Join(filepath.Dir(path), "nonsecret", "observer.nonsecret.yaml")
	if nonsecretData, err := os.ReadFile(nonsecretPath); err == nil {
		nsDec := yaml.NewDecoder(bytes.NewReader(nonsecretData))
		nsDec.KnownFields(true)
		if err := nsDec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("observer.nonsecret.yaml: %w", err)
		}
	}
	// Resolve env-indirection fields on ClusterConfig. Operators may set
	// advertise_url_env / secret_env / prev_secret_env in the ConfigMap so
	// that per-pod values (e.g. POD_IP-derived URL) are injected at runtime
	// without storing them in a Secret. Direct fields take precedence.
	if cfg.Cluster.AdvertiseURL == "" && cfg.Cluster.AdvertiseURLEnv != "" {
		cfg.Cluster.AdvertiseURL = os.Getenv(cfg.Cluster.AdvertiseURLEnv)
	}
	if cfg.Cluster.Secret == "" && cfg.Cluster.SecretEnv != "" {
		cfg.Cluster.Secret = os.Getenv(cfg.Cluster.SecretEnv)
	}
	if cfg.Cluster.PrevSecret == "" && cfg.Cluster.PrevSecretEnv != "" {
		cfg.Cluster.PrevSecret = os.Getenv(cfg.Cluster.PrevSecretEnv)
	}

	if cfg.Production && !yamlPathExists(data, "identity", "legacy_api_keys", "enabled") {
		cfg.Identity.LegacyAPIKeys.Enabled = false
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8090"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "observer.db"
	}
	if cfg.Store.Driver == "" {
		cfg.Store.Driver = "sqlite"
	}
	if cfg.Store.SQLite.Path == "" {
		cfg.Store.SQLite.Path = cfg.DBPath
	}
	if cfg.Store.Postgres.MaxOpenConns == 0 {
		cfg.Store.Postgres.MaxOpenConns = 20
	}
	if cfg.Store.Postgres.MaxIdleConns == 0 {
		cfg.Store.Postgres.MaxIdleConns = 10
	}
	if cfg.Store.Postgres.ConnMaxLifetime == "" {
		cfg.Store.Postgres.ConnMaxLifetime = "30m"
	}
	if cfg.ObjectStore.Driver == "" {
		cfg.ObjectStore.Driver = "filesystem"
	}
	if cfg.ObjectStore.Proxy.MaxBytes == 0 {
		cfg.ObjectStore.Proxy.MaxBytes = 8 << 20
	}
	if cfg.Telemetry.RateLimit.PerMinute == 0 {
		cfg.Telemetry.RateLimit.PerMinute = 60
	}
	if cfg.Telemetry.RateLimit.Burst == 0 {
		cfg.Telemetry.RateLimit.Burst = 120
	}
	if cfg.Telemetry.MaxBodyBytes == 0 {
		cfg.Telemetry.MaxBodyBytes = 256 << 10
	}
	if cfg.Telemetry.RetentionDays == 0 {
		cfg.Telemetry.RetentionDays = 30
	}
	// Cluster defaults — applied before validation so rules can use them.
	if cfg.Cluster.Enabled {
		if cfg.Cluster.HeartbeatInterval == 0 {
			cfg.Cluster.HeartbeatInterval = 30 * time.Second
		}
		if cfg.Cluster.HeartbeatJitter == 0 {
			cfg.Cluster.HeartbeatJitter = 5 * time.Second
		}
		if cfg.Cluster.SweepInterval == 0 {
			cfg.Cluster.SweepInterval = 60 * time.Second
		}
		if cfg.Cluster.DaemonExpiryAfter == 0 {
			cfg.Cluster.DaemonExpiryAfter = 90 * time.Second
		}
		if cfg.Cluster.ForwardTimeout == 0 {
			cfg.Cluster.ForwardTimeout = 5 * time.Second
		}
		if cfg.Cluster.DrainTimeout == 0 {
			cfg.Cluster.DrainTimeout = 10 * time.Second
		}
	}
	if err := validateConfig(&cfg, jobMode); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func yamlPathExists(data []byte, path ...string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	node := &doc
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for _, key := range path {
		if node.Kind != yaml.MappingNode {
			return false
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				next = node.Content[i+1]
				break
			}
		}
		if next == nil {
			return false
		}
		node = next
	}
	return true
}

func effectiveSQLitePath(cfg *Config) string {
	if cfg.Store.SQLite.Path != "" {
		return cfg.Store.SQLite.Path
	}
	return cfg.DBPath
}

func userspaceBlobRoot(sqlitePath string) string {
	if sqlitePath == ":memory:" || sqlitePath == "" {
		return filepath.Join(os.TempDir(), "userspace-blobs")
	}
	return filepath.Join(filepath.Dir(sqlitePath), "userspace-blobs")
}

func validateConfig(cfg *Config, skipCluster bool) error {
	if !cfg.Identity.LegacyAPIKeys.Enabled && !cfg.Identity.Agentserver.Enabled {
		return fmt.Errorf("at least one identity source must be enabled")
	}
	if cfg.Identity.Agentserver.Enabled && strings.TrimSpace(cfg.Identity.Agentserver.URL) == "" {
		return fmt.Errorf("identity.agentserver.url is required when enabled")
	}
	if cfg.Identity.LegacyAPIKeys.Enabled && len(cfg.APIKeys) == 0 {
		return fmt.Errorf("config must define at least one api_keys entry")
	}
	seenID := map[string]bool{}
	for i, k := range cfg.APIKeys {
		if k.ID == "" {
			return fmt.Errorf("api_keys[%d].id is required", i)
		}
		if k.Key == "" {
			return fmt.Errorf("api_keys[%s].key is required", k.ID)
		}
		if seenID[k.ID] {
			return fmt.Errorf("duplicate api_keys.id %s", k.ID)
		}
		seenID[k.ID] = true
	}

	switch cfg.Store.Driver {
	case "sqlite":
		if cfg.Production && !cfg.Store.AllowSQLiteInProduction {
			return fmt.Errorf("sqlite store is not allowed in production")
		}
	case "postgres":
		if cfg.Store.Postgres.DSNEnv == "" {
			return fmt.Errorf("store.postgres.dsn_env is required when store.driver is postgres")
		}
	default:
		return fmt.Errorf("store.driver must be sqlite or postgres")
	}

	switch cfg.ObjectStore.Driver {
	case "filesystem", "memory", "s3":
	case "":
		cfg.ObjectStore.Driver = "filesystem"
	default:
		return fmt.Errorf("object_store.driver must be filesystem, memory, or s3")
	}
	if cfg.ObjectStore.Driver == "s3" {
		if cfg.ObjectStore.S3.Endpoint == "" || cfg.ObjectStore.S3.Bucket == "" {
			return fmt.Errorf("object_store.s3.endpoint and bucket are required")
		}
		if cfg.ObjectStore.S3.AccessKeyEnv == "" || cfg.ObjectStore.S3.SecretKeyEnv == "" {
			return fmt.Errorf("object_store.s3 access_key_env and secret_key_env are required")
		}
	}
	if cfg.Telemetry.Enabled && len(cfg.Telemetry.APIKeys) == 0 {
		return fmt.Errorf("telemetry.api_keys must contain at least one key when telemetry.enabled is true")
	}
	if cfg.Telemetry.MaxBodyBytes > 1<<20 {
		return fmt.Errorf("telemetry.max_body_bytes must be <= 1048576")
	}

	// Validate revocation_channel if set. nil = auto is always valid.
	if rc := cfg.Identity.Agentserver.RevocationChannel; rc != nil {
		if *rc != "" && *rc != "postgres" {
			return fmt.Errorf("identity.agentserver.revocation_channel: unknown value %q (accepted: omitted/auto, empty-string/disabled, \"postgres\")", *rc)
		}
	}

	// Job modes (--migrate-only, --retention-cleanup) don't run forwarding,
	// drain, or heartbeat — they don't need the cluster runtime. Skip cluster
	// validation so the mounted nonsecret ConfigMap (which sets cluster.enabled:
	// true) doesn't cause a crashloop when the job container lacks the cluster
	// env vars that the Deployment carries.
	if !skipCluster {
		if err := validateClusterConfig(&cfg.Cluster, cfg.Store.Driver); err != nil {
			return err
		}
	}

	return nil
}

// validateClusterConfig validates the cluster configuration block.
// It is fail-closed: any inconsistency returns an error rather than silently
// disabling cluster mode. Must be called after defaults are applied.
func validateClusterConfig(c *ClusterConfig, storeDriver string) error {
	if !c.Enabled {
		// Reject partial cluster config when cluster is disabled to catch
		// misconfigurations where the user set cluster fields but forgot
		// to set cluster.enabled: true.
		if c.AdvertiseURL != "" {
			return fmt.Errorf("cluster.advertise_url is set but cluster.enabled is false")
		}
		if c.InternalListenAddr != "" {
			return fmt.Errorf("cluster.internal_listen_addr is set but cluster.enabled is false")
		}
		if c.Secret != "" {
			return fmt.Errorf("cluster.secret is set but cluster.enabled is false")
		}
		return nil
	}
	if c.AdvertiseURL == "" {
		return fmt.Errorf("cluster.advertise_url is required when cluster.enabled is true")
	}
	if c.InternalListenAddr == "" {
		return fmt.Errorf("cluster.internal_listen_addr is required when cluster.enabled is true")
	}
	if c.Secret == "" {
		return fmt.Errorf("cluster.secret is required when cluster.enabled is true")
	}

	// Validate AdvertiseURL is a well-formed http/https URL and not localhost.
	u, err := url.Parse(c.AdvertiseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("cluster.advertise_url must be an http or https URL")
	}
	advertiseHost := u.Hostname()
	if advertiseHost == "localhost" || strings.HasPrefix(advertiseHost, "127.") || advertiseHost == "::1" {
		return fmt.Errorf("cluster.advertise_url must not use a loopback address (got %q)", advertiseHost)
	}

	// internal_listen_addr must bind to a wildcard or loopback-only interface.
	// runDrainLocal always contacts 127.0.0.1:<port>; if the listener is bound
	// to a specific non-loopback IP (e.g. 10.x.x.x) the preStop drain silently
	// gets connection-refused and daemons are not drained.  Hostname binds like
	// "localhost" are also disallowed — require literal IP for predictability.
	//
	// Allowed hosts: "" (wildcard ":port"), "0.0.0.0", "127.0.0.1", "::", "::1".
	// Everything else, including symbolic hostnames and non-loopback IPs, is fatal.
	internalHost, _, _ := net.SplitHostPort(c.InternalListenAddr)
	switch internalHost {
	case "", "0.0.0.0", "127.0.0.1", "::", "::1":
		// accepted: wildcard or explicit loopback
	default:
		return fmt.Errorf(
			"cluster.internal_listen_addr host %q is not a wildcard or loopback address "+
				"(accepted: empty/:port, 0.0.0.0, 127.0.0.1, ::, ::1); "+
				"runDrainLocal contacts 127.0.0.1 so non-wildcard non-loopback binds break preStop drain",
			internalHost)
	}
	// Additionally reject loopback-only binds paired with a non-loopback advertise_url
	// because peers would advertise an address they cannot reach internally.
	if internalHost == "127.0.0.1" || internalHost == "::1" {
		return fmt.Errorf("cluster.internal_listen_addr binds to loopback (%q) but cluster.advertise_url (%q) is non-loopback — peers cannot reach this pod", c.InternalListenAddr, c.AdvertiseURL)
	}

	// Validate secret: must be hex-decodable and at least 32 bytes (256-bit).
	secretBytes, err := hex.DecodeString(c.Secret)
	if err != nil || len(secretBytes) < 32 {
		return fmt.Errorf("cluster.secret is empty or too short (must be at least 64 hex chars / 32 bytes)")
	}

	// Validate prev_secret if set.
	if c.PrevSecret != "" {
		prevBytes, err := hex.DecodeString(c.PrevSecret)
		if err != nil || len(prevBytes) < 32 {
			return fmt.Errorf("cluster.prev_secret is invalid (must be at least 64 hex chars / 32 bytes)")
		}
	}

	// Heartbeat must beat expiry.
	if c.HeartbeatInterval >= c.DaemonExpiryAfter {
		return fmt.Errorf("cluster.heartbeat_interval (%s) must be less than cluster.daemon_expiry_after (%s)",
			c.HeartbeatInterval, c.DaemonExpiryAfter)
	}

	// Cluster registry requires Postgres.
	if storeDriver != "postgres" {
		return fmt.Errorf("cluster.enabled requires store.driver=postgres (got %q)", storeDriver)
	}

	return nil
}

// advertiseHash returns a 4-hex-char prefix of the SHA-256 of the advertise
// URL. Used by hub.go::nextCmdID to make command IDs unique across pods.
func advertiseHash(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])[:4]
}

func buildIdentityResolver(cfg *Config, st observerstore.ManagedStore) (identity.Resolver, error) {
	var resolvers []identity.Resolver
	if cfg.Identity.LegacyAPIKeys.Enabled {
		resolvers = append(resolvers, static.New(st))
	}
	if cfg.Identity.Agentserver.Enabled {
		upstream := agentidentity.New(agentidentity.Config{
			BaseURL: strings.TrimSpace(cfg.Identity.Agentserver.URL),
			Timeout: cfg.Identity.Agentserver.RequestTimeout.Duration(),
		})
		var cacheOpts []identity.Option
		// Attach a cross-pod revocation channel so token invalidations propagate
		// to all pods without waiting for TTL expiry.
		// Pointer semantics (see AgentserverIdentityConfig.RevocationChannel):
		//   nil          → auto: enable PG revocation when store.driver=postgres
		//   ptr("")      → disabled: never enable, even with a Postgres store
		//   ptr("postgres") → always enable
		//   ptr(other)   → fatal (caught by validateConfig before reaching here)
		rc := cfg.Identity.Agentserver.RevocationChannel
		var usePGRevocation bool
		switch {
		case rc == nil:
			// auto: fall back to store-driver heuristic.
			usePGRevocation = cfg.Store.Driver == "postgres"
		case *rc == "":
			// explicit disabled: never use PG revocation.
			usePGRevocation = false
		case *rc == "postgres":
			// explicit opt-in.
			usePGRevocation = true
		default:
			// Should be caught by validateConfig; guard here defensively.
			return nil, fmt.Errorf("identity.agentserver.revocation_channel: unknown value %q (must be empty or \"postgres\")", *rc)
		}
		if usePGRevocation {
			cacheOpts = append(cacheOpts,
				identity.WithRevocationChannel(identity.NewPGRevocationChannel(st.DB())),
			)
		}
		freshTTL := cfg.Identity.Agentserver.FreshTTL.Duration()
		// In cluster mode, use 30s FreshTTL (per v19 spec §identity cache TTLs)
		// when the user has not set an explicit value. The default of 180s is
		// too long for multi-pod revocation propagation scenarios.
		if cfg.Cluster.Enabled && freshTTL == 180*time.Second {
			freshTTL = 30 * time.Second
		}
		resolvers = append(resolvers, identity.NewCache(upstream, identity.CacheConfig{
			FreshTTL:   freshTTL,
			StaleGrace: cfg.Identity.Agentserver.StaleGrace.Duration(),
			Capacity:   cfg.Identity.Agentserver.CacheCapacity,
		}, cacheOpts...))
	}
	if len(resolvers) == 0 {
		return nil, errors.New("at least one identity source must be enabled")
	}
	if len(resolvers) == 1 {
		return resolvers[0], nil
	}
	return identity.NewChain(resolvers...), nil
}

func probeAgentserverWhoami(ctx context.Context, cfg *Config) error {
	if !cfg.Identity.Agentserver.Enabled || !cfg.Identity.Agentserver.StartupProbe {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := probeAgentserverWhoamiOnce(ctx, cfg); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func probeAgentserverWhoamiOnce(ctx context.Context, cfg *Config) error {
	timeout := cfg.Identity.Agentserver.RequestTimeout.Duration()
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.Identity.Agentserver.URL), "/")
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+"/api/agent/whoami", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer observer-startup-probe-invalid-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("identity.agentserver startup probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("identity.agentserver startup probe: /api/agent/whoami returned 404")
	}
	return fmt.Errorf("identity.agentserver startup probe: expected 401, got %d", resp.StatusCode)
}

func withHealth(app http.Handler, ready func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready != nil {
			if err := ready(ctx); err != nil {
				log.Printf("observer-server readiness check failed: %v", err)
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	mux.Handle("/", app)
	return mux
}

// newPublicHTTPServer creates the public-facing HTTP server. WriteTimeout is 0
// because SSE and streaming turns can run for 10+ minutes; rely on per-request
// context deadlines and ReadHeaderTimeout to bound slow/stuck clients.
func newPublicHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // streaming SSE / forwarded turns have no fixed bound
		IdleTimeout:       120 * time.Second,
	}
}

// newInternalHTTPServer creates the internal (cluster-only) HTTP server.
// WriteTimeout is 0 because forwarded streaming turns have no fixed duration.
func newInternalHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // forwarded streaming turns have no fixed duration
		IdleTimeout:       120 * time.Second,
	}
}

// newHTTPServer is kept for compatibility with tests that use it directly.
// New code should prefer newPublicHTTPServer or newInternalHTTPServer.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return newPublicHTTPServer(addr, h)
}
