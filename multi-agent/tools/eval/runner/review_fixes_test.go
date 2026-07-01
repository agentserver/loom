package main

// Regression tests for the findings of PR #53's independent review pass.
// Each test names the finding it locks down so a future refactor that
// silently undoes the fix trips a clear failure.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReview_P0_RunSubprocess_StressFastExit — the P0 race: a fast-exiting
// oracle that prints ~10 KiB used to flake under stress with
// `oracle stdout drain: read |0: file already closed`. Stress the same
// path 50× to lock down the fix (bounded writer assigned to cmd.Stdout
// instead of a user-side drain goroutine).
func TestReview_P0_RunSubprocess_StressFastExit(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nyes x | head -c 10240\n")
	for i := 0; i < 50; i++ {
		res, err := RunSubprocess(context.Background(), SubprocessOpts{
			Cmd:     script,
			Timeout: 5e9, // 5s
			Env:     []string{"PATH=/usr/bin:/bin"},
		})
		if err != nil {
			t.Fatalf("iter %d: err = %v (this was the original race)", i, err)
		}
		if res.ExitCode != 0 {
			t.Fatalf("iter %d: exit = %d", i, res.ExitCode)
		}
	}
}

// TestReview_P1_OracleEscape_Rejected — a workload spec.yaml whose
// success_oracle field resolves outside its workload dir must be rejected
// at pre-flight (exit 2 with ErrWorkloadSpecInvalid).  Earlier code would
// happily exec /bin/echo or any host binary.
func TestReview_P1_OracleEscape_Rejected(t *testing.T) {
	parent := t.TempDir()
	id := "escapy-workload"
	dir := filepath.Join(parent, id)
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	spec := `id: escapy-workload
description: tries to escape
required_contexts: []
allowed_contexts: ["*"]
inputs:
  read_artifacts: []
outputs:
  write_targets: []
success_oracle: ../../../../../../bin/echo
recovery_hint: x
timeout_seconds: 60
`
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	res := Run(context.Background(), Opts{
		WorkloadID:  id,
		WorkloadDir: parent,
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      discardStderr(t),
	})
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", res.ExitCode)
	}
	if !errors.Is(res.Err, ErrWorkloadSpecInvalid) {
		t.Fatalf("err = %v, want ErrWorkloadSpecInvalid", res.Err)
	}
}

// TestReview_P1_OracleEscape_DotSlashRelative — the canonical
// `./oracle.sh` form is still accepted and resolves inside workloadRoot.
func TestReview_P1_OracleEscape_DotSlashRelative(t *testing.T) {
	root := t.TempDir()
	got, err := resolveOraclePath(root, "./oracle.sh")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Errorf("resolved path %q not under %q", got, root)
	}
}

// TestReview_P1_ParseOracleStdout_MetricsAlwaysJSON — JSON-parse failure
// used to leave MetricsRaw == "". Downstream SQL ingestion expects a
// valid JSON object literal; lock the "{}" default.
func TestReview_P1_ParseOracleStdout_MetricsAlwaysJSON(t *testing.T) {
	cases := [][]byte{
		[]byte("garbage not json\n"),
		[]byte(""),
		[]byte("{partially valid"),
	}
	for _, c := range cases {
		got := parseOracleStdout(c)
		if got.MetricsRaw != "{}" {
			t.Errorf("input %q → MetricsRaw=%q, want %q", c, got.MetricsRaw, "{}")
		}
	}
}

// TestReview_P1_CommitMetaFallback_OSPopulated — when commit_meta is
// unavailable the OS fields must not be empty strings; the CSV needs the
// columns populated with a sentinel (N/A) or the real arch.
func TestReview_P1_CommitMetaFallback_OSPopulated(t *testing.T) {
	root := findRepoModuleRoot(t)
	// Shim a failing commit_meta to force the fallback branch.
	t.Setenv("LOOM_EVAL_COMMIT_META_CMD", "exit 3")
	t.Setenv("LOOM_EVAL_GIT_EMAIL_CMD", gitEmailShim(t, "a@b|c@d"))

	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
		Stderr:      discardStderr(t),
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	rows := readCSV(t, outCSV)
	data := rows[1]
	// columns 13 / 14 / 15 (0-indexed) are os_kernel / os_distro / os_arch
	if data[13] == "" || data[14] == "" || data[15] == "" {
		t.Errorf("OS columns empty on commit_meta fallback: kernel=%q distro=%q arch=%q",
			data[13], data[14], data[15])
	}
}

