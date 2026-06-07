package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      Server               `yaml:"server"`
	Credentials Credentials          `yaml:"credentials"`
	Agent       Agent                `yaml:"agent"`
	Claude      Claude               `yaml:"claude"`
	Codex       Codex                `yaml:"codex"`
	MCPServers  map[string]MCPServer `yaml:"mcp_servers"`
	Discovery   Discovery            `yaml:"discovery"`
	Planner     Planner              `yaml:"planner"`
	Fanout      Fanout               `yaml:"fanout"`
	Resources   *Resources           `yaml:"resources,omitempty"`
	Observer    Observer             `yaml:"observer,omitempty"`
	Humanloop   HumanloopConfig      `yaml:"humanloop"`
}

type HumanloopConfig struct {
	ShutdownGraceSec    int `yaml:"shutdown_grace_sec"`
	MaxQuestionsPerTask int `yaml:"max_questions_per_task"`
}

type Agent struct {
	Kind string `yaml:"kind"` // "claude" | "codex"; default claude
}

type Codex struct {
	Bin     string   `yaml:"bin"`
	WorkDir string   `yaml:"workdir"`
	Args    []string `yaml:"extra_args"`
}

type Server struct {
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

type Claude struct {
	Bin     string   `yaml:"bin"`
	WorkDir string   `yaml:"workdir"`
	Args    []string `yaml:"extra_args"`
}

type MCPServer struct {
	Transport string            `yaml:"transport"` // "stdio" | "http"
	Command   string            `yaml:"command,omitempty"`
	Args      []string          `yaml:"args,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	URL       string            `yaml:"url,omitempty"`
	Headers   map[string]string `yaml:"headers,omitempty"`
}

type Discovery struct {
	DisplayName string   `yaml:"display_name"`
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
}

type Planner struct {
	Bin        string   `yaml:"bin"`
	TimeoutSec int      `yaml:"timeout_sec"`
	ExtraArgs  []string `yaml:"extra_args"`
}

type Fanout struct {
	MaxConcurrency  int               `yaml:"max_concurrency"`
	DefaultPolicy   string            `yaml:"default_policy"`
	PolicyBySkill   map[string]string `yaml:"policy_by_skill"`
	SubTaskDefaults SubTaskDefaults   `yaml:"subtask_defaults"`
}

type Observer struct {
	Enabled          bool   `yaml:"enabled"`
	TelemetryEnabled bool   `yaml:"telemetry_enabled,omitempty"`
	TelemetryAPIKey  string `yaml:"telemetry_api_key,omitempty"`
	URL              string `yaml:"url"`
	WorkspaceID      string `yaml:"workspace_id"`
	WorkspaceName    string `yaml:"workspace_name,omitempty"`
	AgentID          string `yaml:"agent_id"`
	APIKey           string `yaml:"api_key"`
	TokenStatePath   string `yaml:"token_state_path"`
}

type SubTaskDefaults struct {
	TimeoutSec   int     `yaml:"timeout_sec"`
	MaxBudgetUSD float64 `yaml:"max_budget_usd"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Claude.Bin == "" {
		c.Claude.Bin = "claude"
	}
	if c.Agent.Kind == "" {
		c.Agent.Kind = "claude"
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
	if c.Fanout.DefaultPolicy == "" {
		c.Fanout.DefaultPolicy = "best_effort"
	}
	if c.Fanout.SubTaskDefaults.TimeoutSec == 0 {
		c.Fanout.SubTaskDefaults.TimeoutSec = 600
	}
	if c.Humanloop.ShutdownGraceSec == 0 {
		c.Humanloop.ShutdownGraceSec = 10
	}
	if c.Humanloop.MaxQuestionsPerTask == 0 {
		c.Humanloop.MaxQuestionsPerTask = 5
	}
	observerLegacyConfigured := c.Observer.APIKey != "" || c.Observer.TokenStatePath != ""
	observerProxyReady := c.Credentials.ProxyToken != ""
	if c.Observer.URL != "" {
		if c.Observer.WorkspaceID == "" && c.Credentials.WorkspaceID != "" {
			c.Observer.WorkspaceID = c.Credentials.WorkspaceID
		}
		if c.Observer.AgentID == "" {
			if c.Credentials.ShortID != "" {
				c.Observer.AgentID = c.Credentials.ShortID
			} else if !c.Observer.Enabled || observerLegacyConfigured {
				c.Observer.AgentID = c.Discovery.DisplayName
			}
		}
	}
	if c.Observer.TelemetryEnabled && c.Observer.TelemetryAPIKey == "" {
		return nil, fmt.Errorf("observer.telemetry_api_key is required when observer.telemetry_enabled is true")
	}
	if c.Observer.Enabled {
		if c.Observer.URL == "" {
			return nil, fmt.Errorf("observer.url is required when observer.enabled is true")
		}
		if observerProxyReady || observerLegacyConfigured {
			if c.Observer.WorkspaceID == "" {
				return nil, fmt.Errorf("observer.workspace_id is required when observer.enabled is true")
			}
			if c.Observer.AgentID == "" {
				return nil, fmt.Errorf("observer.agent_id is required when observer.enabled is true")
			}
		}
		if observerLegacyConfigured {
			if c.Observer.APIKey == "" {
				return nil, fmt.Errorf("observer.api_key is required when observer legacy registration is configured")
			}
			if c.Observer.TokenStatePath == "" {
				return nil, fmt.Errorf("observer.token_state_path is required when observer legacy registration is configured")
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
	}
	return &c, nil
}

func (c *Config) Validate() error {
	if c.Server.URL == "" {
		return fmt.Errorf("server.url is required")
	}
	if c.Fanout.MaxConcurrency < 0 {
		return fmt.Errorf("fanout.max_concurrency must be >= 0 (got %d)", c.Fanout.MaxConcurrency)
	}
	if c.Fanout.DefaultPolicy != "" && c.Fanout.DefaultPolicy != "best_effort" && c.Fanout.DefaultPolicy != "all_or_nothing" {
		return fmt.Errorf("fanout.default_policy must be best_effort or all_or_nothing")
	}
	return nil
}

func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Resources struct {
	CPU      *CPUSpec `yaml:"cpu,omitempty"       json:"cpu,omitempty"`
	GPU      *GPUSpec `yaml:"gpu,omitempty"       json:"gpu,omitempty"`
	MemoryGB int      `yaml:"memory_gb,omitempty" json:"memory_gb,omitempty"`
	Devices  []string `yaml:"devices,omitempty"   json:"devices,omitempty"`
	Tags     []string `yaml:"tags,omitempty"      json:"tags,omitempty"`
}

type CPUSpec struct {
	Cores int    `yaml:"cores"          json:"cores"`
	Arch  string `yaml:"arch,omitempty" json:"arch,omitempty"`
}

type GPUSpec struct {
	Count  int    `yaml:"count"             json:"count"`
	Model  string `yaml:"model,omitempty"   json:"model,omitempty"`
	VRAMGB int    `yaml:"vram_gb,omitempty" json:"vram_gb,omitempty"`
}
