package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	// Assign stdout/stderr to bounded writers and let exec.Cmd own the
	// copy goroutines. This avoids the StdoutPipe()+Wait() race where
	// Wait closes the pipe fd while a user-side drain goroutine is still
	// inside io.CopyN — that race surfaced as
	// `oracle stdout drain: read |0: file already closed` on healthy
	// fast-exit oracles (PR #53 review P0).
	stdoutWriter := newBoundedWriter(max)
	stderrBuf := &bytes.Buffer{}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return SubprocessResult{}, fmt.Errorf("start %s: %w", opts.Cmd[0], err)
	}
	pid := cmd.Process.Pid

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

	// Two terminal events:
	//   * timeout fires → kill the group, wait for cmd.Wait to reap (it
	//     also drains stdout/stderr because exec owns those goroutines)
	//   * cmd exits → check whether the bounded writer tripped overflow
	//     during the run, otherwise return the normal result.
	select {
	case <-timeoutCh:
		killGroup(pid)
		<-waitCh
		// Use -1 for the exit code so downstream CSV consumers can
		// tell "timed out" apart from "exit 0 (passed)". The spec
		// §1.3 oracle contract only pins exit codes 0/1/2; we own
		// the value for the timeout branch and `finalize()` already
		// uses -1 for "started but no exit code" — re-use the same
		// sentinel (PR #53 round 2 P2).
		return SubprocessResult{
			Stdout:   stdoutWriter.bytes(),
			Stderr:   stderrBuf.Bytes(),
			ExitCode: -1,
			PID:      pid,
		}, ErrSubprocessTimeout
	case waitErr := <-waitCh:
		if stdoutWriter.overflowed() {
			// The bounded writer returned errStdoutOverflow on the
			// max+1 byte; exec.Cmd surfaces that to Wait() as the
			// non-nil err. We still kill the group defensively in
			// case a sibling process in the group lingered, then
			// return the security-§7(f) sentinel with ExitCode=-1
			// so the CSV path (when called) won't read it as a
			// real oracle status.
			killGroup(pid)
			return SubprocessResult{PID: pid, ExitCode: -1}, ErrOracleOutputTooLarge
		}
		return finalize(stdoutWriter.buffer(), stderrBuf, pid, waitErr), nil
	}
}

// boundedWriter is an io.Writer that accepts at most `max` bytes; the
// (max+1)-th byte triggers errStdoutOverflow which exec.Cmd's stdout
// goroutine propagates back to Wait().  The writer is goroutine-safe with
// respect to a single producer (exec spawns one stdout-copier per Cmd),
// which is the only writer.  Readers go through bytes()/buffer() AFTER
// Wait() returns — by then the producer is done.
type boundedWriter struct {
	buf      *bytes.Buffer
	max      int
	overflow bool
}

func newBoundedWriter(max int) *boundedWriter {
	return &boundedWriter{buf: &bytes.Buffer{}, max: max}
}

// errStdoutOverflow is the sentinel the bounded writer returns to exec's
// stdout-copy goroutine when the cap is exceeded. Wait() propagates it
// via *exec.ExitError; RunSubprocess maps it to ErrOracleOutputTooLarge.
var errStdoutOverflow = errors.New("eval-runner: bounded stdout writer overflowed")

func (b *boundedWriter) Write(p []byte) (int, error) {
	remaining := b.max - b.buf.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(p) <= remaining {
		return b.buf.Write(p)
	}
	// Write the part that fits, then trip overflow on the next byte.
	if remaining > 0 {
		_, _ = b.buf.Write(p[:remaining])
	}
	b.overflow = true
	// Report short write + error so exec stops copying.
	return remaining, errStdoutOverflow
}

func (b *boundedWriter) overflowed() bool { return b.overflow }
func (b *boundedWriter) bytes() []byte    { return b.buf.Bytes() }
func (b *boundedWriter) buffer() *bytes.Buffer {
	return b.buf
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
		// Require at least one character after the prefix so a stray
		// `LOOM_=val` from the parent env doesn't slip through (PR #53
		// review P2).
		_, named := wanted[k]
		isLoomNS := len(k) > len("LOOM_") && strings.HasPrefix(k, "LOOM_")
		if !named && !isLoomNS {
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
