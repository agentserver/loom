package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      Server               `yaml:"server"`
	Credentials Credentials          `yaml:"credentials"`
	Claude      Claude               `yaml:"claude"`
	MCPServers  map[string]MCPServer `yaml:"mcp_servers"`
	Discovery   Discovery            `yaml:"discovery"`
}

type Server struct {
	URL  string `yaml:"url"`
	Name string `yaml:"name"`
}

type Credentials struct {
	SandboxID   string `yaml:"sandbox_id"`
	TunnelToken string `yaml:"tunnel_token"`
	ProxyToken  string `yaml:"proxy_token"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Claude.Bin == "" {
		c.Claude.Bin = "claude"
	}
	return &c, nil
}

func (c *Config) Validate() error {
	if c.Server.URL == "" {
		return fmt.Errorf("server.url is required")
	}
	return nil
}
