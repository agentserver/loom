package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// findRepoModuleRoot is a test-only helper: walk up until we find a go.mod
// whose first line names this module, then return that directory. Lets the
// tests run from anywhere and still locate tests/eval/workloads/.
func findRepoModuleRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	for {
		p := filepath.Join(d, "go.mod")
		if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte("module github.com/yourorg/multi-agent")) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatal("no go.mod for the multi-agent module found above test cwd")
		}
		d = parent
	}
}

// pickFreePort asks the kernel for a free loopback port and returns it as
// "127.0.0.1:<port>". The lifecycle gap (we close + caller binds) is fine
// for in-process tests; kernels reuse ports lazily.
func pickFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// commitMetaShim writes a tiny shell script that emits a fixed JSON commit
// blob; the runner picks it up via LOOM_EVAL_COMMIT_META_CMD. Tests use
// this both to skip the missing python dependency and to inject controlled
// values for the redaction test.
func commitMetaShim(t *testing.T, json string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "commit-meta-shim.sh")
	body := "#!/bin/sh\ncat <<'EOF'\n" + json + "\nEOF\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	return script
}

// gitEmailShim returns an env value that the runner runs via /bin/sh -c
// to obtain author|committer emails.
func gitEmailShim(t *testing.T, emails string) string {
	t.Helper()
	_ = t
	return "printf '%s' '" + emails + "'"
}

// commitMetaJSON returns a default commit_meta JSON blob for tests that
// don't care about the contents.
func commitMetaJSON() string {
	return `{
  "loom_commit": "abc1234 (test clean)",
  "agentserver_commit": "N/A: not present at /root/agentserver",
  "modelserver_commit": "N/A: not present at /root/modelserver",
  "app_commit": "N/A: not present at /root/app",
  "os": {"kernel": "Linux test", "distro": "Test", "arch": "x86_64"},
  "collected_at_unix": 1700000000,
  "machine_hostname": "test-host"
}`
}

// withShims sets test-shim env vars for the duration of the test.
func withShims(t *testing.T, commitJSON, emails string) {
	t.Helper()
	t.Setenv("LOOM_EVAL_COMMIT_META_CMD", commitMetaShim(t, commitJSON))
	t.Setenv("LOOM_EVAL_GIT_EMAIL_CMD", gitEmailShim(t, emails))
}

// stubBinaryPath builds (or reuses) agentserver-stub once per test process.
func stubBinaryPath(t *testing.T) string {
	t.Helper()
	root := findRepoModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "agentserver-stub")
	cmd := []string{"go", "build", "-o", bin, "./tools/eval/agentserver-stub"}
	c := newCmd(cmd, root)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	return bin
}

// newCmd is a tiny wrapper around exec for tests that want to invoke real
// processes — distinct from the in-runner subprocess plumbing so test
// failures aren't ambiguous about which layer broke.
func newCmd(cmd []string, dir string) *cmdShim {
	return &cmdShim{argv: cmd, dir: dir}
}

type cmdShim struct {
	argv []string
	dir  string
}

func (c *cmdShim) CombinedOutput() ([]byte, error) {
	res, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     c.argv,
		Cwd:     c.dir,
		Env:     os.Environ(),
		Timeout: 60 * time.Second,
	})
	out := append([]byte{}, res.Stdout...)
	out = append(out, res.Stderr...)
	return out, err
}

// TestRun_CrossDeviceCodeMod_HappyPath_CSVOneLine — acceptance test. The
// real workload (cross-device-code-mod) with its mock_workspace artefact
// set; runner produces a 2-line CSV (header + data) with passed=true.
func TestRun_CrossDeviceCodeMod_HappyPath_CSVOneLine(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "alice@example.com|alice@example.com")

	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d (err=%v); row=%+v", res.ExitCode, res.Err, res.Row)
	}

	rows := readCSV(t, outCSV)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (header+data)", len(rows))
	}
	header := rows[0]
	data := rows[1]
	if header[0] != "run_id" || header[5] != "passed" {
		t.Fatalf("header drift: %v", header)
	}
	if data[5] != "true" {
		t.Errorf("passed col = %q, want true", data[5])
	}
	if data[1] != "cross-device-code-mod" {
		t.Errorf("workload_id col = %q", data[1])
	}
}

