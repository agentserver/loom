package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	"github.com/yourorg/multi-agent/internal/platform"
	"github.com/yourorg/multi-agent/internal/poller"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/internal/tunnel"
	"github.com/yourorg/multi-agent/internal/webui"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "humanloop-mcp" {
		if err := runHumanloopMCP(os.Args[2:]); err != nil {
			log.Fatalf("slave_agent humanloop-mcp: %v", err)
		}
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

	ctx, cancel := signal.NotifyContext(context.Background(), platform.ShutdownSignals()...)
	defer cancel()

	tn := tunnel.New(cfg, cfgPath, ui)
	if err := tn.EnsureRegistered(ctx); err != nil {
		return err
	}
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

// resumeAdapter adapts agentbackend.Backend to executor.ResumeBackend so
// ChatResumeExecutor can call RunResume without importing agentbackend.
type resumeAdapter struct{ b agentbackend.Backend }

func (a resumeAdapter) Run(ctx context.Context, t executor.Task, s executor.Sink) (executor.Result, error) {
	return a.b.Run(ctx, t, s)
}
func (a resumeAdapter) RunResume(ctx context.Context, sid, ans string, s executor.Sink) (executor.Result, error) {
	return a.b.RunResume(ctx, sid, ans, s)
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
