package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
)

// Deps lets tests stub the user-facing browser-open step. SDK functions
// themselves (RequestDeviceCode/PollForToken/Register) are package-level and
// hit the real URL — tests point them at an httptest.Server.
type Deps struct {
	Open func(url string) error
}

type Tunnel struct {
	cfg               *config.Config
	cfgPath           string
	http              http.Handler
	deps              Deps
	sdk               *agentsdk.Client
	tools             []string
	mcpTools          []capability.MCPToolDescriptor
	platform          commandiface.Platform
	commandInterfaces []commandiface.CommandInterface

	// ready is closed exactly once, the first time the yamux tunnel reports
	// OnConnect to agentserver. Reconnects do NOT re-close it. Callers
	// (notably the slave-agent commander-daemon goroutine) wait on Ready()
	// before dialing observer; observer validates every WS handshake via
	// agentserver's /api/agent/whoami, and whoami returns 401/403 until the
	// tunnel has put the sandbox into the running state. Without this gate,
	// the daemon races the tunnel and reliably gets bounced on first launch.
	readyOnce sync.Once
	ready     chan struct{}
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
	return &Tunnel{
		cfg:      cfg,
		cfgPath:  cfgPath,
		http:     h,
		deps:     deps,
		tools:    []string{},
		mcpTools: []capability.MCPToolDescriptor{},
		ready:    make(chan struct{}),
	}
}

// Ready returns a channel that is closed the first time the tunnel
// successfully connects to agentserver (the OnConnect callback fires).
// Callers gate observer-bound work on this signal so the agentserver-side
// sandbox state has transitioned to running before observer's whoami probe
// runs — without the gate the daemon races the tunnel and gets bounced
// with 401 on cold start. The channel is never re-opened: reconnects do
// NOT re-close it. Always returns the same channel; safe for many
// concurrent select waiters.
func (t *Tunnel) Ready() <-chan struct{} {
	return t.ready
}

// SetTools sets the flattened MCP tool name list to include in the next
// PublishCard call. Safe to call before or after EnsureRegistered.
func (t *Tunnel) SetTools(tools []string) {
	t.tools = append([]string{}, tools...)
}

// SetMCPTools sets the structured MCP tool descriptors to include in the next
// PublishCard call. Safe to call before or after EnsureRegistered.
func (t *Tunnel) SetMCPTools(tools []capability.MCPToolDescriptor) {
	t.mcpTools = make([]capability.MCPToolDescriptor, len(tools))
	for i, tool := range tools {
		t.mcpTools[i] = tool
		t.mcpTools[i].InputSchema = append([]byte(nil), tool.InputSchema...)
	}
}

// SetPlatform sets the OS/architecture to include in the next PublishCard call.
// When unset, PublishCard falls back to the current runtime platform.
func (t *Tunnel) SetPlatform(platform commandiface.Platform) {
	t.platform = platform
}

// SetCommandInterfaces sets the command interfaces to include in the next
// PublishCard call. Safe to call before or after EnsureRegistered.
func (t *Tunnel) SetCommandInterfaces(interfaces []commandiface.CommandInterface) {
	t.commandInterfaces = append([]commandiface.CommandInterface{}, interfaces...)
}

func (t *Tunnel) EnsureRegistered(ctx context.Context) error {
	if t.cfg.Credentials.SandboxID != "" && t.cfg.Credentials.TunnelToken != "" {
		if t.sdk == nil {
			t.sdk = agentsdk.NewClient(agentsdk.Config{ServerURL: t.cfg.Server.URL, Name: t.cfg.Server.Name})
			t.sdk.SetRegistration(&agentsdk.Registration{
				SandboxID:   t.cfg.Credentials.SandboxID,
				TunnelToken: t.cfg.Credentials.TunnelToken,
				ProxyToken:  t.cfg.Credentials.ProxyToken,
				ShortID:     t.cfg.Credentials.ShortID,
			})
		}
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
	t.cfg.Credentials.WorkspaceID = reg.WorkspaceID
	t.cfg.Credentials.ShortID = reg.ShortID
	t.sdk = cli
	return t.cfg.Save(t.cfgPath)
}

// PublishCard posts the discovery card via raw HTTP (SDK has no helper).
// Best-effort: caller may log+ignore failure.
func (t *Tunnel) PublishCard(ctx context.Context) error {
	platform := t.platform
	if platform.OS == "" {
		platform.OS = runtime.GOOS
	}
	if platform.Arch == "" {
		platform.Arch = runtime.GOARCH
	}
	cardBody := map[string]interface{}{
		"skills":              t.cfg.Discovery.Skills,
		"tools":               t.tools,
		"mcp_tools":           t.mcpTools,
		"platform":            platform,
		"short_id":            t.cfg.Credentials.ShortID,
		"accepts_tasks":       true,
		"has_web_ui":          true,
		"state_path":          "/state",
		"capability_doc_path": "/capabilities",
		"version":             "0.1.0",
	}
	if t.cfg.Resources != nil {
		cardBody["resources"] = t.cfg.Resources
	}
	if len(t.commandInterfaces) > 0 {
		cardBody["command_interfaces"] = t.commandInterfaces
	}
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": t.cfg.Discovery.DisplayName,
		"description":  t.cfg.Discovery.Description,
		"agent_type":   "custom",
		"card":         cardBody,
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
		HTTP: t.http,
		// OnConnect fires every time the yamux tunnel attaches (cold start
		// AND every reconnect). Close ready exactly once so a single waiter
		// pattern stays correct across reconnects.
		OnConnect:    func() { t.readyOnce.Do(func() { close(t.ready) }) },
		OnDisconnect: func(error) {},
		// Task: nil — our internal/poller handles task polling.
	})
}

// SDKClient returns the underlying agentsdk.Client after EnsureRegistered has run.
// Callers (e.g., master_agent's orchestrator) use it to call DelegateTask, WaitForTask, DiscoverAgents.
// Returns nil if EnsureRegistered has not been called.
func (t *Tunnel) SDKClient() *agentsdk.Client {
	return t.sdk
}
