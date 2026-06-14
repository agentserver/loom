package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerclient"
	"github.com/yourorg/multi-agent/internal/orchestration"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/webui"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

const usage = `driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register   --config /path/to/driver.yaml
  driver-agent serve-mcp  --config /path/to/driver.yaml
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "register":
		runRegister(os.Args[2:])
	case "serve-mcp":
		runServe(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func runRegister(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to driver.yaml")
	fs.Parse(args) //nolint:errcheck
	if *cfgPath == "" {
		die("--config required")
	}
	cfg, err := driver.LoadConfig(*cfgPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if cfg.Credentials.ProxyToken != "" {
		fmt.Fprintln(os.Stderr, "already registered (short_id="+cfg.Credentials.ShortID+"); nothing to do")
		return
	}
	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: cfg.Server.URL, Name: cfg.Server.Name})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dc, err := agentsdk.RequestDeviceCode(ctx, cfg.Server.URL)
	if err != nil {
		die("device code: " + err.Error())
	}
	fmt.Fprintf(os.Stderr, "Open this URL to register %q:\n  %s\n", cfg.Server.Name, dc.VerificationURIComplete)
	tok, err := agentsdk.PollForToken(ctx, cfg.Server.URL, dc)
	if err != nil {
		die("poll token: " + err.Error())
	}
	reg, err := cli.Register(ctx, tok.AccessToken)
	if err != nil {
		die("register: " + err.Error())
	}
	cfg.Credentials.SandboxID = reg.SandboxID
	cfg.Credentials.TunnelToken = reg.TunnelToken
	cfg.Credentials.ProxyToken = reg.ProxyToken
	cfg.Credentials.WorkspaceID = reg.WorkspaceID
	cfg.Credentials.ShortID = reg.ShortID
	if err := driver.SaveConfig(*cfgPath, cfg); err != nil {
		die("save config: " + err.Error())
	}
	fmt.Fprintln(os.Stderr, "registered as", cfg.Credentials.ShortID)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve-mcp", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to driver.yaml")
	fs.Parse(args) //nolint:errcheck
	if *cfgPath == "" {
		die("--config required")
	}
	cfg, err := driver.LoadConfig(*cfgPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if cfg.Credentials.ProxyToken == "" {
		die("not registered; run `driver-agent register --config " + *cfgPath + "` first")
	}

	auditPath, err := resolveAuditPath(cfg)
	if err != nil {
		die("audit path: " + err.Error())
	}
	audit, err := driver.NewAuditLog(auditPath)
	if err != nil {
		die("audit log: " + err.Error())
	}
	defer audit.Close()
	taskJournalPath, err := resolveDriverLocalPath(cfg, "driver-tasks.jsonl")
	if err != nil {
		die("task journal path: " + err.Error())
	}
	taskJournal, err := driver.NewTaskJournal(taskJournalPath)
	if err != nil {
		die("task journal: " + err.Error())
	}
	defer taskJournal.Close()
	reg := driver.NewFileRegistry(cfg.DriverDefaults.MaxDirCacheEntries)

	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: cfg.Server.URL, Name: cfg.Server.Name})
	cli.SetRegistration(&agentsdk.Registration{
		SandboxID:   cfg.Credentials.SandboxID,
		TunnelToken: cfg.Credentials.TunnelToken,
		ProxyToken:  cfg.Credentials.ProxyToken,
		WorkspaceID: cfg.Credentials.WorkspaceID,
		ShortID:     cfg.Credentials.ShortID,
	})

	files := driver.NewFilesHandler(reg, audit)
	base := http.NewServeMux()
	base.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"agent":"driver"}`)) //nolint:errcheck
	})
	composed := webui.SetDriverFiles(base, files)

	if err := publishCard(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "driver: publish card warning:", err)
	}

	obs, errObs := observerclient.New(observerclient.Config{
		Enabled:               cfg.Observer.Enabled,
		TelemetryEnabled:      cfg.Observer.TelemetryEnabled,
		TelemetryAPIKey:       cfg.Observer.TelemetryAPIKey,
		URL:                   cfg.Observer.URL,
		WorkspaceID:           cfg.Observer.WorkspaceID,
		WorkspaceName:         cfg.Observer.WorkspaceName,
		AgentID:               cfg.Observer.AgentID,
		AgentRole:             observer.RoleDriver,
		APIKey:                cfg.Observer.APIKey,
		AgentserverProxyToken: cfg.Credentials.ProxyToken,
		TokenStatePath:        cfg.Observer.TokenStatePath,
		ForceRegister:         cfg.Observer.ForceRegister,
	})
	if errObs != nil {
		log.Fatalf("observerclient: %v", errObs)
	}
	defer obs.Close()

	sdkClient := driver.NewAgentSDKClient(cli, cfg.Server.URL, cfg.Credentials.ProxyToken)
	tools := driver.NewTools(reg, audit, sdkClient, cfg, obs)
	tools.SetTaskJournal(taskJournal)
	agentCfg := agentbackend.Config{Kind: agentbackend.Kind(cfg.Agent.Kind)}
	switch cfg.Agent.Kind {
	case "claude":
		agentCfg.Bin = cfg.Claude.Bin
		agentCfg.WorkDir = cfg.Claude.WorkDir
		agentCfg.ExtraArgs = cfg.Claude.Args
	case "codex":
		agentCfg.Bin = cfg.Codex.Bin
		agentCfg.WorkDir = cfg.Codex.WorkDir
		agentCfg.ExtraArgs = cfg.Codex.Args
	}
	// TODO(issue-15 Task 3): replace switch with direct cfg.Agent.{Bin,WorkDir,ExtraArgs}
	backend, err := agentbackend.New(agentCfg, nil)
	if err != nil {
		log.Fatalf("agentbackend: %v", err)
	}
	p := planner.New(cfg.Planner, backend.LLM())
	tools.SetContractRunner(orchestration.NewDriverRunner(p, sdkClient, orchestration.RunnerConfig{
		MaxConcurrency:  cfg.Fanout.MaxConcurrency,
		ChildTimeoutSec: cfg.Fanout.SubTaskDefaults.TimeoutSec,
		SelfID:          cfg.Credentials.SandboxID,
	}))
	mcpSrv := driver.NewMCPServer(tools.All())

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if cfg.DriverDefaults.ArtifactTransport == driver.ArtifactTransportObserverLazy {
		go driver.NewObserverRelay(cfg, obs).ServePendingLoop(ctx, reg, audit, 2*time.Second)
	}

	connDone := make(chan error, 1)
	go func() {
		connDone <- cli.Connect(ctx, agentsdk.Handlers{
			HTTP:         composed,
			OnConnect:    func() { fmt.Fprintln(os.Stderr, "driver: tunnel connected") },
			OnDisconnect: func(err error) { fmt.Fprintf(os.Stderr, "driver: tunnel disconnected: %v\n", err) },
		})
	}()

	if err := mcpSrv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mcp serve:", err)
	}
	cancel()
	<-connDone
}

func resolveAuditPath(cfg *driver.Config) (string, error) {
	return resolveDriverLocalPath(cfg, "audit.log")
}

func resolveDriverLocalPath(cfg *driver.Config, name string) (string, error) {
	dir := cfg.DriverDefaults.AuditLogDir
	if dir == "" {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(u.HomeDir, ".cache", "multi-agent", cfg.Credentials.ShortID)
	}
	return filepath.Join(dir, name), nil
}

func publishCard(cfg *driver.Config) error {
	body, _ := json.Marshal(map[string]interface{}{
		"display_name": cfg.Discovery.DisplayName,
		"description":  cfg.Discovery.Description,
		"agent_type":   "driver",
		"card": map[string]interface{}{
			"skills":        cfg.Discovery.Skills,
			"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
			"short_id":      cfg.Credentials.ShortID,
			"accepts_tasks": false,
			"has_web_ui":    false,
			"version":       "0.1.0",
		},
	})
	url := strings.TrimRight(cfg.Server.URL, "/") + "/api/agent/discovery/cards"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
		return fmt.Errorf("publish card status %d", resp.StatusCode)
	}
	return nil
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver-agent:", msg)
	os.Exit(1)
}