// TestCommitMetaRedacted_Email — Security §7(c). Inject a commit_meta JSON
// and a git-email shim with named addresses; CSV columns must contain only
// the 8-hex SHAs, never the plaintext "@".
func TestCommitMetaRedacted_Email(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "user@example.com|other@x.org")

	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, err = %v", res.ExitCode, res.Err)
	}
	rows := readCSV(t, outCSV)
	data := rows[1]

	wantAuthor := RedactEmail("user@example.com")
	wantCommitter := RedactEmail("other@x.org")
	if data[17] != wantAuthor {
		t.Errorf("author_email_sha8 = %q, want %q", data[17], wantAuthor)
	}
	if data[18] != wantCommitter {
		t.Errorf("committer_email_sha8 = %q, want %q", data[18], wantCommitter)
	}
	full := strings.Join(data, ",")
	if strings.ContainsRune(full, '@') {
		t.Errorf("CSV contains '@'; redaction leaked: %s", full)
	}
}

// TestRunWriter_NoopByDefault — when --observer-db is unset, the CSV is
// still written and the run completes; we don't emit a "run-schema pending"
// warning on stderr (operator noise is bad UX in the default path).
func TestRunWriter_NoopByDefault(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "x@y|x@y")

	var stderr bytes.Buffer
	outCSV := filepath.Join(t.TempDir(), "run.csv")
	stderrFile, err := os.CreateTemp("", "stderr-")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer os.Remove(stderrFile.Name())
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
		Stderr:      stderrFile,
	})
	stderrFile.Close()
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if !fileExistsPlain(outCSV) {
		t.Errorf("CSV not written in NoopWriter default")
	}
	if b, _ := os.ReadFile(stderrFile.Name()); bytes.Contains(b, []byte("run-schema integration pending")) {
		t.Errorf("operator noise leaked in default path:\n%s", b)
	}
	_ = stderr
}

// TestRunWriter_NoopWithObserverDB_WarnsOnce — when --observer-db IS set
// but the SQLiteWriter hasn't been wired in yet, the runner emits a single
// "WT-1-run-schema integration pending" line so operators know the DB will
// not be populated this run. Defends spec §6 and the diagnostic-noise
// boundary in TestRunWriter_NoopByDefault.
func TestRunWriter_NoopWithObserverDB_WarnsOnce(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "x@y|x@y")

	stderrFile, err := os.CreateTemp("", "stderr-")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer os.Remove(stderrFile.Name())
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		ObserverDB:  filepath.Join(t.TempDir(), "run.db"),
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      stderrFile,
	})
	stderrFile.Close()
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	b, _ := os.ReadFile(stderrFile.Name())
	if !bytes.Contains(b, []byte("WT-1-run-schema integration pending")) {
		t.Errorf("missing run-schema-pending diagnostic; stderr=\n%s", b)
	}
	if bytes.Count(b, []byte("WT-1-run-schema integration pending")) != 1 {
		t.Errorf("diagnostic emitted more than once; stderr=\n%s", b)
	}
}

// TestStubListen_RejectsNonLoopback_0000 — Security §7(d).
func TestStubListen_RejectsNonLoopback_0000(t *testing.T) {
	t.Parallel()
	res := runWithStub(t, "0.0.0.0:18080")
	expectPreflight(t, res, ErrStubMustBeLoopback)
}

// TestStubListen_RejectsNonLoopback_External — Security §7(d).
func TestStubListen_RejectsNonLoopback_External(t *testing.T) {
	t.Parallel()
	res := runWithStub(t, "10.0.0.5:18080")
	expectPreflight(t, res, ErrStubMustBeLoopback)
}

// TestStubListen_RejectsEmpty — defends the empty-flag path; flag default
// is loopback, but a user can pass --stub-listen="".
func TestStubListen_RejectsEmpty(t *testing.T) {
	t.Parallel()
	res := runWithStub(t, "")
	expectPreflight(t, res, ErrStubMustBeLoopback)
}

// TestObserverDB_RejectsEtc — Security §7(e). Reject /etc/, /proc/, /sys/.
func TestObserverDB_RejectsEtc(t *testing.T) {
	t.Parallel()
	for _, db := range []string{
		"/etc/passwd",
		"/proc/self/environ",
		"/sys/kernel/debug/x",
		"/dev/null/foo",
		"/boot/grub.cfg",
		"/var/log/syslog",
	} {
		t.Run(db, func(t *testing.T) {
			res := runWithObserverDB(t, db)
			expectPreflight(t, res, ErrObserverDBPathForbidden)
		})
	}
}

// TestObserverDB_AcceptsTmp — sanity that a path in /tmp/ is accepted.
func TestObserverDB_AcceptsTmp(t *testing.T) {
	t.Parallel()
	if err := validateObserverDB("/tmp/eval-runner.db"); err != nil {
		t.Errorf("validate /tmp/...: %v", err)
	}
}

