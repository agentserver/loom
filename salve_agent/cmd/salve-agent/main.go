package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/yourorg/salve_agent/internal/config"
	"github.com/yourorg/salve_agent/internal/dispatch"
	"github.com/yourorg/salve_agent/internal/executor"
	"github.com/yourorg/salve_agent/internal/journal"
	"github.com/yourorg/salve_agent/internal/poller"
	"github.com/yourorg/salve_agent/internal/store"
	"github.com/yourorg/salve_agent/internal/tunnel"
	"github.com/yourorg/salve_agent/internal/webui"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	if err := run(cfgPath); err != nil {
		log.Fatalf("salve_agent: %v", err)
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

	mcpCfg := map[string]executor.MCPServerCfg{}
	for name, m := range cfg.MCPServers {
		mcpCfg[name] = executor.MCPServerCfg{
			Transport: m.Transport, Command: m.Command, Args: m.Args, Env: m.Env,
			URL: m.URL, Headers: m.Headers,
		}
	}
	mcpExec := executor.NewMCPExecutor(mcpCfg)
	defer mcpExec.Close()
	claudeExec := executor.NewClaudeExecutor(executor.ClaudeConfig{
		Bin: cfg.Claude.Bin, WorkDir: cfg.Claude.WorkDir, Args: cfg.Claude.Args,
	})

	routes := map[string]executor.Executor{
		"mcp": mcpExec,
		"":    claudeExec,
	}
	d := dispatch.New(routes, j, s)

	ui := webui.NewHandler(s, journalDir, cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tn := tunnel.New(cfg, cfgPath, ui)
	if err := tn.EnsureRegistered(ctx); err != nil {
		return err
	}
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
