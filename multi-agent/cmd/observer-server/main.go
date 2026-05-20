package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/observerweb"
)

type Config struct {
	ListenAddr string            `yaml:"listen_addr"`
	DBPath     string            `yaml:"db_path"`
	Workspaces []WorkspaceConfig `yaml:"workspaces"`
}

type WorkspaceConfig struct {
	ID      string         `yaml:"id"`
	Name    string         `yaml:"name"`
	APIKeys []APIKeyConfig `yaml:"api_keys"`
}

type APIKeyConfig struct {
	ID  string `yaml:"id"`
	Key string `yaml:"key"`
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

	for _, workspace := range cfg.Workspaces {
		if err := st.UpsertWorkspace(observerstore.Workspace{ID: workspace.ID, Name: workspace.Name}); err != nil {
			log.Fatal(err)
		}
		specs := make([]observerstore.APIKeySpec, 0, len(workspace.APIKeys))
		for _, k := range workspace.APIKeys {
			specs = append(specs, observerstore.APIKeySpec{ID: k.ID, Key: k.Key})
		}
		if err := st.ReplaceAPIKeysForWorkspace(workspace.ID, specs); err != nil {
			log.Fatal(err)
		}
	}

	log.Printf("observer-server listening on %s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, observerweb.New(st)))
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
	if len(cfg.Workspaces) == 0 {
		return fmt.Errorf("at least one workspace is required")
	}

	workspaces := make(map[string]struct{})
	for wi, workspace := range cfg.Workspaces {
		workspaceID := workspace.ID
		if workspaceID == "" {
			return fmt.Errorf("workspace[%d].id is required", wi)
		}
		if hasOuterWhitespace(workspaceID) {
			return fmt.Errorf("workspace[%d].id must not contain leading or trailing whitespace", wi)
		}
		if workspace.Name == "" {
			return fmt.Errorf("workspace[%d].name is required", wi)
		}
		if hasOuterWhitespace(workspace.Name) {
			return fmt.Errorf("workspace[%d].name must not contain leading or trailing whitespace", wi)
		}
		if _, ok := workspaces[workspaceID]; ok {
			return fmt.Errorf("duplicate workspace id %s", workspaceID)
		}
		workspaces[workspaceID] = struct{}{}

		if len(workspace.APIKeys) == 0 {
			return fmt.Errorf("workspace[%s] must define at least one api_keys entry", workspaceID)
		}

		keyIDs := make(map[string]struct{})
		for ki, k := range workspace.APIKeys {
			if k.ID == "" {
				return fmt.Errorf("workspace[%s].api_keys[%d].id is required", workspaceID, ki)
			}
			if hasOuterWhitespace(k.ID) {
				return fmt.Errorf("workspace[%s].api_keys[%d].id must not contain leading or trailing whitespace", workspaceID, ki)
			}
			if _, ok := keyIDs[k.ID]; ok {
				return fmt.Errorf("duplicate api_keys.id %s in workspace %s", k.ID, workspaceID)
			}
			keyIDs[k.ID] = struct{}{}
			if k.Key == "" {
				return fmt.Errorf("workspace[%s].api_keys[%s].key is required", workspaceID, k.ID)
			}
			if hasOuterWhitespace(k.Key) {
				return fmt.Errorf("workspace[%s].api_keys[%s].key must not contain leading or trailing whitespace", workspaceID, k.ID)
			}
		}
	}
	return nil
}

func hasOuterWhitespace(s string) bool {
	return s != strings.TrimSpace(s)
}
