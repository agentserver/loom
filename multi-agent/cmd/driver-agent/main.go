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
	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerclient"
	"github.com/yourorg/multi-agent/internal/orchestration"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/webui"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

const usage = `driver-agent — bridges Claude Code to the multi-agent workspace.

Usage:
  driver-agent register      --config /path/to/driver.yaml
  driver-agent serve-mcp     --config /path/to/driver.yaml
  driver-agent serve-daemon  --config /path/to/driver.yaml [--listen host:port]
  driver-agent humanloop-mcp ENDPOINT_JSON_OR_SOCKET_PATH MAX_QUESTIONS
`

// driverVersion is injected by release builds with:
//
//	go build -ldflags "-X main.driverVersion=vX.Y.Z"
var driverVersion = "v0.0.0"

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
	case "serve-daemon":
		runServeDaemon(os.Args[2:])
	case "humanloop-mcp":
		if err := runHumanloopMCP(os.Args[2:]); err != nil {
			log.Fatalf("driver_agent humanloop-mcp: %v", err)
		}
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
	backend, err := newAgentBackend(cfg)
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

func newAgentBackend(cfg *driver.Config) (agentbackend.Backend, error) {
	return agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.Kind(cfg.Agent.Kind),
		Bin:        cfg.Agent.Bin,
		WorkDir:    cfg.Agent.WorkDir,
		ExtraArgs:  cfg.Agent.ExtraArgs,
		WorkerMode: cfg.Agent.WorkerMode,
	}, nil)
}

type serveDaemonOpts struct {
	ConfigPath string
	Listen     string
}

func parseServeDaemonFlags(args []string) (serveDaemonOpts, error) {
	fs := flag.NewFlagSet("serve-daemon", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to driver.yaml (required)")
	listen := fs.String("listen", "", "HTTP bind override")
	if err := fs.Parse(args); err != nil {
		return serveDaemonOpts{}, err
	}
	if *cfgPath == "" {
		return serveDaemonOpts{}, fmt.Errorf("--config is required")
	}
	return serveDaemonOpts{ConfigPath: *cfgPath, Listen: *listen}, nil
}

func runServeDaemon(args []string) {
	opts, err := parseServeDaemonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve-daemon:", err)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cfg, err := driver.LoadConfig(opts.ConfigPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if cfg.Credentials.ProxyToken == "" {
		die("serve-daemon requires credentials.proxy_token; run `driver-agent register --config " + opts.ConfigPath + "` first")
	}
	if cfg.Observer.URL == "" {
		die("serve-daemon requires observer.url")
	}

	listen := opts.Listen
	if listen == "" {
		listen = cfg.Daemon.Listen
	}
	if strings.HasPrefix(listen, "0.0.0.0") {
		fmt.Fprintln(os.Stderr, "WARNING: serve-daemon HTTP bound to 0.0.0.0; debug API will be reachable from the network")
	}

	backend, err := newAgentBackend(cfg)
	if err != nil {
		die("agentbackend.New: " + err.Error())
	}

	wsURL, insecureWS := daemonWSURL(cfg.Observer.URL, cfg.Daemon.WSPath)
	if insecureWS {
		fmt.Fprintln(os.Stderr, "WARNING: serve-daemon WS uses ws://; credentials.proxy_token will be sent without TLS. Use https:// observer.url outside loopback/debug deployments.")
	}

	handler := &commander.Handler{Backend: backend, WorkerMax: cfg.Daemon.WorkerMax}
	if cfg.Daemon.WorkerIdleTimeoutSec > 0 {
		handler.WorkerIdleTimeout = time.Duration(cfg.Daemon.WorkerIdleTimeoutSec) * time.Second
	}

	d := commander.NewDaemon(commander.DaemonConfig{
		Handler:       handler,
		ListenAddr:    listen,
		HTTPAuthToken: cfg.Credentials.ProxyToken,
		WS: commander.WSConfig{
			URL:        wsURL,
			ProxyToken: cfg.Credentials.ProxyToken,
			Register: commander.RegisterPayload{
				SchemaVersion: commander.SchemaVersion,
				Kind:          cfg.Agent.Kind,
				AgentBin:      cfg.Agent.Bin,
				AgentWorkDir:  cfg.Agent.WorkDir,
				DisplayName:   cfg.Discovery.DisplayName,
				DriverVersion: driverVersion,
			},
			HeartbeatInt:   time.Duration(cfg.Daemon.HeartbeatIntervalSec) * time.Second,
			InitialBackoff: time.Duration(cfg.Daemon.InitialBackoffMs) * time.Millisecond,
			MaxBackoff:     time.Duration(cfg.Daemon.MaxBackoffMs) * time.Millisecond,
		},
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	select {
	case err := <-errCh:
		if err != nil {
			die("daemon: " + err.Error())
		}
		return
	case <-d.Ready():
	case <-ctx.Done():
		if err := <-errCh; err != nil {
			die("daemon: " + err.Error())
		}
		return
	}
	fmt.Fprintf(os.Stderr, "serve-daemon: ws=%s http=http://%s\n", wsURL, d.HTTPAddr())
	if err := <-errCh; err != nil {
		die("daemon: " + err.Error())
	}
}

func daemonWSURL(observerURL, wsPath string) (string, bool) {
	wsURL := strings.TrimRight(observerURL, "/") + wsPath
	switch {
	case strings.HasPrefix(wsURL, "http://"):
		return "ws://" + strings.TrimPrefix(wsURL, "http://"), true
	case strings.HasPrefix(wsURL, "https://"):
		return "wss://" + strings.TrimPrefix(wsURL, "https://"), false
	case strings.HasPrefix(wsURL, "ws://"):
		return wsURL, true
	default:
		return wsURL, false
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "driver-agent:", msg)
	os.Exit(1)
}
