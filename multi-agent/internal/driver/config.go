// Package driver implements the cmd/driver-agent runtime.
package driver

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the driver-agent yaml shape. Server / Credentials / Discovery
// mirror the agentboot config used by image-pipeline agents (intentional
// duplication — internal packages must not import from examples/).
type Config struct {
	Server         ServerConfig   `yaml:"server"`
	Credentials    Credentials    `yaml:"credentials"`
	Discovery      Discovery      `yaml:"discovery"`
	ListenAddr     string         `yaml:"listen_addr"`
	DriverDefaults DriverDefaults `yaml:"driver_defaults"`
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
}

// LoadConfig reads + validates the yaml at path and applies DriverDefaults defaults.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
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
