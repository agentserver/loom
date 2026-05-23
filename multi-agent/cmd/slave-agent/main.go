package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/capabilitydoc"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/dispatch"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/journal"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerclient"
	"github.com/yourorg/multi-agent/internal/poller"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/internal/tunnel"
	"github.com/yourorg/multi-agent/internal/webui"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	if err := run(cfgPath); err != nil {
		log.Fatalf("slave_agent: %v", err)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	s, err := store.Open("data.db")
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Recover(); err != nil {
		return err
	}

	journalDir, _ := filepath.Abs("journal")
	j, err := journal.New(journal.Config{Dir: journalDir, ClaudeBin: cfg.Claude.Bin})
	if err != nil {
		return err
	}
	capDoc := capabilitydoc.NewStore(journalDir)
	workdir, _ := os.Getwd()
	dynamicMCPPath := filepath.Join(workdir, "dynamic_mcp.yaml")

	mcpCfg := map[string]executor.MCPServerCfg{}
	for name, m := range cfg.MCPServers {
		mcpCfg[name] = executor.MCPServerCfg{
			Transport: m.Transport, Command: m.Command, Args: m.Args, Env: m.Env,
			URL: m.URL, Headers: m.Headers,
		}
	}
	if df, err := loadDynamicMCP("dynamic_mcp.yaml"); err == nil {
		for name, entry := range df.Servers {
			mcpCfg[name] = executor.MCPServerCfg{
				Transport: entry.Transport, Command: entry.Command, Args: entry.Args,
			}
		}
	}
	mcpExec := executor.NewMCPExecutor(mcpCfg)
	defer mcpExec.Close()
	backend, err := agentbackend.New(agentbackend.Config{
		Kind:   agentbackend.Kind(cfg.Agent.Kind),
		Claude: agentbackend.ClaudeConfig{Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, ExtraArgs: cfg.Claude.Args},
		Codex:  agentbackend.CodexConfig{Bin: cfg.Codex.Bin, WorkDir: cfg.Codex.WorkDir, ExtraArgs: cfg.Codex.Args},
	}, nil)
	if err != nil {
		log.Fatalf("agentbackend: %v", err)
	}

	ui := webui.NewHandler(s, journalDir, cfg)
	webui.SetMCPBridge(ui, mcpExec)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tn := tunnel.New(cfg, cfgPath, ui)
	obs, errObs := observerclient.New(observerclient.Config{
		Enabled:        cfg.Observer.Enabled,
		URL:            cfg.Observer.URL,
		WorkspaceID:    cfg.Observer.WorkspaceID,
		AgentID:        cfg.Observer.AgentID,
		AgentRole:      observer.RoleSlave,
		APIKey:         cfg.Observer.APIKey,
		TokenStatePath: cfg.Observer.TokenStatePath,
	})
	if errObs != nil {
		log.Fatalf("observerclient: %v", errObs)
	}
	defer obs.Close()

	routes := map[string]executor.Executor{
		"mcp": mcpExec,
		"":    backendExecutor{backend},
	}
	if hasSkill(cfg.Discovery.Skills, "bash") {
		routes["bash"] = executor.NewBashExecutor(executor.BashConfig{WorkDir: cfg.Claude.WorkDir})
	}
	if hasSkill(cfg.Discovery.Skills, "file") {
		routes["file"] = executor.NewFileExecutor(executor.FileConfig{WorkDir: cfg.Claude.WorkDir})
	}
	enumerateMCPTools := func(ctx context.Context) []capability.MCPToolDescriptor {
		allDesc := []capability.MCPToolDescriptor{}
		for _, name := range mcpExec.Servers() {
			enumCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			descriptors, err := mcpExec.ListTools(enumCtx, name)
			cancel()
			if err == nil {
				allDesc = append(allDesc, descriptors...)
			}
		}
		return allDesc
	}
	refreshCapabilities := func(ctx context.Context, reason string) []capability.MCPToolDescriptor {
		allDesc := enumerateMCPTools(ctx)
		tn.SetMCPTools(allDesc)
		tn.SetTools(capability.FlatNames(allDesc))
		if err := capDoc.Refresh(ctx, capabilitydoc.Input{
			Config:         cfg,
			WorkDir:        workdir,
			DynamicMCPPath: dynamicMCPPath,
			MCPTools:       allDesc,
			Reason:         reason,
		}); err != nil {
			log.Printf("capability doc refresh: %v", err)
		}
		return allDesc
	}
	if hasSkill(cfg.Discovery.Skills, "permissions") || hasSkill(cfg.Discovery.Skills, "claude_permissions") {
		if hasSkill(cfg.Discovery.Skills, "claude_permissions") {
			log.Printf("WARN: 'claude_permissions' skill name is deprecated; rename to 'permissions' in discovery.skills")
		}
		permExec := newPermissionsExecutor(backend.Permissions(), func(ctx context.Context, reason string) error {
			refreshCapabilities(ctx, reason)
			return tn.PublishCard(ctx)
		})
		routes["permissions"] = permExec
		routes["claude_permissions"] = permExec // BC alias
	}
	if hasSkill(cfg.Discovery.Skills, "register_mcp") {
		routes["register_mcp"] = executor.NewRegisterMCPExecutor(executor.RegisterMCPConfig{
			WorkDir: workdir,
			MCPExec: mcpExec,
			Republish: func(ctx context.Context) error {
				refreshCapabilities(ctx, "register_mcp registered or updated MCP server")
				return tn.PublishCard(ctx)
			},
			Observer: obs,
		})
	}
	d := dispatch.New(routes, refreshingJournal{
		base: j,
		refresh: func(ctx context.Context, reason string) error {
			refreshCapabilities(ctx, reason)
			return nil
		},
	}, s, obs)

	if err := tn.EnsureRegistered(ctx); err != nil {
		return err
	}

	refreshCapabilities(ctx, "startup scan")

	if err := tn.PublishCard(ctx); err != nil {
		log.Printf("publish card: %v (continuing)", err)
	}

	p := poller.New(poller.Config{
		ServerURL: cfg.Server.URL, ProxyToken: cfg.Credentials.ProxyToken,
	}, d, s)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return tn.Run(gctx) })
	g.Go(func() error { return p.Run(gctx) })

	err = g.Wait()
	if err != nil && err != context.Canceled {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// backendExecutor adapts agentbackend.Backend to executor.Executor.
type backendExecutor struct {
	b agentbackend.Backend
}

func (be backendExecutor) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	return be.b.Run(ctx, t, sink)
}

func hasSkill(skills []string, want string) bool {
	for _, skill := range skills {
		if skill == want {
			return true
		}
	}
	return false
}

type refreshingJournal struct {
	base    *journal.Journal
	refresh func(context.Context, string) error
}

func (r refreshingJournal) Record(ctx context.Context, t executor.Task, res executor.Result) error {
	err := r.base.Record(ctx, t, res)
	if res.CapabilityChange != "" && r.refresh != nil {
		if refreshErr := r.refresh(ctx, "task capability change: "+t.ID); refreshErr != nil && err == nil {
			err = refreshErr
		}
	}
	return err
}

type dynamicMCPFile struct {
	Servers map[string]struct {
		Transport string   `yaml:"transport"`
		Command   string   `yaml:"command"`
		Args      []string `yaml:"args"`
	} `yaml:"servers"`
}

func loadDynamicMCP(path string) (*dynamicMCPFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var df dynamicMCPFile
	if err := yaml.Unmarshal(b, &df); err != nil {
		return nil, err
	}
	if df.Servers == nil {
		df.Servers = map[string]struct {
			Transport string   `yaml:"transport"`
			Command   string   `yaml:"command"`
			Args      []string `yaml:"args"`
		}{}
	}
	return &df, nil
}
