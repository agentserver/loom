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
	claudeExec := executor.NewClaudeExecutor(executor.ClaudeConfig{
		Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, Args: cfg.Claude.Args,
	})

	ui := webui.NewHandler(s, journalDir, cfg)
	webui.SetMCPBridge(ui, mcpExec)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tn := tunnel.New(cfg, cfgPath, ui)
	obs := observerclient.New(observerclient.Config{
		Enabled:     cfg.Observer.Enabled,
		URL:         cfg.Observer.URL,
		WorkspaceID: cfg.Observer.WorkspaceID,
		AgentID:     cfg.Observer.AgentID,
		AgentRole:   observer.RoleSlave,
		Token:       cfg.Observer.Token,
	})
	defer obs.Close()

	routes := map[string]executor.Executor{
		"mcp": mcpExec,
		"":    claudeExec,
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
	hasBuildMCP := false
	for _, skill := range cfg.Discovery.Skills {
		if skill == "build_mcp" {
			hasBuildMCP = true
			break
		}
	}
	if hasBuildMCP {
		buildExec := executor.NewBuildMCPExecutor(executor.BuildMCPConfig{
			WorkDir:   workdir,
			ClaudeBin: cfg.Claude.Bin,
			MCPExec:   mcpExec,
			Observer:  obs,
			Republish: func(ctx context.Context) error {
				refreshCapabilities(ctx, "build_mcp generated or updated MCP server")
				return tn.PublishCard(ctx)
			},
		})
		routes["build_mcp"] = buildExec
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
