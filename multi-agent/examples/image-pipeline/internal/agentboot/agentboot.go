// Package agentboot is shared boot/registration glue for the image-pipeline
// example agents. Keeps each agent's main.go to ~25 lines.
package agentboot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"gopkg.in/yaml.v3"
)

// Config is the minimal yaml shape used by both image agents.
type Config struct {
	Server struct {
		URL  string `yaml:"url"`
		Name string `yaml:"name"`
	} `yaml:"server"`
	Credentials struct {
		SandboxID   string `yaml:"sandbox_id"`
		TunnelToken string `yaml:"tunnel_token"`
		ProxyToken  string `yaml:"proxy_token"`
		WorkspaceID string `yaml:"workspace_id"`
		ShortID     string `yaml:"short_id"`
	} `yaml:"credentials"`
	Discovery struct {
		DisplayName string   `yaml:"display_name"`
		Description string   `yaml:"description"`
		Skills      []string `yaml:"skills"`
	} `yaml:"discovery"`
	ListenAddr string `yaml:"listen_addr"` // for the in-process httpx.Server
}

// LoadConfig reads and parses the yaml file at path.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Server.URL == "" || c.Server.Name == "" {
		return nil, fmt.Errorf("config missing server.url or server.name")
	}
	if c.Discovery.DisplayName == "" {
		return nil, fmt.Errorf("config missing discovery.display_name")
	}
	return &c, nil
}

// SaveConfig writes c back to path (used after first-run device flow).
func SaveConfig(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// EnsureRegistered runs device-flow if credentials are missing, otherwise
// just calls SetRegistration. On first run it prints the verification URL
// to stderr and writes credentials back to cfgPath.
func EnsureRegistered(ctx context.Context, cli *agentsdk.Client, cfg *Config, cfgPath string) error {
	if cfg.Credentials.ProxyToken != "" {
		cli.SetRegistration(&agentsdk.Registration{
			SandboxID:   cfg.Credentials.SandboxID,
			TunnelToken: cfg.Credentials.TunnelToken,
			ProxyToken:  cfg.Credentials.ProxyToken,
			WorkspaceID: cfg.Credentials.WorkspaceID,
			ShortID:     cfg.Credentials.ShortID,
		})
		return nil
	}
	dc, err := agentsdk.RequestDeviceCode(ctx, cfg.Server.URL)
	if err != nil {
		return fmt.Errorf("device code: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Open this URL in a browser to register %q:\n  %s\n", cfg.Server.Name, dc.VerificationURIComplete)
	tok, err := agentsdk.PollForToken(ctx, cfg.Server.URL, dc)
	if err != nil {
		return fmt.Errorf("poll token: %w", err)
	}
	reg, err := cli.Register(ctx, tok.AccessToken)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	cfg.Credentials.SandboxID = reg.SandboxID
	cfg.Credentials.TunnelToken = reg.TunnelToken
	cfg.Credentials.ProxyToken = reg.ProxyToken
	cfg.Credentials.WorkspaceID = reg.WorkspaceID
	cfg.Credentials.ShortID = reg.ShortID
	if err := SaveConfig(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// PublishCard posts the discovery card. Mirrors multi-agent/internal/tunnel/tunnel.go:81
// (the SDK has no helper).
func PublishCard(ctx context.Context, cfg *Config) error {
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": cfg.Discovery.DisplayName,
		"description":  cfg.Discovery.Description,
		"agent_type":   "custom",
		"card": map[string]interface{}{
			"skills":        cfg.Discovery.Skills,
			"accepts_tasks": true,
			"has_web_ui":    false,
			"version":       "0.1.0",
		},
	})
	url := strings.TrimRight(cfg.Server.URL, "/") + "/api/agent/discovery/cards"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Credentials.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("publish card: status %d", resp.StatusCode)
	}
	return nil
}

// Run is the one-line entry point used by each agent's main: load config,
// register or set creds, publish card, Connect with the given handler.
// Blocks until ctx is cancelled or Connect returns an error.
func Run(ctx context.Context, cfgPath string, handler agentsdk.TaskHandler) error {
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return err
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: cfg.Server.URL, Name: cfg.Server.Name})
	if err := EnsureRegistered(ctx, cli, cfg, cfgPath); err != nil {
		return err
	}
	if err := PublishCard(ctx, cfg); err != nil {
		return fmt.Errorf("publish card: %w", err)
	}
	return cli.Connect(ctx, agentsdk.Handlers{
		Task: handler,
		OnConnect: func() {
			fmt.Fprintf(os.Stderr, "agentboot: %s connected\n", cfg.Server.Name)
		},
		OnDisconnect: func(err error) {
			fmt.Fprintf(os.Stderr, "agentboot: %s disconnected: %v\n", cfg.Server.Name, err)
		},
	}, agentsdk.WithTaskPollInterval(2*time.Second))
}
