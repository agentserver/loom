// Package driver implements the cmd/driver-agent runtime.
package driver

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	agentconfig "github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	"gopkg.in/yaml.v3"
)

// Config is the driver-agent yaml shape. Server / Credentials / Discovery
// mirror the agentboot config used by image-pipeline agents (intentional
// duplication — internal packages must not import from examples/).
type Config struct {
	Server         ServerConfig        `yaml:"server"`
	Credentials    Credentials         `yaml:"credentials"`
	Agent          AgentConfig         `yaml:"agent"`
	Discovery      Discovery           `yaml:"discovery"`
	ListenAddr     string              `yaml:"listen_addr"`
	Planner        agentconfig.Planner `yaml:"planner"`
	Fanout         agentconfig.Fanout  `yaml:"fanout"`
	DriverDefaults DriverDefaults      `yaml:"driver_defaults"`
	Observer       Observer            `yaml:"observer,omitempty"`
}

// AgentConfig is the single per-backend descriptor consumed by both
// the agent runtime (agentbackend.New) and the driver-only paths
// (jail roots, planner bin default). Previously this was split
// across claude:/codex: top-level YAML blocks plus a tiny
// AgentConfig{Kind} stub; collapsed in issue #15.
type AgentConfig struct {
	Kind      string   `yaml:"kind"`
	Bin       string   `yaml:"bin"`
	WorkDir   string   `yaml:"workdir"`
	ExtraArgs []string `yaml:"extra_args"`
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
	// WorkDir is the driver's local working directory. write_slave_file
	// source_path inputs must resolve inside this directory (or
	// SourcePathReadRoots) so an LLM-controlled source_path cannot read
	// arbitrary driver files (e.g. /etc/shadow). See §1.4 #17.
	WorkDir string `yaml:"workdir,omitempty"`
	// SourcePathReadRoots adds extra directories (beyond WorkDir) from
	// which write_slave_file's source_path may read driver-local files.
	// Operator opt-in. See §1.4 #17.
	SourcePathReadRoots []string `yaml:"source_path_read_roots,omitempty"`
}

const (
	ArtifactTransportPeerProxy     = "peer_proxy"
	ArtifactTransportObserverLazy  = "observer_lazy"
	ArtifactTransportObserverEager = "observer_eager"
)

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
	// a still-live sibling driver remain blocked. See §1.3 #11.
	ForceRegister bool `yaml:"force_register,omitempty"`
}

// LoadConfig reads + validates the yaml at path and applies DriverDefaults defaults.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
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
	_ = yaml.Unmarshal(b, &probe)
	var legacy []string
	if probe.Claude.Kind != 0 {
		legacy = append(legacy, "claude")
	}
	if probe.Codex.Kind != 0 {
		legacy = append(legacy, "codex")
	}
	if len(legacy) > 0 {
		// Note: include agent.workdir in message so operators have an
		// actionable pointer for the most common migration footgun.
		return nil, fmt.Errorf("config %s: legacy top-level key(s) %v are no longer supported; consolidate into agent: { kind, bin, workdir (agent.workdir), extra_args }. See docs/migration/2026-06-agent-config.md", path, legacy)
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
		c.Agent.Bin = c.Agent.Kind // factory will recognise "" too, but explicit is clearer
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
	if c.Planner.Bin == "" {
		c.Planner.Bin = c.Agent.Bin
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
	// driver_defaults.workdir defaults to agent.workdir (jail root).
	if c.DriverDefaults.WorkDir == "" {
		c.DriverDefaults.WorkDir = c.Agent.WorkDir
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
