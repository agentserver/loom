package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/capabilitydoc"
	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/dispatch"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/journal"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerclient"
	"github.com/yourorg/multi-agent/internal/platform"
	"github.com/yourorg/multi-agent/internal/poller"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/internal/tunnel"
	"github.com/yourorg/multi-agent/internal/webui"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/opencode"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "humanloop-mcp" {
		if err := runHumanloopMCP(os.Args[2:]); err != nil {
			log.Fatalf("slave_agent humanloop-mcp: %v", err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "serve-daemon" {
		runServeDaemon(os.Args[2:])
		return
	}
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}
	if err := run(cfgPath); err != nil {
		log.Fatalf("slave_agent: %v", err)
	}
}

// acquireInstanceLock takes an exclusive lock on $CWD/slave-agent.lock so two
// slave-agents in the same install dir can't fight for the broker tunnel
// (single-occupancy → 1s EOF flap, the "mode B" outage).
//
// When the lock is already held, behavior depends on whether we were started
// by a service manager (INVOCATION_ID env, set by systemd ≥232): managed starts
// terminate the holder and retake the lock (last-start-wins is the right
// semantics for `systemctl restart` and on-failure respawns); manual starts
// refuse with a clear error so an operator probing with ssh doesn't
// accidentally knock over the production unit.
//
// The holder's pid is written to the lock file so operators can identify it.
// The returned lock must stay open for the process lifetime.
func acquireInstanceLock() (*platform.FileLock, error) {
	lockPath, err := filepath.Abs("slave-agent.lock")
	if err != nil {
		return nil, fmt.Errorf("resolve lock path: %w", err)
	}
	lock, err := platform.TryLock(lockPath)
	if err != nil {
		holderPid := readHolderPid(lockPath)
		if !errors.Is(err, platform.ErrLocked) {
			return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
		}
		if os.Getenv("INVOCATION_ID") == "" {
			return nil, fmt.Errorf("another slave-agent is already running in this install dir "+
				"(lock=%s holder_pid=%d); refusing to start. "+
				"If this is a stale lock, stop the running slave-agent for this install dir and remove %s",
				lockPath, holderPid, lockPath)
		}
		log.Printf("acquireInstanceLock: lock held by pid=%d, taking over (managed start)", holderPid)
		lock, err = takeOverLock(lockPath, holderPid)
		if err != nil {
			return nil, err
		}
	}
	if err := lock.WriteString(fmt.Sprintf("%d\n", os.Getpid())); err != nil {
		_ = lock.Unlock()
		return nil, fmt.Errorf("write lock holder pid: %w", err)
	}
	return lock, nil
}

