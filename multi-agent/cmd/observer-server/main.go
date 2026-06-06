package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

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

func main() {
	cfgPath := flag.String("config", "observer.yaml", "path to observer config")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
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
	if err := st.ReplaceAPIKeys(specs); err != nil {
		log.Fatal(err)
	}
	log.Printf("observer-server loaded %d api_keys", len(specs))

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

	if err := userspace.Migrate(st.DB()); err != nil {
		log.Fatalf("userspace migrate: %v", err)
	}
	blobRoot := userspaceBlobRoot(effectiveSQLitePath(cfg))
	blobs, err := userspace.NewBlobStore(st.DB(), blobRoot)
	if err != nil {
		log.Fatalf("userspace blob store: %v", err)
	}
	usHandler := &userspace.Handler{
		Store: userspace.NewStore(st.DB()),
		Blobs: blobs,
		Resolver: func(r *http.Request) (string, string, bool) {
			return observerweb.AgentFromRequest(st, r)
		},
	}

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	app := observerweb.NewWithOptions(st, usHandler, observerweb.Options{
		TelemetryRateLimit: observerweb.RateLimitConfig{
			PerMinute: cfg.Telemetry.RateLimit.PerMinute,
			Burst:     cfg.Telemetry.RateLimit.Burst,
		},
		MaxEventBodyBytes: cfg.Telemetry.MaxBodyBytes,
		Objects:           objects,
	})
	srv := newHTTPServer(cfg.ListenAddr, withHealth(app, func(ctx context.Context) error {
		return st.DB().PingContext(ctx)
	}))
	log.Fatal(srv.ListenAndServe())
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
	switch cfg.Store.Driver {
	case "sqlite":
		return observerstore.Open(cfg.Store.SQLite.Path)
	case "postgres":
		dsn := os.Getenv(cfg.Store.Postgres.DSNEnv)
		if dsn == "" {
			return nil, fmt.Errorf("%s is required", cfg.Store.Postgres.DSNEnv)
		}
		lifetime, err := time.ParseDuration(cfg.Store.Postgres.ConnMaxLifetime)
		if err != nil {
			return nil, fmt.Errorf("store.postgres.conn_max_lifetime: %w", err)
		}
		return pgobs.Open(pgobs.Config{
			DSN:             dsn,
			MaxOpenConns:    cfg.Store.Postgres.MaxOpenConns,
			MaxIdleConns:    cfg.Store.Postgres.MaxIdleConns,
			ConnMaxLifetime: lifetime,
		})
	default:
		return nil, fmt.Errorf("unsupported store driver %q", cfg.Store.Driver)
	}
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

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
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
	if len(cfg.APIKeys) == 0 {
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

	return nil
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