// TestReview_P2_PreflightNoDoublePrefix — the operator-facing error
// message used to read `eval-runner: eval-runner: ...`; we now want
// exactly one prefix.
func TestReview_P2_PreflightNoDoublePrefix(t *testing.T) {
	var buf bytes.Buffer
	tmp, err := os.CreateTemp("", "stderr-")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer os.Remove(tmp.Name())
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: "tests/eval/workloads",
		StubListen:  "0.0.0.0:18080",
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      tmp,
	})
	tmp.Close()
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", res.ExitCode)
	}
	b, _ := os.ReadFile(tmp.Name())
	if bytes.Contains(b, []byte("eval-runner: eval-runner:")) {
		t.Errorf("double prefix leaked: %s", b)
	}
	if !bytes.Contains(b, []byte("eval-runner: --stub-listen")) {
		t.Errorf("expected single-prefix error message; got: %s", b)
	}
	_ = buf
}

// TestReview_P2_WhitelistEnv_LoomEmptySuffixRejected — `LOOM_=val` is no
// longer treated as a LOOM-namespaced var; only `LOOM_<non-empty>` flows.
func TestReview_P2_WhitelistEnv_LoomEmptySuffixRejected(t *testing.T) {
	parent := []string{
		"PATH=/x",
		"LOOM_=value-of-empty-suffix",
		"LOOM_REAL=ok",
	}
	out := WhitelistEnv(parent, "x", nil)
	got := strings.Join(out, "\n")
	if strings.Contains(got, "LOOM_=value-of-empty-suffix") {
		t.Errorf("empty-suffix LOOM_ slipped through: %s", got)
	}
	if !strings.Contains(got, "LOOM_REAL=ok") {
		t.Errorf("legitimate LOOM_ var dropped: %s", got)
	}
}

