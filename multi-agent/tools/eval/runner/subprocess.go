package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// MaxOracleStdoutBytes caps the bytes the runner is willing to read from an
// oracle subprocess. Security §7(f): protects the runner from OOM if an
// oracle pipes gigabytes; the cap is also the boundary at which the run is
// failed pre-flight rather than reported as a normal oracle decision.
const MaxOracleStdoutBytes = 1 << 20 // 1 MiB

// ErrOracleOutputTooLarge is returned by RunSubprocess when stdout exceeded
// MaxOracleStdoutBytes before the process exited or closed stdout. The
// subprocess group has been killed by the time this surfaces.
var ErrOracleOutputTooLarge = errors.New("eval-runner: oracle stdout exceeded 1 MiB cap")

// ErrSubprocessTimeout is returned when the subprocess (or any child in its
// process group, on Linux) failed to exit before the requested deadline.
// The full group has been signalled by the time this surfaces.
var ErrSubprocessTimeout = errors.New("eval-runner: subprocess group killed on timeout")

// SubprocessOpts configures one subprocess invocation. The runner uses this
// for oracle.sh; future agent-stage subprocesses go through the same plumbing.
type SubprocessOpts struct {
	// Cmd is the program to run plus argv. Cmd[0] is resolved via PATH.
	Cmd []string

	// Cwd is the working directory. Empty means "inherit", but the runner
	// always sets this to the tempdir — see Security §7(b).
	Cwd string

	// Env is the *complete* environment for the child. The caller is
	// expected to have routed it through WhitelistEnv — RunSubprocess
	// does not silently merge with os.Environ().
	Env []string

	// Timeout is the maximum wall time the subprocess may take. Zero means
	// "no timeout"; pass a real duration in production callers.
	Timeout time.Duration

	// MaxStdoutBytes caps stdout. Zero means MaxOracleStdoutBytes. When
	// the cap is hit, the process group is killed and the call returns
	// ErrOracleOutputTooLarge.
	MaxStdoutBytes int
}

// SubprocessResult is what RunSubprocess returns on a clean run (i.e. the
// process exited inside the deadline and did not over-spew stdout). The
// caller decides whether ExitCode is failure.
type SubprocessResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	PID      int // recorded for post-mortem "did we leak any children?" tests
}

// RunSubprocess executes opts.Cmd under the runner's safety policy:
//   - explicit env (no parent leakage; Security §7(a))
//   - own process group on Linux (Security §7(a) lifecycle)
//   - bounded stdout (Security §7(f))
//   - timeout-kills the full group
//
// On Linux the platform-specific setupProcGroup() sets SysProcAttr.Setpgid so
// killGroup(-pid) signals every descendant; on other platforms it degrades
// to a leaf-only kill (documented in spec §6 and plan risk section).
func RunSubprocess(ctx context.Context, opts SubprocessOpts) (SubprocessResult, error) {
	if len(opts.Cmd) == 0 {
		return SubprocessResult{}, errors.New("eval-runner: subprocess Cmd is empty")
	}
	max := opts.MaxStdoutBytes
	if max <= 0 {
		max = MaxOracleStdoutBytes
	}

	cmd := exec.CommandContext(ctx, opts.Cmd[0], opts.Cmd[1:]...)
	cmd.Dir = opts.Cwd
	cmd.Env = append([]string{}, opts.Env...) // defensive copy
	setupProcGroup(cmd)

	// Read stdout through a LimitReader-equivalent capped at max+1. If we
	// see max+1 bytes, the child has tripped Security §7(f); kill the
	// group and surface ErrOracleOutputTooLarge.
	stdoutBuf := &bytes.Buffer{}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return SubprocessResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return SubprocessResult{}, fmt.Errorf("start %s: %w", opts.Cmd[0], err)
	}
	pid := cmd.Process.Pid

	// Drain stdout in a goroutine; signal back when (a) we hit the cap,
	// (b) the pipe closes naturally, or (c) we hit a read error.
	type drainResult struct {
		overflow bool
		err      error
	}
	drainCh := make(chan drainResult, 1)
	go func() {
		// CopyN(max+1) lets us discover whether the producer wrote
		// strictly more than max bytes (overflow == true).
		n, err := io.CopyN(stdoutBuf, stdoutPipe, int64(max)+1)
		if err != nil && err != io.EOF {
			drainCh <- drainResult{overflow: false, err: err}
			return
		}
		drainCh <- drainResult{overflow: n > int64(max), err: nil}
	}()

	// Set up timeout. We don't rely on exec.CommandContext alone because
	// its kill signals only the leaf — we want the whole group.
	var timeoutCh <-chan time.Time
	if opts.Timeout > 0 {
		t := time.NewTimer(opts.Timeout)
		defer t.Stop()
		timeoutCh = t.C
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// Three terminal events: overflow → kill+drain+wait; timeout →
	// kill+drain+wait; natural exit → wait for drain to finish then return.
	// We always wait for `drainCh` before reading stdoutBuf because the
	// drain goroutine writes to it concurrently — touching the buffer
	// before drain returns is a data race.
	for {
		select {
		case d := <-drainCh:
			drainCh = nil
			if d.overflow {
				killGroup(pid)
				<-waitCh
				return SubprocessResult{PID: pid}, ErrOracleOutputTooLarge
			}
			if d.err != nil {
				killGroup(pid)
				<-waitCh
				return SubprocessResult{PID: pid}, fmt.Errorf("oracle stdout drain: %w", d.err)
			}
			// Stdout closed; wait for cmd to exit.
		case <-timeoutCh:
			timeoutCh = nil
			killGroup(pid)
			// Drain may still be live — block on it before reading.
			if drainCh != nil {
				<-drainCh
				drainCh = nil
			}
			<-waitCh
			return SubprocessResult{
				Stdout: stdoutBuf.Bytes(),
				Stderr: stderrBuf.Bytes(),
				PID:    pid,
			}, ErrSubprocessTimeout
		case waitErr := <-waitCh:
			// Cmd exited — drain may still be live until the pipe
			// flushes its tail; block.
			if drainCh != nil {
				<-drainCh
			}
			return finalize(stdoutBuf, stderrBuf, pid, waitErr), nil
		}
		// Loop only continues after a clean drainCh; the next iteration
		// waits on (timeout | waitCh) without re-arming drain.
		if drainCh == nil && timeoutCh == nil {
			// Both fired or neither set; block on wait.
			waitErr := <-waitCh
			return finalize(stdoutBuf, stderrBuf, pid, waitErr), nil
		}
	}
}

