package main

import (
	"bytes"
	"context"
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
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/observerweb"
	"github.com/yourorg/multi-agent/internal/userspace"
)

type Config struct {
	ListenAddr string         `yaml:"listen_addr"`
	DBPath     string         `yaml:"db_path"`
	APIKeys    []APIKeyConfig `yaml:"api_keys"`
	Identity   IdentityConfig `yaml:"identity"`
}

type APIKeyConfig struct {
	ID   string `yaml:"id"`
	Key  string `yaml:"key"`
	Note string `yaml:"note,omitempty"`
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
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	st, err := observerstore.Open(cfg.DBPath)
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

	if err := userspace.Migrate(st.DB()); err != nil {
		log.Fatalf("userspace migrate: %v", err)
	}
	blobRoot := filepath.Join(filepath.Dir(cfg.DBPath), "userspace-blobs")
	if cfg.DBPath == ":memory:" || cfg.DBPath == "" {
		blobRoot = filepath.Join(os.TempDir(), "userspace-blobs")
	}
	blobs, err := userspace.NewBlobStore(st.DB(), blobRoot)
	if err != nil {
		log.Fatalf("userspace blob store: %v", err)
	}
	usHandler := &userspace.Handler{
		Store: userspace.NewStore(st.DB()),
		Blobs: blobs,
		Resolver: func(r *http.Request) (userspace.Identity, bool) {
			ident, ok := observerweb.IdentityFromRequest(resolver, r)
			return userspace.Identity{UserID: ident.UserID, WorkspaceID: ident.WorkspaceID, AgentID: ident.AgentID}, ok
		},
	}

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, observerweb.NewWithResolver(st, usHandler, resolver)))
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
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8090"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "observer.db"
	}
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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
	return nil
}

func buildIdentityResolver(cfg *Config, st *observerstore.Store) (identity.Resolver, error) {
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