// TestReview_P2_ErrCSVExists_IsValueError — the sentinel is constructed
// with errors.New (matching the rest of the package); errors.Is still
// matches it.
func TestReview_P2_ErrCSVExists_IsValueError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.csv")
	if err := WriteCSVRow(path, RunRow{RunID: "a"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := WriteCSVRow(path, RunRow{RunID: "b"})
	if !errors.Is(err, ErrCSVExists) {
		t.Fatalf("errors.Is(err, ErrCSVExists) = false, err=%v", err)
	}
}

// TestReview_P2_CopyTree_SymlinkToDirRefused — a symlink that resolves
// (in-tree) to a directory used to silently produce a 0-byte file at the
// destination via copyFile. We refuse loudly now.
func TestReview_P2_CopyTree_SymlinkToDirRefused(t *testing.T) {
	src := t.TempDir()
	// Create an in-tree directory with a file inside.
	if err := os.MkdirAll(filepath.Join(src, "real_dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "real_dir", "f.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("real_dir", filepath.Join(src, "link_to_dir")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, err := SetupWorkspace(src, false)
	if !errors.Is(err, ErrFixtureSymlinkEscapes) {
		t.Fatalf("err = %v, want ErrFixtureSymlinkEscapes (symlink-to-dir variant)", err)
	}
}

// --- Round 2 review fixes ---

// TestReviewR2_P1_TestHelper_NoETXTBSY_UnderParallelLoad — the round-2 P1
// flake: under high parallel test load `writeScript` returned a path that
// was exec'd directly, racing the WriteFile's still-in-flight write fd
// across goroutines and producing `text file busy`. Switching `writeScript`
// to return `[]string{"/bin/sh", path}` makes /bin/sh open the script as
// data instead. Spawn 40 short subprocesses concurrently to prove the
// helper is now stable. The test is itself flaky-test bait — a regression
// here would surface as ETXTBSY just like the original.
func TestReviewR2_P1_TestHelper_NoETXTBSY_UnderParallelLoad(t *testing.T) {
	const N = 40
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			script := writeScript(t, "#!/bin/sh\nprintf hello\n")
			_, err := RunSubprocess(context.Background(), SubprocessOpts{
				Cmd:     script,
				Timeout: 5e9,
				Env:     []string{"PATH=/usr/bin:/bin"},
			})
			errs <- err
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Errorf("subprocess #%d: %v", i, err)
		}
	}
}

// TestReviewR2_P2_TimeoutExitCode_NotZero — pre-fix the timeout return
// path left ExitCode at Go zero, so CSV's oracle_exit_code column matched
// the success contract value (0) even though passed=false. Now it's -1.
func TestReviewR2_P2_TimeoutExitCode_NotZero(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nsleep 30\n")
	res, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     script,
		Timeout: 200 * 1e6, // 200ms
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if !errors.Is(err, ErrSubprocessTimeout) {
		t.Fatalf("err = %v, want ErrSubprocessTimeout", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0 on timeout; want a non-zero sentinel (e.g. -1) so CSV consumers can tell timeout apart from success")
	}
}

// TestReviewR2_P2_OverflowExitCode_NotZero — same reasoning for the
// stdout-overflow termination path.
func TestReviewR2_P2_OverflowExitCode_NotZero(t *testing.T) {
	script := writeScript(t, "#!/bin/sh\nyes x | head -c 2097152\n")
	res, err := RunSubprocess(context.Background(), SubprocessOpts{
		Cmd:     script,
		Timeout: 30 * 1e9,
		Env:     []string{"PATH=/usr/bin:/bin"},
	})
	if !errors.Is(err, ErrOracleOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOracleOutputTooLarge", err)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0 on overflow; want non-zero sentinel")
	}
}

// TestReviewR2_P2_CommitMeta_PartialJSON_MergesFallback — when commit_meta
// succeeds but omits some fields (a future commit_meta version or a
// hand-rolled shim), the runner must per-field merge against the fallback
// so CSV columns are never empty. Pre-fix only `MachineHostname` got the
// merge; OS triple and the four commit SHAs went out empty.
func TestReviewR2_P2_CommitMeta_PartialJSON_MergesFallback(t *testing.T) {
	root := findRepoModuleRoot(t)
	// commit_meta shim that returns valid JSON with ONLY loom_commit set;
	// everything else is missing and must fall back to N/A / runtime arch.
	partial := `{"loom_commit":"abc1234 (test clean)"}`
	t.Setenv("LOOM_EVAL_COMMIT_META_CMD", commitMetaShim(t, partial))
	t.Setenv("LOOM_EVAL_GIT_EMAIL_CMD", gitEmailShim(t, "a@b|c@d"))

	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
		Stderr:      discardStderr(t),
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	rows := readCSV(t, outCSV)
	data := rows[1]
	// columns: 9 loom_commit, 10 agentserver_commit, 11 modelserver_commit,
	// 12 app_commit, 13 os_kernel, 14 os_distro, 15 os_arch, 16 hostname.
	if data[9] != "abc1234 (test clean)" {
		t.Errorf("loom_commit = %q; want shim value", data[9])
	}
	for i, name := range map[int]string{
		10: "agentserver_commit",
		11: "modelserver_commit",
		12: "app_commit",
		13: "os_kernel",
		14: "os_distro",
		15: "os_arch",
	} {
		if data[i] == "" {
			t.Errorf("%s (col %d) empty; partial-JSON merge regressed", name, i)
		}
	}
}

// TestReviewR2_P2_OracleAbsolutePath_Rejected — a spec.yaml with an
// absolute `success_oracle` is rejected at pre-flight (exit 2) instead
// of being silently rewritten under the workload dir.
func TestReviewR2_P2_OracleAbsolutePath_Rejected(t *testing.T) {
	parent := t.TempDir()
	id := "abs-oracle-workload"
	dir := filepath.Join(parent, id)
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	spec := `id: abs-oracle-workload
description: tries an absolute oracle path
required_contexts: []
allowed_contexts: ["*"]
inputs:
  read_artifacts: []
outputs:
  write_targets: []
success_oracle: /etc/passwd
recovery_hint: x
timeout_seconds: 60
`
	if err := os.WriteFile(filepath.Join(dir, "spec.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	res := Run(context.Background(), Opts{
		WorkloadID:  id,
		WorkloadDir: parent,
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      discardStderr(t),
	})
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", res.ExitCode)
	}
	if !errors.Is(res.Err, ErrWorkloadSpecInvalid) {
		t.Fatalf("err = %v, want ErrWorkloadSpecInvalid", res.Err)
	}
}

// TestReview_P2_GitEmailShim_MalformedWarns — a shim missing the `|`
// separator now warns to stderr and returns empty strings rather than
// passing the raw line to redact.
func TestReview_P2_GitEmailShim_MalformedWarns(t *testing.T) {
	tmp, err := os.CreateTemp("", "stderr-")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer os.Remove(tmp.Name())

	env := []string{"PATH=/usr/bin:/bin", "LOOM_EVAL_GIT_EMAIL_CMD=printf 'just-one-token-no-pipe'"}
	author, committer := collectGitEmails(context.Background(), Opts{}, env, tmp)
	tmp.Close()
	if author != "" || committer != "" {
		t.Errorf("got (%q, %q), want both empty", author, committer)
	}
	b, _ := os.ReadFile(tmp.Name())
	if !bytes.Contains(b, []byte("git-email output missing `|`")) {
		t.Errorf("missing warn line; stderr=%s", b)
	}
}

// --- Round 3 review fixes ---

// TestReviewR3_P2_OracleDotResolvesToRoot_Rejected — `success_oracle: "."`
// resolves to the workload directory itself; pre-fix the runner would
// try to exec that path and fail with `is a directory`. Now resolveOraclePath
// catches it.
func TestReviewR3_P2_OracleDotResolvesToRoot_Rejected(t *testing.T) {
	root := t.TempDir()
	for _, in := range []string{".", "./", "./."} {
		_, err := resolveOraclePath(root, in)
		if err == nil {
			t.Errorf("resolveOraclePath(%q, %q) = nil; want error", root, in)
		}
	}
}

// TestReviewR3_P2_OracleWhitespace_Rejected — a single-space or tab-only
// value used to slip past the `== ""` empty guard and resolve to
// `<root>/ `; now trimmed first.
func TestReviewR3_P2_OracleWhitespace_Rejected(t *testing.T) {
	root := t.TempDir()
	for _, in := range []string{" ", "\t", "  \t  ", ""} {
		_, err := resolveOraclePath(root, in)
		if err == nil {
			t.Errorf("resolveOraclePath(%q, %q) = nil; want error", root, in)
		}
	}
}

// TestReviewR3_P2_OracleCanonicalForm_StillAccepted — defensive: the
// canonical `./oracle.sh` form still resolves under workloadRoot after
// the new dot/whitespace rejects.
func TestReviewR3_P2_OracleCanonicalForm_StillAccepted(t *testing.T) {
	root := t.TempDir()
	got, err := resolveOraclePath(root, "./oracle.sh")
	if err != nil {
		t.Fatalf("canonical form rejected: %v", err)
	}
	if got != filepath.Join(root, "oracle.sh") {
		t.Errorf("got %q, want %q", got, filepath.Join(root, "oracle.sh"))
	}
}

// TestReviewR3_P2_OnTempdirPanic_StillCleansTempdir — a test hook that
// panics inside OnTempdir used to leak ws.Root because Cleanup was an
// outer statement. With cleanup now an inner defer, the tempdir is
// removed even on panic.
func TestReviewR3_P2_OnTempdirPanic_StillCleansTempdir(t *testing.T) {
	root := findRepoModuleRoot(t)
	withShims(t, commitMetaJSON(), "a@b|c@d")

	var capturedPath string
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected OnTempdir panic to propagate")
		}
		if capturedPath == "" {
			t.Fatal("OnTempdir never ran; test is broken")
		}
		if _, err := os.Stat(capturedPath); err == nil {
			t.Errorf("tempdir %s leaked after OnTempdir panic", capturedPath)
		}
	}()

	_ = Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      filepath.Join(t.TempDir(), "run.csv"),
		Stderr:      discardStderr(t),
		OnTempdir: func(p string) {
			capturedPath = p
			panic("test-induced panic")
		},
	})
}

