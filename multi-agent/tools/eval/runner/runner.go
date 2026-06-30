package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// --- Errors surfaced as exit 2 ---

var (
	ErrStubMustBeLoopback      = errors.New("eval-runner: --stub-listen must resolve to loopback")
	ErrObserverDBPathForbidden = errors.New("eval-runner: --observer-db path is in a forbidden location")
	ErrWorkloadSpecInvalid     = errors.New("eval-runner: workload spec.yaml invalid")
	ErrStubFailedToStart       = errors.New("eval-runner: agentserver-stub failed to start")
	ErrOracleStdoutNotJSON     = errors.New("eval-runner: oracle stdout first line is not valid JSON {passed,details,metrics}")
)

// Opts holds the runner's per-invocation configuration. Constructed by the
// CLI from flags; tests build it directly to skip flag parsing.
type Opts struct {
	WorkloadID      string
	WorkloadDir     string
	StubListen      string
	StubBin         string // path to agentserver-stub binary; empty = auto-build
	ObserverDB      string
	CodexConfigPath string
	RunID           string
	Timeout         time.Duration
	OutCSV          string
	KeepTempdir     bool

	// Writer is the persistence seam (RunWriter). When nil, the runner
	// defaults to NoopWriter and the CSV is the only output.
	Writer RunWriter

	// AgentStage hook (skeleton). Default: copy mock_workspace into the
	// workspace. Future worktrees plug in real driver/observer/slave spawn.
	AgentStage func(ctx context.Context, ws *Workspace, spec *WorkloadSpec) error

	// OnTempdir, if set, is called with the workspace path right before
	// Cleanup runs. Tests use this to inspect perms before teardown
	// without flipping --keep-tempdir.
	OnTempdir func(string)

	// Stderr / Stdout for diagnostics. Default os.Stderr/os.Stdout.
	Stderr, Stdout *os.File
}

// Result is what Run returns. ExitCode is the value main() should pass to
// os.Exit.
type Result struct {
	Row      RunRow
	ExitCode int
	Err      error
}

