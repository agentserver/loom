// Package driver implements the cmd/driver-agent runtime.
package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	agentconfig "github.com/yourorg/multi-agent/internal/config"
	"gopkg.in/yaml.v3"
)

// Config is the driver-agent yaml shape. Server / Credentials / Discovery
// mirror the agentboot config used by image-pipeline agents (intentional
// duplication — internal packages must not import from examples/).
type Config struct {
	Server         ServerConfig        `yaml:"server"`
	Credentials    Credentials         `yaml:"credentials"`
	Agent          AgentConfig         `yaml:"agent"`
	Claude         ClaudeConfig        `yaml:"claude"`
	Codex          CodexConfig         `yaml:"codex"`
	Discovery      Discovery           `yaml:"discovery"`
	ListenAddr     string              `yaml:"listen_addr"`
	Planner        agentconfig.Planner `yaml:"planner"`
	Fanout         agentconfig.Fanout  `yaml:"fanout"`
	DriverDefaults DriverDefaults      `yaml:"driver_defaults"`
	Observer       Observer            `yaml:"observer,omitempty"`
}

type AgentConfig struct {
	Kind string `yaml:"kind"` // "claude" | "codex"; default claude
}

type ClaudeConfig struct {
	Bin     string   `yaml:"bin"`
	WorkDir string   `yaml:"workdir"`
	Args    []string `yaml:"extra_args"`
}

type CodexConfig struct {
	Bin     string   `yaml:"bin"`
	WorkDir string   `yaml:"workdir"`
	Args    []string `yaml:"extra_args"`
}

type ServerConfig struct {
	URL  string `yaml:"url"`
	Name string `yaml:"name"`
}

type Credentials struct {
	SandboxID   string `yaml:"sandbox_id"`
	TunnelToken string `yaml:"tunnel_token"`
	ProxyToken  string `yaml:"proxy_token"`
	WorkspaceID string `yaml:"workspace_id"`
	ShortID     string `yaml:"short_id"`
}

type Discovery struct {
	DisplayName string   `yaml:"display_name"`
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
}

type DriverDefaults struct {
	TargetDisplayName  string `yaml:"target_display_name"`
	TaskTimeoutSec     int    `yaml:"task_timeout_sec"`
	AuditLogDir        string `yaml:"audit_log_dir"`
	DisableUIDCheck    bool   `yaml:"disable_uid_check"`
	MaxDirCacheEntries int    `yaml:"max_dir_cache_entries"`
	ArtifactTransport  string `yaml:"artifact_transport"`
}

const (
	ArtifactTransportPeerProxy     = "peer_proxy"
	ArtifactTransportObserverLazy  = "observer_lazy"
	ArtifactTransportObserverEager = "observer_eager"
)

type Observer struct {
	Enabled        bool   `yaml:"enabled"`
	URL            string `yaml:"url"`
	WorkspaceID    string `yaml:"workspace_id"`
	WorkspaceName  string `yaml:"workspace_name,omitempty"`
	AgentID        string `yaml:"agent_id"`
	APIKey         string `yaml:"api_key"`
	TokenStatePath string `yaml:"token_state_path"`
}

// LoadConfig reads + validates the yaml at path and applies DriverDefaults defaults.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.URL == "" {
		return nil, fmt.Errorf("config missing server.url")
	}
	if c.Server.Name == "" {
		return nil, fmt.Errorf("config missing server.name")
	}
	if c.Discovery.DisplayName == "" {
		return nil, fmt.Errorf("config missing discovery.display_name")
	}
	if c.DriverDefaults.TaskTimeoutSec == 0 {
		c.DriverDefaults.TaskTimeoutSec = 600
	}
	if c.DriverDefaults.MaxDirCacheEntries == 0 {
		c.DriverDefaults.MaxDirCacheEntries = 50000
	}
	if c.DriverDefaults.ArtifactTransport == "" {
		c.DriverDefaults.ArtifactTransport = ArtifactTransportPeerProxy
	}
	if c.Agent.Kind == "" {
		c.Agent.Kind = "claude"
	}
	if c.Claude.Bin == "" {
		c.Claude.Bin = "claude"
	}
	if c.Codex.Bin == "" {
		c.Codex.Bin = "codex"
	}
	if c.Codex.WorkDir == "" {
		c.Codex.WorkDir = c.Claude.WorkDir
	}
	if c.Planner.Bin == "" {
		switch c.Agent.Kind {
		case "codex":
			c.Planner.Bin = c.Codex.Bin
		default:
			c.Planner.Bin = c.Claude.Bin
		}
	}
	if c.Planner.TimeoutSec == 0 {
		c.Planner.TimeoutSec = 60
	}
	if c.Fanout.MaxConcurrency == 0 {
		c.Fanout.MaxConcurrency = 4
	}
	if c.Fanout.SubTaskDefaults.TimeoutSec == 0 {
		c.Fanout.SubTaskDefaults.TimeoutSec = c.DriverDefaults.TaskTimeoutSec
	}
	if c.Observer.URL != "" {
		if c.Observer.AgentID == "" {
			c.Observer.AgentID = c.Discovery.DisplayName
		}
	}
	if c.Observer.Enabled {
		if c.Observer.URL == "" {
			return nil, fmt.Errorf("observer.url is required when observer.enabled is true")
		}
		if c.Observer.WorkspaceID == "" {
			return nil, fmt.Errorf("observer.workspace_id is required when observer.enabled is true")
		}
		if c.Observer.AgentID == "" {
			return nil, fmt.Errorf("observer.agent_id is required when observer.enabled is true")
		}
		if c.Observer.APIKey == "" {
			return nil, fmt.Errorf("observer.api_key is required when observer.enabled is true")
		}
		if c.Observer.TokenStatePath == "" {
			return nil, fmt.Errorf("observer.token_state_path is required when observer.enabled is true")
		}
		if !filepath.IsAbs(c.Observer.TokenStatePath) {
			return nil, fmt.Errorf("observer.token_state_path must be an absolute path (got %q)", c.Observer.TokenStatePath)
		}
		parent := filepath.Dir(c.Observer.TokenStatePath)
		info, err := os.Stat(parent)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("observer.token_state_path parent directory %q must exist", parent)
		}
		probe := filepath.Join(parent, ".observer-write-probe")
		f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("observer.token_state_path parent directory %q must be writable: %w", parent, err)
		}
		_ = f.Close()
		_ = os.Remove(probe)
	}
	if c.DriverDefaults.ArtifactTransport == ArtifactTransportObserverLazy && !c.Observer.Enabled {
		return nil, fmt.Errorf("observer must be enabled when driver_defaults.artifact_transport is observer_lazy")
	}
	return &c, nil
}

// SaveConfig writes c back to path with 0600 perms (it contains tokens).
func SaveConfig(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