// --- Round 4 review fixes ---

// TestReviewR4_P2_DurationCoversFullRun — spec §5 pins started_at_unix
// to "runner wall clock at step 1" and finished_at_unix to "step 17".
// Pre-fix, `startedAt` was captured at step 10 (agent stage) and
// `finishedAt` right after oracle stdout parse; commit_meta collection
// was OUTSIDE the duration_ms window and downstream aggregators
// systematically under-reported wall time.
//
// The test shims commit_meta with a 250ms sleep and asserts
// duration_ms >= 200ms — proof that finishedAt is captured after
// commit_meta rather than before it.
func TestReviewR4_P2_DurationCoversFullRun(t *testing.T) {
	root := findRepoModuleRoot(t)
	// Shim commit_meta to sleep, then print a valid JSON blob.
	shim := filepath.Join(t.TempDir(), "slow-shim.sh")
	body := "#!/bin/sh\nsleep 0.25\ncat <<'EOF'\n" + commitMetaJSON() + "\nEOF\n"
	if err := os.WriteFile(shim, []byte(body), 0o700); err != nil {
		t.Fatalf("write shim: %v", err)
	}
	t.Setenv("LOOM_EVAL_COMMIT_META_CMD", "/bin/sh "+shim)
	t.Setenv("LOOM_EVAL_GIT_EMAIL_CMD", gitEmailShim(t, "a@b|c@d"))

	outCSV := filepath.Join(t.TempDir(), "run.csv")
	res := Run(context.Background(), Opts{
		WorkloadID:  "cross-device-code-mod",
		WorkloadDir: filepath.Join(root, "tests/eval/workloads"),
		StubListen:  pickFreePort(t),
		StubBin:     stubBinaryPath(t),
		OutCSV:      outCSV,
		Stderr:      discardStderr(t),
	})
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if res.Row.DurationMs < 200 {
		t.Errorf("DurationMs = %d, want >= 200 (spec §5: finished_at includes commit_meta)", res.Row.DurationMs)
	}
	if res.Row.FinishedAtUnix < res.Row.StartedAtUnix {
		t.Fatalf("finished (%d) before started (%d)", res.Row.FinishedAtUnix, res.Row.StartedAtUnix)
	}
}

