package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

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
	Production  bool              `yaml:"production"`
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
	Enabled        bool           `yaml:"enabled"`
	URL            string         `yaml:"url"`
	FreshTTL       durationConfig `yaml:"fresh_ttl"`
	StaleGrace     durationConfig `yaml:"stale_grace"`
	RequestTimeout durationConfig `yaml:"request_timeout"`
	CacheCapacity  int            `yaml:"cache_capacity"`
	StartupProbe   bool           `yaml:"startup_probe"`
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
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
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

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	app := observerweb.NewWithResolverOptions(st, usHandler, resolver, observerWebOptions(cfg, objects))
	srv := newHTTPServer(cfg.ListenAddr, withHealth(app, func(ctx context.Context) error {
		return st.DB().PingContext(ctx)
	}))
	log.Fatal(srv.ListenAndServe())
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
	return nil
}

func shouldMigrateUserspaceOnStartup(driver string) bool {
	return driver != "postgres" && driver != "pgx"
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

func loadConfig(path string) (*Config, error) {
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
	if err := validateConfig(&cfg); err != nil {
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

func validateConfig(cfg *Config) error {
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

	return nil
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
		resolvers = append(resolvers, identity.NewCache(upstream, identity.CacheConfig{
			FreshTTL:   cfg.Identity.Agentserver.FreshTTL.Duration(),
			StaleGrace: cfg.Identity.Agentserver.StaleGrace.Duration(),
			Capacity:   cfg.Identity.Agentserver.CacheCapacity,
		}))
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

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
