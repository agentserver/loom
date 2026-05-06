package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/orchestrator"
	"github.com/yourorg/multi-agent/internal/planner"
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
		log.Fatalf("master_agent: %v", err)
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

	ui := webui.NewHandler(s, "", cfg) // master does not maintain a journal

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tn := tunnel.New(cfg, cfgPath, ui)
	if err := tn.EnsureRegistered(ctx); err != nil {
		return err
	}
	if err := tn.PublishCard(ctx); err != nil {
		log.Printf("publish card: %v (continuing)", err)
	}

	sdk := tn.SDKClient()
	if sdk == nil {
		return fmt.Errorf("tunnel.SDKClient returned nil after EnsureRegistered")
	}
	p := planner.New(cfg.Planner)
	orch := orchestrator.New(s, p, sdk, cfg.Fanout, cfg.Credentials.SandboxID)

	pollCfg := poller.Config{
		ServerURL:  cfg.Server.URL,
		ProxyToken: cfg.Credentials.ProxyToken,
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return tn.Run(gctx) })
	g.Go(func() error { return poller.New(pollCfg, orch, s).Run(gctx) })

	err = g.Wait()
	if err != nil && err != context.Canceled {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}
