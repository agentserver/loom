package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/observerweb"
)

type Config struct {
	ListenAddr string            `yaml:"listen_addr"`
	DBPath     string            `yaml:"db_path"`
	Workspaces []WorkspaceConfig `yaml:"workspaces"`
}

type WorkspaceConfig struct {
	ID     string        `yaml:"id"`
	Name   string        `yaml:"name"`
	Agents []AgentConfig `yaml:"agents"`
}

type AgentConfig struct {
	ID          string `yaml:"id"`
	Role        string `yaml:"role"`
	DisplayName string `yaml:"display_name"`
	Token       string `yaml:"token"`
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
		for _, agent := range workspace.Agents {
			err := st.UpsertAgent(observerstore.Agent{
				WorkspaceID: workspace.ID,
				ID:          agent.ID,
				Role:        agent.Role,
				DisplayName: agent.DisplayName,
			}, agent.Token)
			if err != nil {
				log.Fatal(err)
			}
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

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
	tokens := make(map[string]struct{})
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
		if len(workspace.Agents) == 0 {
			return fmt.Errorf("workspace[%s] must define at least one agent", workspaceID)
		}

		agents := make(map[string]struct{})
		for ai, agent := range workspace.Agents {
			agentID := agent.ID
			role := agent.Role
			displayName := agent.DisplayName
			token := agent.Token
			if agentID == "" {
				return fmt.Errorf("workspace[%s].agents[%d].id is required", workspaceID, ai)
			}
			if hasOuterWhitespace(agentID) {
				return fmt.Errorf("workspace[%s].agents[%d].id must not contain leading or trailing whitespace", workspaceID, ai)
			}
			if _, ok := agents[agentID]; ok {
				return fmt.Errorf("duplicate agent id %s in workspace %s", agentID, workspaceID)
			}
			agents[agentID] = struct{}{}
			if role == "" {
				return fmt.Errorf("workspace[%s].agents[%s].role is required", workspaceID, agentID)
			}
			if hasOuterWhitespace(role) {
				return fmt.Errorf("workspace[%s].agents[%s].role must not contain leading or trailing whitespace", workspaceID, agentID)
			}
			if !validRole(role) {
				return fmt.Errorf("workspace[%s].agents[%s].role must be one of driver, master, slave", workspaceID, agentID)
			}
			if displayName == "" {
				return fmt.Errorf("workspace[%s].agents[%s].display_name is required", workspaceID, agentID)
			}
			if hasOuterWhitespace(displayName) {
				return fmt.Errorf("workspace[%s].agents[%s].display_name must not contain leading or trailing whitespace", workspaceID, agentID)
			}
			if token == "" {
				return fmt.Errorf("workspace[%s].agents[%s].token is required", workspaceID, agentID)
			}
			if hasOuterWhitespace(token) {
				return fmt.Errorf("workspace[%s].agents[%s].token must not contain leading or trailing whitespace", workspaceID, agentID)
			}
			if _, ok := tokens[token]; ok {
				return fmt.Errorf("duplicate token %s", token)
			}
			tokens[token] = struct{}{}
		}
	}
	return nil
}

func validRole(role string) bool {
	switch role {
	case observer.RoleDriver, observer.RoleMaster, observer.RoleSlave:
		return true
	default:
		return false
	}
}

func hasOuterWhitespace(s string) bool {
	return s != strings.TrimSpace(s)
}