// TestReviewR3_P2_LockedWriter_NoInterleave — concurrent Write calls from
// two goroutines through the lockedWriter must produce well-formed
// records, never byte-level torn output. Smoke at 1000 writes/goroutine
// across 4 goroutines.
func TestReviewR3_P2_LockedWriter_NoInterleave(t *testing.T) {
	var buf bytes.Buffer
	lw := &lockedWriter{w: &buf}
	const (
		gs       = 4
		perG     = 1000
		recordsz = 80
	)
	done := make(chan struct{}, gs)
	payload := make([]byte, recordsz)
	for i := range payload {
		payload[i] = 'A' + byte(i%26)
	}
	payload[recordsz-1] = '\n'
	for g := 0; g < gs; g++ {
		go func() {
			for i := 0; i < perG; i++ {
				_, _ = lw.Write(payload)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < gs; i++ {
		<-done
	}
	if got := buf.Len(); got != gs*perG*recordsz {
		t.Errorf("byte count = %d, want %d", got, gs*perG*recordsz)
	}
	// Each newline must be at a record boundary.
	for i, c := range buf.Bytes() {
		isNL := c == '\n'
		atBoundary := (i+1)%recordsz == 0
		if isNL != atBoundary {
			t.Fatalf("torn record at offset %d", i)
		}
	}
}