// TestSetupWorkspace_OnTempdirHook_Perm0700 — Security §7(g) via the
// OnTempdir hook (no --keep-tempdir needed). Run a happy-path single
// workload and capture the tempdir's mode bits before Cleanup runs.
func TestSetupWorkspace_OnTempdirHook_Perm0700(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "x@y|x@y")

	var capturedMode os.FileMode
	var capturedPath string
	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
		OnTempdir: func(p string) {
			if info, err := os.Stat(p); err == nil {
				capturedMode = info.Mode().Perm()
				capturedPath = p
			}
		},
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if capturedMode != 0o700 {
		t.Errorf("tempdir perm = %o, want 0700 (path was %s)", capturedMode, capturedPath)
	}
}

// TestTempdir_CleanedOnExit — default cleanup behaviour. Capture the
// tempdir path during the run; ASSERT it's gone after Run returns.
func TestTempdir_CleanedOnExit(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "x@y|x@y")

	var capturedPath string
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		OnTempdir:   func(p string) { capturedPath = p },
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if capturedPath == "" {
		t.Fatal("OnTempdir not called")
	}
	if _, err := os.Stat(capturedPath); err == nil {
		t.Errorf("tempdir %s still exists after Run", capturedPath)
	}
}

// TestRun_OracleOutputTooLarge_Exit2 — Security §7(f). Substitute a
// custom oracle (in a per-test workload dir) that spews 2 MiB; expect
// exit 2 and CSV is NOT written.
func TestRun_OracleOutputTooLarge_Exit2(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash unavailable")
	}
	withShims(t, commitMetaJSON(), "x@y|x@y")

	workloadDir := mkTestWorkload(t, "spew-workload", "#!/bin/sh\nyes x | head -c 2097152\n", 60)
	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "spew-workload",
		WorkloadDir: workloadDir,
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
	})
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2; err=%v", res.ExitCode, res.Err)
	}
	if !errors.Is(res.Err, ErrOracleOutputTooLarge) {
		t.Errorf("err = %v, want ErrOracleOutputTooLarge", res.Err)
	}
	if fileExistsPlain(outCSV) {
		t.Errorf("CSV was written despite pre-flight failure")
	}
}

// TestRun_FixturesUnchanged — Security §7(b). After a real run, the source
// workload's fixtures dir is byte-identical to before.
func TestRun_FixturesUnchanged(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "x@y|x@y")

	fixturesDir := filepath.Join(root, "tests/eval/workloads/cross-device-code-mod/fixtures")
	before := hashTreeForTest(t, fixturesDir)
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	after := hashTreeForTest(t, fixturesDir)
	if before != after {
		t.Fatalf("fixtures tree mutated by run; security §7(b) violated")
	}
}

// --- helpers ---

func runWithStub(t *testing.T, stubListen string) Result {
	t.Helper()
	return Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: "tests/eval/workloads",
		StubListen:  stubListen,
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      discardStderr(t),
	})
}

func runWithObserverDB(t *testing.T, db string) Result {
	t.Helper()
	return Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: "tests/eval/workloads",
		StubListen:  pickFreePort(t),
		ObserverDB:  db,
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      discardStderr(t),
	})
}

func expectPreflight(t *testing.T, res Result, want error) {
	t.Helper()
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2; err=%v", res.ExitCode, res.Err)
	}
	if !errors.Is(res.Err, want) {
		t.Fatalf("err = %v, want %v", res.Err, want)
	}
}

func discardStderr(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp("", "discard-stderr-")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	t.Cleanup(func() {
		f.Close()
		os.Remove(f.Name())
	})
	return f
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open CSV: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	return rows
}

func fileExistsPlain(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// mkTestWorkload builds a transient workload dir + spec.yaml + oracle.sh
// for tests that need a custom oracle (e.g. the 2 MiB spew test). Returns
// the parent dir suitable for --workload-dir.
func mkTestWorkload(t *testing.T, id, oracleBody string, timeoutSec int) string {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, id)
	fixDir := filepath.Join(dir, "fixtures")
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		t.Fatalf("mkdir workload: %v", err)
	}
	spec := fmt.Sprintf(`id: %s
description: test workload
required_contexts: []
allowed_contexts: ["*"]
inputs:
  read_artifacts: []
outputs:
  write_targets: []
success_oracle: ./oracle.sh
recovery_hint: test
timeout_seconds: %d
`, id, timeoutSec)
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "oracle.sh"), []byte(oracleBody), 0o755); err != nil {
		t.Fatalf("write oracle: %v", err)
	}
	return parent
}

// hashTreeForTest mirrors fixtures_test.go's hashTree but lives in the
// runner-test file so cross-test reuse is explicit.
func hashTreeForTest(t *testing.T, root string) string {
	t.Helper()
	return hashTree(t, root)
}
