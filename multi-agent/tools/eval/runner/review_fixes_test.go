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
			Cmd:     []string{script},
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
