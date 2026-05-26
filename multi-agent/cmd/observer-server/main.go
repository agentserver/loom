package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/observerweb"
	"github.com/yourorg/multi-agent/internal/userspace"
)

type Config struct {
	ListenAddr string         `yaml:"listen_addr"`
	DBPath     string         `yaml:"db_path"`
	APIKeys    []APIKeyConfig `yaml:"api_keys"`
}

type APIKeyConfig struct {
	ID   string `yaml:"id"`
	Key  string `yaml:"key"`
	Note string `yaml:"note,omitempty"`
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
	if err := st.ReplaceAPIKeys(specs); err != nil {
		log.Fatal(err)
	}
	log.Printf("observer-server loaded %d api_keys", len(specs))

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
		Resolver: func(r *http.Request) (string, string, bool) {
			return observerweb.AgentFromRequest(st, r)
		},
	}

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, observerweb.New(st, usHandler)))
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
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
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
	return nil
}
