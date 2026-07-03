package main

// WT-2 B4: driverUserspaceAdapter bridges *userspace.Store to the
// narrow driver.UserspaceSearcher interface Lookup depends on. The
// two types are structurally different because internal/driver stays
// free of the internal/userspace import.

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
	"github.com/yourorg/multi-agent/internal/evalrun"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerclient"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/orchestration"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
	"github.com/yourorg/multi-agent/internal/userspace"
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
	// WT-2 B4: propagate the eval-runner's run_id (spawner sets
	// LOOM_EVAL_RUN_ID before exec'ing driver-agent). Empty is fine
	// for interactive/ad-hoc driver sessions; registry_lookup_samples
	// rows land with run_id='' in that case.
	if runID := os.Getenv("LOOM_EVAL_RUN_ID"); runID != "" {
		driver.SetCurrentRunID(runID)
	}
	// WT-2 B6: wire the promotion-audit writer if a local observer.db
	// path is configured. Empty path leaves the writer nil — the
	// register / unregister tools then degrade to a helper-error log
	// per §3.4 step 6 while the slave register itself still succeeds.
	if cfg.Observer.PromotionAuditDBPath != "" {
		promoStore, err := observerstore.OpenSQLite(cfg.Observer.PromotionAuditDBPath)
		if err != nil {
			log.Printf("promotion_audit: OpenSQLite(%q): %v — audit writer disabled",
				cfg.Observer.PromotionAuditDBPath, err)
		} else {
			// PR #71 round-2 review P1-C: replay promotion_audit
			// history into the in-process slaveRegistryView so a
			// driver-agent sharing this observer.db with another
			// process sees the same starting registry hash. Best-
			// effort — see docstring for the trade-offs.
			if err := driver.ReconstructRegistryViewFromAudit(context.Background(), promoStore.DB()); err != nil {
				log.Printf("promote_candidate: registry view reconstruct failed: %v — continuing with empty view", err)
			}
			tools.SetPromotionAuditWriter(promotionaudit.NewSQLiteWriter(promoStore.DB()))
			// WT-2 B4: same local observer store handle powers the
			// registry_lookup_samples writer AND the userspace search
			// backing driver.Lookup. Remote-observer prod deployments
			// (which leave PromotionAuditDBPath empty) don't get
			// Lookup samples yet — future HTTP-piped variant handles
			// that; for now Lookup runs registry-only with a WARN
			// per §7 (d).
			// PR #71 round-2 review P1-B: both writers now honour the
			// shared NoObserver ablation (via evalrun.DisableTelemetry)
			// so all three observer-store writers (promotion_audit +
			// promote_candidates + registry_lookup_samples) behave
			// symmetrically under `--ablation NoObserver`.
			isNoObserver := func() bool { return evalrun.DisableTelemetry }
			lookupSampleWriter := observerstore.NewRegistryLookupSamplesWriterWithAblation(promoStore.DB(), isNoObserver)
			usStore := userspace.NewStore(promoStore.DB())
			// WT-2 B1: promote-candidate deps + expiry goroutine.
			promoWriter := observerstore.NewPromoteCandidatesWriterWithAblation(promoStore.DB(), isNoObserver)
			driver.SetPromoteCandidateDeps(driver.PromoteCandidateDeps{
				Writer: &driverPromoWriterAdapter{w: promoWriter},
				Events: obs,
				Now:    time.Now,
			})
			// 5-minute sweep; 24h cutoff. The goroutine uses
			// context.Background() because it starts before the
			// signal-cancellable ctx is declared below; on process
			// exit the goroutine dies with the process. A future
			// refactor can hoist ctx creation earlier.
			go func() {
				sweepCtx := context.Background()
				tick := time.NewTicker(5 * time.Minute)
				defer tick.Stop()
				for range tick.C {
					cutoff := time.Now().Add(-24 * time.Hour)
					if _, err := driver.ExpireCandidatesOlderThan(sweepCtx, cutoff); err != nil {
						log.Printf("promote_candidate: expiry sweep: %v", err)
					}
				}
			}()

			driver.SetLookupDeps(driver.LookupDeps{
				UserspaceStore: &driverUserspaceAdapter{store: usStore},
				WorkspaceID:    cfg.Observer.WorkspaceID,
				UserID:         "", // driver-agent does not carry a user identity
				CurrentRunID:   driver.CurrentRunID,
				SampleWrite: func(ctx context.Context, s driver.RegistryLookupSample) error {
					return lookupSampleWriter.WriteRegistryLookupSample(ctx, observerstore.RegistryLookupSampleRow{
						TS:              s.Queried,
						RunID:           s.RunID,
						WorkspaceID:     s.WorkspaceID,
						QueryHashPrefix: s.QueryHashPrefix,
						HitCount:        s.HitCount,
						RegistryHits:    s.RegistryHits,
						UserspaceHits:   s.UserspaceHits,
						TopScore:        s.TopScore,
					})
				},
			})
			defer promoStore.Close()
		}
	}
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
	cfg.Agent.CodexHome = agentbackend.ResolveCodexHome(cfg.Agent.CodexHome, cfg.Agent.LoomHome, cfg.Credentials.ShortID, cfg.Agent.WorkDir)
	return agentbackend.New(agentbackend.Config{
		Kind:       agentbackend.Kind(cfg.Agent.Kind),
		Bin:        cfg.Agent.Bin,
		WorkDir:    cfg.Agent.WorkDir,
		ExtraArgs:  cfg.Agent.ExtraArgs,
		WorkerMode: cfg.Agent.WorkerMode,
		CodexHome:  cfg.Agent.CodexHome,
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
	if cfg.Credentials.ShortID == "" {
		// ShortID is the stable agent identity Commander uses to build the
		// (owner_agent_id, session_id) parent index across daemons (P3). An
		// empty ShortID would still register, but driver sessions would have
		// no owner namespace and cross-daemon nesting could collide.
		die("serve-daemon requires credentials.short_id; run `driver-agent register --config " + opts.ConfigPath + "` first")
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
				ShortID:       cfg.Credentials.ShortID,
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

// driverUserspaceAdapter bridges *userspace.Store to
// driver.UserspaceSearcher. Kept in the driver-agent binary so the
// internal/driver package stays free of the internal/userspace import.
type driverUserspaceAdapter struct{ store *userspace.Store }

func (a *driverUserspaceAdapter) SearchPackagesForIdentity(q, workspaceID, userID, kindFilter string, limit int) ([]driver.PackageHit, error) {
	rows, err := a.store.SearchPackagesForIdentity(q, workspaceID, userID, kindFilter, limit)
	if err != nil {
		return nil, err
	}
	out := make([]driver.PackageHit, len(rows))
	for i, r := range rows {
		out[i] = driver.PackageHit{
			Slug:        r.Slug,
			Description: r.Description,
			// Rank derived by position in the driver layer.
		}
	}
	return out, nil
}

// driverPromoWriterAdapter bridges observerstore.PromoteCandidatesWriter
// to driver.PromoteCandidatesWriter. The two types are structurally
// identical but live in different packages so internal/driver stays
// free of the internal/observerstore import.
type driverPromoWriterAdapter struct{ w observerstore.PromoteCandidatesWriter }

func (a *driverPromoWriterAdapter) InsertPromoteCandidate(ctx context.Context, row driver.PromoteCandidateRow) error {
	return a.w.InsertPromoteCandidate(ctx, observerstore.PromoteCandidateRow{
		CandidateID:   row.CandidateID,
		Family:        row.Family,
		SourceTaskIDs: row.SourceTaskIDs,
		SurfacedAt:    row.SurfacedAt,
		SurfacedBy:    row.SurfacedBy,
		WorkspaceID:   row.WorkspaceID,
		RunID:         row.RunID,
	})
}
func (a *driverPromoWriterAdapter) UpdatePromoteCandidateDecision(ctx context.Context, cid, dec, at string) error {
	return a.w.UpdatePromoteCandidateDecision(ctx, cid, dec, at)
}
func (a *driverPromoWriterAdapter) ExpirePromoteCandidates(ctx context.Context, cutoff string) (int, error) {
	return a.w.ExpirePromoteCandidates(ctx, cutoff)
}