// Run is the orchestrator. It executes the pipeline from spec §4 and
// returns a Result; the CLI shim translates Result into os.Exit.
func Run(ctx context.Context, opts Opts) Result {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	// Whether the caller supplied a real writer; used below to decide
	// whether to emit the "WT-1-run-schema pending" diagnostic.
	writerSupplied := opts.Writer != nil
	if !writerSupplied {
		opts.Writer = NoopWriter{}
	}

	// Pre-flight validations (exit 2 on any failure).
	if err := validateStubListen(opts.StubListen); err != nil {
		return preflight(opts, err)
	}
	if err := validateObserverDB(opts.ObserverDB); err != nil {
		return preflight(opts, err)
	}
	if opts.ObserverDB != "" && !writerSupplied {
		// Spec §6: when --observer-db is supplied but the real
		// SQLiteWriter hasn't been wired in (WT-1-run-schema pending),
		// surface a single warning so operators know the DB will not
		// actually be populated. NoopWriter is still used.
		fmt.Fprintln(opts.Stderr, "eval-runner: WT-1-run-schema integration pending; --observer-db will not be populated this run")
	}

	specPath := filepath.Join(opts.WorkloadDir, opts.WorkloadID, "spec.yaml")
	spec, err := LoadWorkloadSpec(specPath)
	if err != nil {
		return preflight(opts, fmt.Errorf("%w: %v", ErrWorkloadSpecInvalid, err))
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = time.Duration(spec.TimeoutSeconds) * time.Second
	}
	runID := opts.RunID
	if runID == "" {
		runID = deriveRunID(opts.WorkloadID)
	}

	// Workspace setup (Security §7(b), §7(g)).
	workloadRoot := filepath.Join(opts.WorkloadDir, opts.WorkloadID)
	fixturesDir := filepath.Join(workloadRoot, "fixtures")
	if _, err := os.Stat(fixturesDir); err != nil {
		fixturesDir = "" // workloads without fixtures still permitted
	}
	ws, err := SetupWorkspace(fixturesDir, opts.KeepTempdir)
	if err != nil {
		return preflight(opts, err)
	}
	defer func() {
		if opts.OnTempdir != nil {
			opts.OnTempdir(ws.Root)
		}
		ws.Cleanup()
	}()

	// Start agentserver-stub. The stub URL is injected as AGENTSERVER_URL
	// into every subprocess we spawn.
	stubURL := "http://" + opts.StubListen
	stubProc, err := startStub(ctx, opts, opts.Stderr)
	if err != nil {
		return preflight(opts, fmt.Errorf("%w: %v", ErrStubFailedToStart, err))
	}
	defer stubProc.kill()

	if err := waitStubReady(ctx, stubURL, 5*time.Second); err != nil {
		return preflight(opts, fmt.Errorf("%w: stub /healthz: %v", ErrStubFailedToStart, err))
	}

	// Agent stage — skeleton copies mock_workspace; real fanout is a
	// future worktree's job. Errors here mark the run as fail, not a
	// pre-flight error.
	startedAt := time.Now()
	if opts.AgentStage != nil {
		if err := opts.AgentStage(ctx, ws, spec); err != nil {
			fmt.Fprintf(opts.Stderr, "eval-runner: agent stage error: %v\n", err)
		}
	}
	// (Default skeleton behaviour — copy mock_workspace — was already
	// applied during SetupWorkspace.)

	// Resolve the oracle script path. The script is named relative to
	// the workload directory by spec.success_oracle (canonically
	// `./oracle.sh`); the runner's subprocess Cwd is the workspace
	// tempdir, NOT the workload dir, so we must hand exec an absolute
	// path. resolveOraclePath also enforces that the resolved path is
	// contained inside workloadRoot — a spec.yaml with
	// `success_oracle: ../../../bin/sh` would otherwise let workload
	// authors point at arbitrary binaries (PR #53 review P1).
	oraclePath, oraclePathErr := resolveOraclePath(workloadRoot, spec.SuccessOracle)
	if oraclePathErr != nil {
		return preflight(opts, fmt.Errorf("%w: %v", ErrWorkloadSpecInvalid, oraclePathErr))
	}
	env := WhitelistEnv(os.Environ(), spec.ID, map[string]string{
		"AGENTSERVER_URL": stubURL,
	})

	res, oracleErr := RunSubprocess(ctx, SubprocessOpts{
		Cmd:     []string{oraclePath, ws.Root},
		Cwd:     ws.Root,
		Env:     env,
		Timeout: timeout,
	})

	// Specific pre-flight errors that should still exit 2.
	if errors.Is(oracleErr, ErrOracleOutputTooLarge) {
		return preflight(opts, oracleErr)
	}

	// Parse oracle output (best-effort; bad JSON → run fails, not exit 2).
	oracleOut := parseOracleStdout(res.Stdout)
	passed := oracleOut.Passed && oracleErr == nil && res.ExitCode == 0
	if oracleErr != nil && !errors.Is(oracleErr, ErrSubprocessTimeout) {
		fmt.Fprintf(opts.Stderr, "eval-runner: oracle subprocess error: %v\n", oracleErr)
	}

	finishedAt := time.Now()

	// commit_meta + git emails.
	commit := collectCommitMeta(ctx, opts, env, opts.Stderr)
	author, committer := collectGitEmails(ctx, opts, env, opts.Stderr)

	// Assemble row.
	row := RunRow{
		RunID:              runID,
		WorkloadID:         spec.ID,
		StartedAtUnix:      startedAt.Unix(),
		FinishedAtUnix:     finishedAt.Unix(),
		DurationMs:         finishedAt.Sub(startedAt).Milliseconds(),
		Passed:             passed,
		OracleExitCode:     res.ExitCode,
		OracleDetailsJSON:  oracleOut.DetailsRaw,
		OracleMetricsJSON:  oracleOut.MetricsRaw,
		LoomCommit:         commit.LoomCommit,
		AgentserverCommit:  commit.AgentserverCommit,
		ModelserverCommit:  commit.ModelserverCommit,
		AppCommit:          commit.AppCommit,
		OSKernel:           commit.OS.Kernel,
		OSDistro:           commit.OS.Distro,
		OSArch:             commit.OS.Arch,
		MachineHostname:    commit.MachineHostname,
		AuthorEmailSHA8:    RedactEmail(author),
		CommitterEmailSHA8: RedactEmail(committer),
		CodexConfigPath:    opts.CodexConfigPath,
		StubListen:         opts.StubListen,
		TempdirKept:        opts.KeepTempdir,
	}

	// Persist + CSV.
	if err := opts.Writer.Insert(ctx, row); err != nil {
		fmt.Fprintf(opts.Stderr, "eval-runner: writer insert: %v\n", err)
	}
	if opts.OutCSV != "" {
		if err := WriteCSVRow(opts.OutCSV, row); err != nil {
			return Result{Row: row, ExitCode: 2, Err: err}
		}
	}

	exit := 1
	if passed {
		exit = 0
	}
	return Result{Row: row, ExitCode: exit}
}

