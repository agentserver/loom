package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/salve_agent/internal/config"
)

// Deps lets tests stub the user-facing browser-open step. SDK functions
// themselves (RequestDeviceCode/PollForToken/Register) are package-level and
// hit the real URL — tests point them at an httptest.Server.
type Deps struct {
	Open func(url string) error
}

type Tunnel struct {
	cfg     *config.Config
	cfgPath string
	http    http.Handler
	deps    Deps
	sdk     *agentsdk.Client
}

func New(cfg *config.Config, cfgPath string, h http.Handler) *Tunnel {
	return NewWithDeps(cfg, cfgPath, h, Deps{
		Open: func(u string) error {
			fmt.Printf("\nOpen this URL to authenticate:\n\n    %s\n\n", u)
			return nil
		},
	})
}

func NewWithDeps(cfg *config.Config, cfgPath string, h http.Handler, deps Deps) *Tunnel {
	return &Tunnel{cfg: cfg, cfgPath: cfgPath, http: h, deps: deps}
}

func (t *Tunnel) EnsureRegistered(ctx context.Context) error {
	if t.cfg.Credentials.SandboxID != "" && t.cfg.Credentials.TunnelToken != "" {
		return nil
	}
	dc, err := agentsdk.RequestDeviceCode(ctx, t.cfg.Server.URL)
	if err != nil {
		return fmt.Errorf("device code: %w", err)
	}
	if err := t.deps.Open(dc.VerificationURIComplete); err != nil {
		return err
	}
	tok, err := agentsdk.PollForToken(ctx, t.cfg.Server.URL, dc)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: t.cfg.Server.URL, Name: t.cfg.Server.Name})
	reg, err := cli.Register(ctx, tok.AccessToken)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	t.cfg.Credentials.SandboxID = reg.SandboxID
	t.cfg.Credentials.TunnelToken = reg.TunnelToken
	t.cfg.Credentials.ProxyToken = reg.ProxyToken
	t.cfg.Credentials.ShortID = reg.ShortID
	t.sdk = cli
	return t.cfg.Save(t.cfgPath)
}

// PublishCard posts the discovery card via raw HTTP (SDK has no helper).
// Best-effort: caller may log+ignore failure.
func (t *Tunnel) PublishCard(ctx context.Context) error {
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": t.cfg.Discovery.DisplayName,
		"description":  t.cfg.Discovery.Description,
		"agent_type":   "custom",
		"card": map[string]interface{}{
			"skills":        t.cfg.Discovery.Skills,
			"accepts_tasks": true,
			"has_web_ui":    true,
			"version":       "0.1.0",
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.cfg.Server.URL+"/api/agent/discovery/cards", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.cfg.Credentials.ProxyToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("publish card: status %d", resp.StatusCode)
	}
	return nil
}

func (t *Tunnel) Run(ctx context.Context) error {
	if t.sdk == nil {
		t.sdk = agentsdk.NewClient(agentsdk.Config{ServerURL: t.cfg.Server.URL, Name: t.cfg.Server.Name})
		t.sdk.SetRegistration(&agentsdk.Registration{
			SandboxID:   t.cfg.Credentials.SandboxID,
			TunnelToken: t.cfg.Credentials.TunnelToken,
			ProxyToken:  t.cfg.Credentials.ProxyToken,
			ShortID:     t.cfg.Credentials.ShortID,
		})
	}
	return t.sdk.Connect(ctx, agentsdk.Handlers{
		HTTP:         t.http,
		OnConnect:    func() {},
		OnDisconnect: func(error) {},
		// Task: nil — our internal/poller handles task polling.
	})
}