// readHolderPid parses the pid written by the current lock holder. Returns 0
// on any read/parse failure — callers treat 0 as "unknown holder".
func readHolderPid(lockPath string) int {
	b, err := os.ReadFile(lockPath)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// takeOverLock terminates the current holder (if alive) and retakes the lock.
// Graceful termination gets a 5s window before a forced kill.
// Returns an error if we can't acquire the lock within ~10s total.
func takeOverLock(lockPath string, holderPid int) (*platform.FileLock, error) {
	if holderPid > 0 && holderPid != os.Getpid() {
		if err := platform.TerminatePID(holderPid); err != nil {
			log.Printf("acquireInstanceLock: terminate pid=%d: %v (continuing)", holderPid, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !platform.ProcessExists(holderPid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if platform.ProcessExists(holderPid) {
			log.Printf("acquireInstanceLock: pid=%d still alive after graceful terminate, killing", holderPid)
			_ = platform.KillPID(holderPid)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lock, err := platform.TryLock(lockPath)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, platform.ErrLocked) {
			return nil, fmt.Errorf("try lock %s during takeover: %w", lockPath, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("could not acquire %s after terminating pid=%d", lockPath, holderPid)
}

func run(cfgPath string) error {
	lockFile, err := acquireInstanceLock()
	if err != nil {
		return err
	}
	defer lockFile.Unlock()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	caps := normalizeDiscoveryForRuntime(cfg, commandiface.Detector{})

	s, err := store.Open("data.db")
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.Recover(); err != nil {
		return err
	}

	journalDir, _ := filepath.Abs("journal")
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

	ui := webui.NewHandler(s, journalDir, cfg)
	webui.SetMCPBridge(ui, mcpExec)

	ctx, cancel := signal.NotifyContext(context.Background(), platform.ShutdownSignals()...)
	defer cancel()

	tn := tunnel.New(cfg, cfgPath, ui)
	tn.SetPlatform(caps.Platform)
	tn.SetCommandInterfaces(caps.CommandInterfaces)
	if err := tn.EnsureRegistered(ctx); err != nil {
		return err
	}

	// Resolve CodexHome now that cfg.Credentials.ShortID is populated by EnsureRegistered.
	cfg.Agent.CodexHome = agentbackend.ResolveCodexHome(cfg.Agent.CodexHome, cfg.Agent.LoomHome, cfg.Credentials.ShortID, cfg.Agent.WorkDir)
	backend, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.Kind(cfg.Agent.Kind),
		Bin:        cfg.Agent.Bin,
		WorkDir:    cfg.Agent.WorkDir,
		ExtraArgs:  cfg.Agent.ExtraArgs,
		WorkerMode: cfg.Agent.WorkerMode,
		CodexHome:  cfg.Agent.CodexHome,
	}, nil)
	if err != nil {
		log.Fatalf("agentbackend: %v", err)
	}

	j, err := journal.New(journal.Config{Dir: journalDir, LLM: backend.LLM()})
	if err != nil {
		return err
	}
	applyRuntimeCapabilities(cfg, caps)
	if cfg.Observer.WorkspaceID == "" {
		cfg.Observer.WorkspaceID = cfg.Credentials.WorkspaceID
	}
	if cfg.Observer.AgentID == "" {
		cfg.Observer.AgentID = cfg.Credentials.ShortID
	}

	obs, errObs := observerclient.New(observerclient.Config{
		Enabled:               cfg.Observer.Enabled,
		TelemetryEnabled:      cfg.Observer.TelemetryEnabled,
		TelemetryAPIKey:       cfg.Observer.TelemetryAPIKey,
		URL:                   cfg.Observer.URL,
		WorkspaceID:           cfg.Observer.WorkspaceID,
		WorkspaceName:         cfg.Observer.WorkspaceName,
		AgentID:               cfg.Observer.AgentID,
		AgentRole:             observer.RoleSlave,
		APIKey:                cfg.Observer.APIKey,
		AgentserverProxyToken: cfg.Credentials.ProxyToken,
		TokenStatePath:        cfg.Observer.TokenStatePath,
		ForceRegister:         cfg.Observer.ForceRegister,
	})
	if errObs != nil {
		log.Fatalf("observerclient: %v", errObs)
	}
	defer obs.Close()

	routes := map[string]executor.Executor{
		"mcp": mcpExec,
		"":    backendExecutor{backend},
	}
	routes["chat_resume"] = executor.NewChatResume(executor.ChatResumeConfig{
		Backend:  resumeAdapter{backend},
		FlockDir: filepath.Join(workdir, "humanloop"),
	})
	registerRuntimeShellRoutes(routes, cfg, caps)
	if hasSkill(cfg.Discovery.Skills, "file") {
		// File-jail root comes from the unified agent.workdir
		// (issue #15). PR #14 P1 reverse-mirror band-aid removed.
		routes["file"] = executor.NewFileExecutor(executor.FileConfig{WorkDir: cfg.Agent.WorkDir})
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
			Permissions:    backend.Permissions(),
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
	if hasSkill(cfg.Discovery.Skills, "unregister_mcp") {
		routes["unregister_mcp"] = executor.NewUnregisterMCPExecutor(executor.UnregisterMCPConfig{
			WorkDir: workdir,
			MCPExec: mcpExec,
			Republish: func(ctx context.Context) error {
				refreshCapabilities(ctx, "unregister_mcp removed MCP server")
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

	// Auto-start the Commander daemon alongside the poller when configured.
	// Default policy: enable iff observer.url + proxy_token are both set —
	// the same preconditions serve-daemon enforces. This is what makes the
	// existing slave-agent.service unit report sessions to Commander without
	// any deployment-side changes (#24 P2 review feedback). Operators can
	// disable via `daemon.auto_start: false` for worker-only deployments.
	if shouldAutoStartDaemon(cfg) {
		daemon, dErr := buildSlaveDaemon(cfg, backend)
		if dErr != nil {
			log.Printf("commander daemon disabled: %v", dErr)
		} else {
			g.Go(func() error {
				// Wait for the yamux tunnel to actually attach to
				// agentserver before dialing observer. Observer
				// validates every WS handshake via agentserver's
				// /api/agent/whoami; whoami returns 401/403 until the
				// tunnel has put the sandbox into the running state.
				// Without this gate the daemon races tn.Run and reliably
				// gets bounced on cold start — the historical
				// "slave-daemon-startup-race" symptom that left
				// freshly-registered slaves invisible to commander UI.
				select {
				case <-tn.Ready():
				case <-gctx.Done():
					return nil
				}
				if rErr := daemon.Run(gctx); rErr != nil && rErr != context.Canceled {
					return fmt.Errorf("commander daemon: %w", rErr)
				}
				return nil
			})
			go func() {
				select {
				case <-daemon.Ready():
					fmt.Fprintf(os.Stderr, "slave-agent: commander daemon ready http=http://%s\n", daemon.HTTPAddr())
				case <-gctx.Done():
				}
			}()
		}
	}

	err = g.Wait()
	if err != nil && err != context.Canceled {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}

// shouldAutoStartDaemon decides whether the slave's run() loop should bring
// up a Commander daemon alongside the poller. Explicit `daemon.auto_start`
// always wins; otherwise the daemon turns on iff both preconditions
// runServeDaemon would have validated (proxy_token + observer.url) are met.
// Empty ShortID is treated as "not yet registered" — the daemon stays off
// until the next start cycle reads the populated config.
func shouldAutoStartDaemon(cfg *config.Config) bool {
	if cfg.Daemon.AutoStart != nil {
		return *cfg.Daemon.AutoStart
	}
	return cfg.Credentials.ProxyToken != "" &&
		cfg.Credentials.ShortID != "" &&
		cfg.Observer.URL != ""
}

// buildSlaveDaemon constructs the Commander daemon used by both
// runServeDaemon (standalone subcommand) and run() (auto-start path).
// The caller owns the backend and the daemon lifetime.
func buildSlaveDaemon(cfg *config.Config, backend agentbackend.Backend) (*commander.Daemon, error) {
	if cfg.Credentials.ProxyToken == "" {
		return nil, fmt.Errorf("credentials.proxy_token is required")
	}
	if cfg.Credentials.ShortID == "" {
		return nil, fmt.Errorf("credentials.short_id is required (run registration first)")
	}
	if cfg.Observer.URL == "" {
		return nil, fmt.Errorf("observer.url is required")
	}

	listen := cfg.Daemon.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	if strings.HasPrefix(listen, "0.0.0.0") {
		fmt.Fprintln(os.Stderr, "WARNING: slave commander HTTP bound to 0.0.0.0; debug API will be reachable from the network")
	}

	wsURL, insecureWS := daemonWSURL(cfg.Observer.URL, cfg.Daemon.WSPath)
	if insecureWS {
		fmt.Fprintln(os.Stderr, "WARNING: slave commander WS uses ws://; credentials.proxy_token will be sent without TLS. Use https:// observer.url outside loopback/debug deployments.")
	}

	handler := &commander.Handler{Backend: backend, WorkerMax: cfg.Daemon.WorkerMax}
	if cfg.Daemon.WorkerIdleTimeoutSec > 0 {
		handler.WorkerIdleTimeout = time.Duration(cfg.Daemon.WorkerIdleTimeoutSec) * time.Second
	}

	return commander.NewDaemon(commander.DaemonConfig{
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
				DriverVersion: "", // slave has no version constant; driver_version left empty
				ShortID:       cfg.Credentials.ShortID,
			},
			HeartbeatInt:   time.Duration(cfg.Daemon.HeartbeatIntervalSec) * time.Second,
			InitialBackoff: time.Duration(cfg.Daemon.InitialBackoffMs) * time.Millisecond,
			MaxBackoff:     time.Duration(cfg.Daemon.MaxBackoffMs) * time.Millisecond,
		},
	}), nil
}

// backendExecutor adapts agentbackend.Backend to executor.Executor.
type backendExecutor struct {
	b agentbackend.Backend
}

func (be backendExecutor) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	return be.b.Run(ctx, t, sink)
}

// resumeAdapter adapts agentbackend.Backend to executor.ResumeBackend so
// ChatResumeExecutor can call RunResume without importing agentbackend.
type resumeAdapter struct{ b agentbackend.Backend }

func (a resumeAdapter) Run(ctx context.Context, t executor.Task, s executor.Sink) (executor.Result, error) {
	return a.b.Run(ctx, t, s)
}

// RunResume is the seam between internal/executor.ResumeBackend (bare string,
// permanently — see spec §"Why ResumeBackend stays string") and the typed
// agentbackend.Backend.RunResume(SessionRef) above the seam. The slave's
// ChatResumeExecutor passes the slave's own backend-native session id (sourced
// from the slave's kind marker output), so the wrap can populate
// SessionRef.Backend with confidence; Kind comes from the Backend interface,
// AgentID is empty (single-backend seam, no cross-agent disambiguation).
func (a resumeAdapter) RunResume(ctx context.Context, sid, ans string, s executor.Sink) (executor.Result, error) {
	if sid == "" {
		return executor.Result{}, fmt.Errorf("resumeAdapter: empty session id; cannot resume")
	}
	ref := agentbackend.NewBackend(a.b.Kind(), "", sid)
	return a.b.RunResume(ctx, ref, ans, s)
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

// --------------------------------------------------------------------------
// serve-daemon subcommand
// --------------------------------------------------------------------------

// serveDaemonOpts holds parsed serve-daemon flags.
type serveDaemonOpts struct {
	ConfigPath string
	Listen     string
}

// parseServeDaemonFlags parses the serve-daemon flag set. Mirrors driver's parseServeDaemonFlags.
func parseServeDaemonFlags(args []string) (serveDaemonOpts, error) {
	fs := flag.NewFlagSet("serve-daemon", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to slave config.yaml (required)")
	listen := fs.String("listen", "", "HTTP bind override")
	if err := fs.Parse(args); err != nil {
		return serveDaemonOpts{}, err
	}
	if *cfgPath == "" {
		return serveDaemonOpts{}, fmt.Errorf("--config is required")
	}
	return serveDaemonOpts{ConfigPath: *cfgPath, Listen: *listen}, nil
}

// runServeDaemon implements the serve-daemon subcommand for the slave agent.
// It mirrors cmd/driver-agent/main.go runServeDaemon; slave codex sessions
// become listable by Commander once connected.
func runServeDaemon(args []string) {
	opts, err := parseServeDaemonFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve-daemon:", err)
		os.Exit(2)
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		die("load config: " + err.Error())
	}
	if opts.Listen != "" {
		// --listen override flows through cfg so buildSlaveDaemon sees it.
		cfg.Daemon.Listen = opts.Listen
	}

	cfg.Agent.CodexHome = agentbackend.ResolveCodexHome(cfg.Agent.CodexHome, cfg.Agent.LoomHome, cfg.Credentials.ShortID, cfg.Agent.WorkDir)
	backend, err := agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.Kind(cfg.Agent.Kind),
		Bin:        cfg.Agent.Bin,
		WorkDir:    cfg.Agent.WorkDir,
		ExtraArgs:  cfg.Agent.ExtraArgs,
		WorkerMode: cfg.Agent.WorkerMode,
		CodexHome:  cfg.Agent.CodexHome,
	}, nil)
	if err != nil {
		die("agentbackend.New: " + err.Error())
	}

	d, err := buildSlaveDaemon(cfg, backend)
	if err != nil {
		die("serve-daemon: " + err.Error())
	}

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
	wsURLLog, _ := daemonWSURL(cfg.Observer.URL, cfg.Daemon.WSPath)
	fmt.Fprintf(os.Stderr, "serve-daemon: ws=%s http=http://%s\n", wsURLLog, d.HTTPAddr())
	if err := <-errCh; err != nil {
		die("daemon: " + err.Error())
	}
}

// daemonWSURL converts an observer HTTP(S) URL + wsPath into a WS URL.
// Duplicated from cmd/driver-agent/main.go — TODO: move to a shared internal helper.
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

// die prints a fatal error message to stderr and exits with code 1.
func die(msg string) {
	fmt.Fprintln(os.Stderr, "slave-agent:", msg)
	os.Exit(1)
}