// preflight wraps a pre-flight error (exit 2 path) and prints to stderr.
// The sentinel errors all already carry the "eval-runner:" prefix; we
// emit the wrapped string verbatim rather than double-prefixing it
// (PR #53 review P2).
func preflight(opts Opts, err error) Result {
	if opts.Stderr != nil {
		fmt.Fprintf(opts.Stderr, "%v\n", err)
	}
	return Result{ExitCode: 2, Err: err}
}

// --- Validators (Security §7(d), §7(e)) ---

// validateStubListen enforces Security §7(d) — only loopback hosts.
func validateStubListen(addr string) error {
	if addr == "" {
		return fmt.Errorf("%w: empty", ErrStubMustBeLoopback)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStubMustBeLoopback, err)
	}
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrStubMustBeLoopback)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: %s", ErrStubMustBeLoopback, host)
		}
		return nil
	}
	// Hostname — every resolved address must be loopback.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: lookup %s: %v", ErrStubMustBeLoopback, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: %s resolves to nothing", ErrStubMustBeLoopback, host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: %s resolves to %s (non-loopback)", ErrStubMustBeLoopback, host, ip)
		}
	}
	return nil
}

// validateObserverDB enforces Security §7(e). Empty string is valid.
var forbiddenObserverDBPrefixes = []string{
	"/etc/", "/proc/", "/sys/", "/dev/", "/boot/", "/var/log/",
}

func validateObserverDB(p string) error {
	if p == "" {
		return nil
	}
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrObserverDBPathForbidden, err)
	}
	for _, pref := range forbiddenObserverDBPrefixes {
		if abs == strings.TrimSuffix(pref, "/") || strings.HasPrefix(abs+"/", pref) {
			return fmt.Errorf("%w: %s under %s", ErrObserverDBPathForbidden, abs, pref)
		}
	}
	// Allowed roots: cwd subtree, /tmp/, /var/tmp/, $HOME.
	cwd, _ := os.Getwd()
	home := os.Getenv("HOME")
	allowed := []string{cwd, "/tmp", "/var/tmp"}
	if home != "" {
		allowed = append(allowed, home)
	}
	for _, root := range allowed {
		if root == "" {
			continue
		}
		if abs == root || strings.HasPrefix(abs, root+"/") {
			return nil
		}
	}
	return fmt.Errorf("%w: %s not under cwd/$HOME/tmp", ErrObserverDBPathForbidden, abs)
}

// --- Workload spec ---

