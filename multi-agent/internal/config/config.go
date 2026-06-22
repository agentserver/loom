package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      Server               `yaml:"server"`
	Credentials Credentials          `yaml:"credentials"`
	Agent       Agent                `yaml:"agent"`
	MCPServers  map[string]MCPServer `yaml:"mcp_servers"`
	Discovery   Discovery            `yaml:"discovery"`
	Planner     Planner              `yaml:"planner"`
	Fanout      Fanout               `yaml:"fanout"`
	Resources   *Resources           `yaml:"resources,omitempty"`
	Observer    Observer             `yaml:"observer,omitempty"`
	Daemon      DaemonConfig         `yaml:"daemon,omitempty"`
	Humanloop   HumanloopConfig      `yaml:"humanloop"`
}

// DaemonConfig configures optional long-lived `slave-agent serve-daemon` mode.
// Fields and yaml tags mirror internal/driver/config.go DaemonConfig.
type DaemonConfig struct {
	// AutoStart, when explicitly set to a non-nil pointer, opts the slave's
	// long-running run() loop into starting a Commander daemon alongside the
	// poller (using the same backend instance, no extra process). When nil
	// (the default), run() auto-starts the daemon iff both
	// credentials.proxy_token and observer.url are configured — the same
	// preconditions serve-daemon enforces. Set to false to opt out (e.g. a
	// machine where you want only the worker poller, no Commander reporting).
	AutoStart *bool `yaml:"auto_start,omitempty"`
	// Listen is the HTTP bind for the daemon's debug API. Defaults to
	// 127.0.0.1:0 (loopback, random port) when AutoStart selects on.
	Listen string `yaml:"listen,omitempty"`
	// WSPath is appended to Observer.URL when dialing the observer hub.
	WSPath string `yaml:"ws_path,omitempty"`
	// HeartbeatIntervalSec is the daemon-to-observer heartbeat cadence.
	HeartbeatIntervalSec int `yaml:"heartbeat_interval_sec,omitempty"`
	// InitialBackoffMs and MaxBackoffMs bound WS reconnect backoff.
	InitialBackoffMs int `yaml:"initial_backoff_ms,omitempty"`
	MaxBackoffMs     int `yaml:"max_backoff_ms,omitempty"`
	// WorkerMax caps hot session workers kept by commander. Zero uses the handler default.
	WorkerMax int `yaml:"worker_max,omitempty"`
	// WorkerIdleTimeoutSec controls idle hot-worker lifetime. Zero uses the handler default.
	WorkerIdleTimeoutSec int `yaml:"worker_idle_timeout_sec,omitempty"`
}

type HumanloopConfig struct {
	ShutdownGraceSec    int `yaml:"shutdown_grace_sec"`
	MaxQuestionsPerTask int `yaml:"max_questions_per_task"`
}

// Agent is the single per-backend descriptor consumed by both the
// agent runtime (agentbackend.New) and slave-local paths (executor
// jail roots, planner bin default). Previously this was split across
// claude:/codex: top-level YAML blocks plus a tiny Agent{Kind} stub;
// collapsed in issue #15.
type Agent struct {
	Kind       string   `yaml:"kind"`
	Bin        string   `yaml:"bin"`
	WorkDir    string   `yaml:"workdir"`
	ExtraArgs  []string `yaml:"extra_args"`
	WorkerMode string   `yaml:"worker_mode"`
	CodexHome  string   `yaml:"codex_home,omitempty"`
	LoomHome   string   `yaml:"loom_home,omitempty"`
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
	// ForceRegister, when true, instructs observerclient to set "force":true
	// on register. Use to recover from a stale duplicate-takeover 409 after
	// a within-5-min restart. Defaults to false so accidental takeovers of
	// a still-live sibling agent remain blocked. See §1.3 #11.
	ForceRegister bool `yaml:"force_register,omitempty"`
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
	// Legacy-key peek: produce a friendly migration error before the
	// unknown-fields decoder buries it as a generic "unknown field"
	// message. Probe without DisallowUnknownFields so we can recognise
	// the old shape regardless of agent.kind validity.
	// Using yaml.Node (not `any`) so we detect bare `claude:` /
	// `claude: null` — both unmarshal to a `nil` interface but to a
	// node with Kind != 0 (absent keys leave Kind=0).
	type legacyProbe struct {
		Claude yaml.Node `yaml:"claude"`
		Codex  yaml.Node `yaml:"codex"`
	}
	var probe legacyProbe
	_ = yaml.Unmarshal(data, &probe)
	var legacy []string
	if probe.Claude.Kind != 0 {
		legacy = append(legacy, "claude")
	}
	if probe.Codex.Kind != 0 {
		legacy = append(legacy, "codex")
	}
	if len(legacy) > 0 {
		return nil, fmt.Errorf("config %s: legacy top-level key(s) %v are no longer supported; consolidate into agent: { kind, bin, workdir (agent.workdir), extra_args }. See docs/migration/2026-06-agent-config.md", path, legacy)
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

	// Required-field validation (no implicit defaults — see issue #15).
	if c.Agent.Kind == "" {
		return nil, fmt.Errorf("config %s: agent.kind is required (one of %v)", path, agentbackend.RegisteredKinds())
	}
	if c.Agent.WorkDir == "" {
		return nil, fmt.Errorf("config %s: agent.workdir is required", path)
	}
	if !isRegisteredKind(c.Agent.Kind) {
		return nil, fmt.Errorf("config %s: unknown agent.kind %q; registered: %v", path, c.Agent.Kind, agentbackend.RegisteredKinds())
	}
	// Defaults from the registered factory (Bin only — WorkDir is
	// required, ExtraArgs default-empty).
	if c.Agent.Bin == "" {
		c.Agent.Bin = c.Agent.Kind
	}

	if c.Planner.Bin == "" {
		c.Planner.Bin = c.Agent.Bin
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
	if c.Daemon.Listen == "" {
		c.Daemon.Listen = "127.0.0.1:0"
	}
	if c.Daemon.WSPath == "" {
		c.Daemon.WSPath = "/api/daemon-link"
	}
	if c.Daemon.HeartbeatIntervalSec == 0 {
		c.Daemon.HeartbeatIntervalSec = 30
	}
	if c.Daemon.InitialBackoffMs == 0 {
		c.Daemon.InitialBackoffMs = 1000
	}
	if c.Daemon.MaxBackoffMs == 0 {
		c.Daemon.MaxBackoffMs = 30000
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

// isRegisteredKind asks the agentbackend registry whether kind is
// claimed by some imported backend package.
func isRegisteredKind(kind string) bool {
	for _, k := range agentbackend.RegisteredKinds() {
		if k == kind {
			return true
		}
	}
	return false
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