func finalize(stdoutBuf, stderrBuf *bytes.Buffer, pid int, waitErr error) SubprocessResult {
	res := SubprocessResult{
		Stdout: stdoutBuf.Bytes(),
		Stderr: stderrBuf.Bytes(),
		PID:    pid,
	}
	if waitErr == nil {
		res.ExitCode = 0
		return res
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res
	}
	// Non-exit error (start failure, pipe failure) — record as -1; caller
	// distinguishes via a sentinel.
	res.ExitCode = -1
	return res
}

// AlwaysAllowedEnvKeys are the keys propagated from the parent env to every
// subprocess unconditionally. Anything not listed here is dropped.
//
// Security §7(a): keep this list tight. Adding a key here is the same as
// inviting that key into every oracle's environment forever; if a workload
// needs a new variable, prefer the per-workload allowlist below.
var AlwaysAllowedEnvKeys = []string{
	"PATH",
	"HOME",
	"LANG",
	"LC_ALL",
	"TZ",
	"USER",
}

// AlwaysAllowedIfSetEnvKeys are propagated only when present in the parent
// env. They configure commit_meta and its sibling collectors — passing them
// through lets the eval host point at non-default repo roots without rebuild.
var AlwaysAllowedIfSetEnvKeys = []string{
	"AGENTSERVER_ROOT",
	"MODELSERVER_ROOT",
	"APP_ROOT",
	"MOCK_MODEL_URL",
}

// PerWorkloadAllowedEnvKeys is the per-workload allow-list. Adding a row
// requires a code edit here — a workload's spec.yaml cannot expand the
// allow-list at runtime. See Security §7(a).
var PerWorkloadAllowedEnvKeys = map[string][]string{
	"credential-bound-model": {"EXPECTED_MODEL_ALIAS"},
}

// WhitelistEnv returns the env slice for a subprocess, given the parent's
// env (as os.Environ()-style "k=v"), the workload id (so the per-workload
// allow-list applies), and an extra map the runner injects (e.g.
// AGENTSERVER_URL pointing at the stub the runner just spawned).
//
// LOOM_* keys are also propagated unconditionally — they're the test seam
// and per-team config namespace called out in plan.md.
func WhitelistEnv(parent []string, workloadID string, injected map[string]string) []string {
	wanted := map[string]struct{}{}
	for _, k := range AlwaysAllowedEnvKeys {
		wanted[k] = struct{}{}
	}
	for _, k := range AlwaysAllowedIfSetEnvKeys {
		wanted[k] = struct{}{}
	}
	for _, k := range PerWorkloadAllowedEnvKeys[workloadID] {
		wanted[k] = struct{}{}
	}

	out := make([]string, 0, len(wanted)+len(injected))
	seen := map[string]bool{}
	for _, kv := range parent {
		k, _, ok := splitEnv(kv)
		if !ok {
			continue
		}
		// LOOM_* passes a prefix check rather than an exact-match entry
		// in `wanted`; this is the per-team config + test-seam namespace.
		_, named := wanted[k]
		if !named && !strings.HasPrefix(k, "LOOM_") {
			continue
		}
		out = append(out, kv)
		seen[k] = true
	}
	for k, v := range injected {
		if seen[k] {
			// Caller-supplied injection wins over parent inheritance.
			// Rewrite the existing entry rather than duplicate.
			for i, kv := range out {
				if pk, _, ok := splitEnv(kv); ok && pk == k {
					out[i] = k + "=" + v
				}
			}
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func splitEnv(kv string) (string, string, bool) {
	i := strings.IndexByte(kv, '=')
	if i <= 0 {
		return "", "", false
	}
	return kv[:i], kv[i+1:], true
}