// WorkloadSpec mirrors 13_workload_spec.md §1.2. Only the fields the runner
// actually consumes are surfaced; extra fields don't error (the YAML may
// grow without rebuild).
type WorkloadSpec struct {
	ID             string `yaml:"id"`
	Description    string `yaml:"description"`
	SuccessOracle  string `yaml:"success_oracle"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	RecoveryHint   string `yaml:"recovery_hint"`
}

// LoadWorkloadSpec parses spec.yaml and validates the four fields the runner
// hard-depends on. Other 13-spec fields (required_contexts, inputs, outputs)
// pass through silently — they belong to the agent stage, which the
// skeleton stubs out.
func LoadWorkloadSpec(path string) (*WorkloadSpec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s WorkloadSpec
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if s.ID == "" {
		return nil, errors.New("spec.id is empty")
	}
	if s.SuccessOracle == "" {
		return nil, errors.New("spec.success_oracle is empty")
	}
	if s.TimeoutSeconds <= 0 {
		return nil, errors.New("spec.timeout_seconds must be > 0")
	}
	return &s, nil
}

// resolveOraclePath joins workloadRoot with the success_oracle field and
// rejects the result if it escapes workloadRoot. A workload author that
// slips `success_oracle: ../../../bin/sh` past spec review would otherwise
// be able to execute arbitrary host binaries under the runner's identity;
// this symmetry with §7(b) symlink-escape defense closes that gap
// (PR #53 review P1).
func resolveOraclePath(workloadRoot, successOracle string) (string, error) {
	if successOracle == "" {
		return "", errors.New("spec.success_oracle is empty")
	}
	// 13_workload_spec.md §1.3 mandates `./oracle.sh` — workload-relative.
	// Reject absolute paths up front so a typo like `success_oracle:
	// /etc/passwd` surfaces as a clear spec error instead of being
	// silently rewritten by filepath.Join to a non-existent path inside
	// the workload (PR #53 round 2 P2).
	if filepath.IsAbs(successOracle) {
		return "", fmt.Errorf("success_oracle must be workload-relative, got absolute path %q", successOracle)
	}
	rootAbs, err := filepath.Abs(workloadRoot)
	if err != nil {
		return "", fmt.Errorf("abs(workloadRoot): %w", err)
	}
	joined := filepath.Join(rootAbs, strings.TrimPrefix(successOracle, "./"))
	cleaned := filepath.Clean(joined)
	if cleaned != rootAbs && !strings.HasPrefix(cleaned, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("success_oracle escapes workload dir: %q resolves to %q", successOracle, cleaned)
	}
	return cleaned, nil
}

// --- agentserver-stub lifecycle ---

type stubProcess struct {
	cmd  *exec.Cmd
	tmp  string // temp build dir, removed in kill()
	stop func()
}

func (s *stubProcess) kill() {
	if s == nil {
		return
	}
	if s.cmd != nil && s.cmd.Process != nil {
		killGroup(s.cmd.Process.Pid)
		_, _ = s.cmd.Process.Wait()
	}
	if s.tmp != "" {
		_ = os.RemoveAll(s.tmp)
	}
	if s.stop != nil {
		s.stop()
	}
}

// startStub builds agentserver-stub (if not provided) into a tempdir, then
// forks it with --listen=stubListen. Its stdout/stderr are captured into a
// log file for debugging; not surfaced to the caller's streams to avoid
// interleaving with the CSV / row diagnostics.
func startStub(ctx context.Context, opts Opts, stderr *os.File) (*stubProcess, error) {
	stubBin := opts.StubBin
	tmp := ""
	if stubBin == "" {
		// Find the module root by walking up from cwd until go.mod.
		mod := findModuleRoot()
		if mod == "" {
			return nil, errors.New("cannot find module root (go.mod)")
		}
		var err error
		tmp, err = os.MkdirTemp("", "evalrun-stub-")
		if err != nil {
			return nil, err
		}
		stubBin = filepath.Join(tmp, "agentserver-stub")
		buildCmd := exec.CommandContext(ctx, "go", "build", "-o", stubBin, "./tools/eval/agentserver-stub")
		buildCmd.Dir = mod
		buildCmd.Stdout = stderr
		buildCmd.Stderr = stderr
		if err := buildCmd.Run(); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("build agentserver-stub: %w", err)
		}
	}

	cmd := exec.Command(stubBin, "--listen", opts.StubListen, "--workspace-id", "auto")
	setupProcGroup(cmd)
	// Stub logs go to a file so the goroutine exec spawns to drain the
	// pipe doesn't race with anything in our address space. Path is
	// recorded for post-mortem on a failed health check; we don't surface
	// it on the happy path.
	logFile, err := os.CreateTemp("", "agentserver-stub-log-")
	if err != nil {
		if tmp != "" {
			_ = os.RemoveAll(tmp)
		}
		return nil, fmt.Errorf("temp log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		os.Remove(logFile.Name())
		if tmp != "" {
			_ = os.RemoveAll(tmp)
		}
		return nil, fmt.Errorf("start agentserver-stub: %w", err)
	}
	return &stubProcess{cmd: cmd, tmp: tmp, stop: func() {
		logFile.Close()
		_ = os.Remove(logFile.Name())
	}}, nil
}

// waitStubReady polls /healthz until it returns 200, up to deadline.
func waitStubReady(ctx context.Context, url string, deadline time.Duration) error {
	end := time.Now().Add(deadline)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for time.Now().Before(end) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := client.Get(url + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("stub did not become ready within %s", deadline)
}

// findModuleRoot walks up from cwd until a go.mod is found. Returns "" if
// none. (We could read os.Args[0], but tests run via `go test` from the
// package dir, so cwd-walk is more robust.)
func findModuleRoot() string {
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// --- oracle stdout parsing ---

type oracleOutput struct {
	Passed     bool
	DetailsRaw string
	MetricsRaw string
}

// parseOracleStdout takes raw stdout and extracts the first newline-terminated
// JSON line per §1.3. Garbage in stdout doesn't surface an error here —
// we mark passed=false and let the CSV record the row with a noted reason
// in oracle_details_json.
func parseOracleStdout(stdout []byte) oracleOutput {
	if len(stdout) == 0 {
		return oracleOutput{
			DetailsRaw: `{"reason":"oracle stdout empty"}`,
			MetricsRaw: "{}",
		}
	}
	nl := bytes.IndexByte(stdout, '\n')
	var line []byte
	if nl < 0 {
		line = stdout
	} else {
		line = stdout[:nl]
	}
	var raw struct {
		Passed  bool            `json:"passed"`
		Details json.RawMessage `json:"details"`
		Metrics json.RawMessage `json:"metrics"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return oracleOutput{
			DetailsRaw: fmt.Sprintf(`{"reason":"oracle stdout not JSON: %s"}`, jsonSafe(err.Error())),
			MetricsRaw: "{}",
		}
	}
	out := oracleOutput{Passed: raw.Passed}
	if len(raw.Details) > 0 {
		out.DetailsRaw = string(raw.Details)
	} else {
		out.DetailsRaw = "{}"
	}
	if len(raw.Metrics) > 0 {
		out.MetricsRaw = string(raw.Metrics)
	} else {
		out.MetricsRaw = "{}"
	}
	return out
}

func jsonSafe(s string) string {
	b, _ := json.Marshal(s)
	// trim quotes
	return string(b[1 : len(b)-1])
}

// --- commit_meta + git emails ---

type commitMeta struct {
	LoomCommit        string `json:"loom_commit"`
	AgentserverCommit string `json:"agentserver_commit"`
	ModelserverCommit string `json:"modelserver_commit"`
	AppCommit         string `json:"app_commit"`
	OS                struct {
		Kernel string `json:"kernel"`
		Distro string `json:"distro"`
		Arch   string `json:"arch"`
	} `json:"os"`
	CollectedAtUnix int64  `json:"collected_at_unix"`
	MachineHostname string `json:"machine_hostname"`
}

// collectCommitMeta runs `python -m commit_meta.collect --format=json` and
// parses the result. Override via env LOOM_EVAL_COMMIT_META_CMD (test seam).
// Failures degrade to N/A strings rather than aborting the run.
func collectCommitMeta(ctx context.Context, opts Opts, env []string, stderr *os.File) commitMeta {
	// Fallback row used if commit_meta is unreachable. OS fields fall
	// back to runtime.GOOS/GOARCH (always available — we're running
	// in-process) rather than the empty string; the earlier behaviour
	// wrote ",,," for os_{kernel,distro,arch} which downstream schema
	// validators couldn't tell apart from "field missing" (PR #53
	// review P1).
	out := commitMeta{
		LoomCommit:        "N/A: commit_meta unavailable",
		AgentserverCommit: "N/A: commit_meta unavailable",
		ModelserverCommit: "N/A: commit_meta unavailable",
		AppCommit:         "N/A: commit_meta unavailable",
		MachineHostname:   hostnameOrNA(),
	}
	out.OS.Kernel = "N/A: commit_meta unavailable"
	out.OS.Distro = "N/A: commit_meta unavailable"
	out.OS.Arch = runtime.GOARCH
	cmd := []string{"python", "-m", "commit_meta.collect", "--format=json"}
	if override := envGet(env, "LOOM_EVAL_COMMIT_META_CMD"); override != "" {
		cmd = []string{"/bin/sh", "-c", override}
	}
	res, err := RunSubprocess(ctx, SubprocessOpts{
		Cmd:     cmd,
		Env:     env,
		Timeout: 10 * time.Second,
	})
	if err != nil || res.ExitCode != 0 {
		fmt.Fprintf(stderr, "eval-runner: commit_meta failed (err=%v exit=%d); falling back to N/A\n", err, res.ExitCode)
		return out
	}
	var parsed commitMeta
	if err := json.Unmarshal(res.Stdout, &parsed); err != nil {
		fmt.Fprintf(stderr, "eval-runner: commit_meta json parse: %v\n", err)
		return out
	}
	// Per-field merge with the fallback `out`: a commit_meta JSON that
	// omits any of these fields shouldn't leave the CSV column empty
	// (PR #53 round 2 P2). The fallback already carries N/A sentinels
	// for the four commit SHAs, the OS triple, and a real hostname.
	if parsed.LoomCommit == "" {
		parsed.LoomCommit = out.LoomCommit
	}
	if parsed.AgentserverCommit == "" {
		parsed.AgentserverCommit = out.AgentserverCommit
	}
	if parsed.ModelserverCommit == "" {
		parsed.ModelserverCommit = out.ModelserverCommit
	}
	if parsed.AppCommit == "" {
		parsed.AppCommit = out.AppCommit
	}
	if parsed.OS.Kernel == "" {
		parsed.OS.Kernel = out.OS.Kernel
	}
	if parsed.OS.Distro == "" {
		parsed.OS.Distro = out.OS.Distro
	}
	if parsed.OS.Arch == "" {
		parsed.OS.Arch = out.OS.Arch
	}
	if parsed.MachineHostname == "" {
		parsed.MachineHostname = out.MachineHostname
	}
	return parsed
}

// collectGitEmails returns (authorEmail, committerEmail) from the loom repo
// HEAD. Override via env LOOM_EVAL_GIT_EMAIL_CMD (test seam) — the command
// is expected to print "author@x|committer@y" on a single line.
func collectGitEmails(ctx context.Context, _ Opts, env []string, stderr *os.File) (string, string) {
	cmd := []string{"git", "log", "-1", "--format=%ae|%ce"}
	if override := envGet(env, "LOOM_EVAL_GIT_EMAIL_CMD"); override != "" {
		cmd = []string{"/bin/sh", "-c", override}
	}
	res, err := RunSubprocess(ctx, SubprocessOpts{
		Cmd:     cmd,
		Env:     env,
		Timeout: 5 * time.Second,
	})
	if err != nil || res.ExitCode != 0 {
		fmt.Fprintf(stderr, "eval-runner: git-email collect failed (err=%v exit=%d)\n", err, res.ExitCode)
		return "", ""
	}
	line := strings.TrimSpace(string(res.Stdout))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	parts := strings.SplitN(line, "|", 2)
	if len(parts) != 2 {
		// A shim or upstream git that omits the `|` separator would
		// otherwise see the whole line treated as an author and pass
		// through redact unchanged when it lacks an `@`. Surfacing
		// this lets the operator notice a misconfigured override
		// (PR #53 review P2).
		fmt.Fprintf(stderr, "eval-runner: git-email output missing `|` separator: %q; recording as empty\n", line)
		return "", ""
	}
	return parts[0], parts[1]
}

func envGet(env []string, k string) string {
	for _, kv := range env {
		key, val, ok := splitEnv(kv)
		if ok && key == k {
			return val
		}
	}
	return ""
}

func hostnameOrNA() string {
	h, err := os.Hostname()
	if err != nil {
		return "N/A: hostname unavailable"
	}
	return h
}

// --- run id derivation ---

func deriveRunID(workload string) string {
	var buf [4]byte
	_, _ = rand.Read(buf[:])
	return fmt.Sprintf("run-%d-%s-%s", time.Now().Unix(), workload, hex.EncodeToString(buf[:]))
}
