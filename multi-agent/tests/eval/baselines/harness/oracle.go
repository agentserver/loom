package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

// MaxOracleStdoutBytes is the 1 MiB cap on oracle stdout. Runners under
// tools/eval/runner already ship this contract (their spec §7(f)); the
// baseline harness mirrors it here so the failure mode ("oracle spews
// gigabytes → we OOM") is closed off in both places. Re-implementing
// rather than importing keeps the two packages independent per spec §6.
const MaxOracleStdoutBytes = 1 << 20

// ErrOracleOutputTooLarge is returned when the oracle subprocess prints
// more than MaxOracleStdoutBytes to stdout before exiting. The process
// is killed before this surfaces.
var ErrOracleOutputTooLarge = errors.New("harness: oracle stdout exceeded 1 MiB cap")

// ErrOracleStdoutNotJSON is returned when the oracle's first stdout
// line is not a parseable JSON object matching {passed, details, metrics}.
// The run is marked failed rather than escalated to a pre-flight abort
// (the runner spec's precedent: a bad-JSON oracle is a workload bug,
// not a runner bug).
var ErrOracleStdoutNotJSON = errors.New("harness: oracle stdout first line not JSON {passed,details,metrics}")

// ErrOracleStdoutHasExtraLines is returned when the oracle prints any
// second line at all — blank or not — after the first line's terminator.
// 13 号 §1.3 Forbidden: "不准向 stdout 多打任何额外行". The single
// trailing `"\n"` that terminates the first line is the only permitted
// post-content byte; a second `"\n"` (or anything else) is a contract
// violation. Run marked failed (passed=false, exit 1).
var ErrOracleStdoutHasExtraLines = errors.New("harness: oracle stdout has more than one line (13 号 §1.3)")

// OracleResult is the parsed shape of the oracle's first stdout line
// plus the process metadata the caller needs to fill in a run row.
type OracleResult struct {
	Passed      bool
	ExitCode    int
	DetailsJSON string
	MetricsJSON string
	// RawStdout is the full captured stdout up to the cap; kept for
	// diagnostics only and never persisted.
	RawStdout []byte
	Stderr    []byte
}

// oracleWire is the JSON shape 13 号 §1.3 requires from the first
// stdout line. Extra keys are ignored so oracles can add debugging
// fields without breaking the runner.
type oracleWire struct {
	Passed  *bool           `json:"passed"`
	Details json.RawMessage `json:"details"`
	Metrics json.RawMessage `json:"metrics"`
}

// RunOracle invokes `oracleScript` with `workspace` as its $1 argument,
// under the caller-supplied `env` and `timeout`. Stdout is capped at
// 1 MiB; the first newline-terminated line is JSON-parsed per the
// contract in 13 号 §1.3.
//
// On success (parse OK, whether pass or fail) returns OracleResult with
// Err = nil; the caller decides how to translate Passed/ExitCode to a
// run row + exit code.
//
// On pre-flight faults (oversized stdout, invalid JSON) returns the
// error to the caller; the caller decides whether to abort or mark the
// row as failed. The runner-spec convention (§7(f)) is:
//   - oversized stdout → runner exit 2, no CSV written
//   - malformed JSON  → run row emitted with passed=false, exit 1
func RunOracle(ctx context.Context, oracleScript, workspace string, env []string, timeout time.Duration) (OracleResult, error) {
	if oracleScript == "" {
		return OracleResult{}, errors.New("harness: oracleScript path is empty")
	}
	if !filepath.IsAbs(oracleScript) {
		// spec.success_oracle is by convention relative to the workload
		// dir; caller must have resolved it before now.
		return OracleResult{}, fmt.Errorf("harness: oracleScript must be absolute; got %q", oracleScript)
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	// Own our own context so a caller ctx cancel triggers process kill.
	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(subCtx, oracleScript, workspace)
	cmd.Env = append([]string{}, env...)
	cmd.Dir = workspace

	stdoutW := newBoundedBuffer(MaxOracleStdoutBytes)
	stderrBuf := &bytes.Buffer{}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrBuf

	err := cmd.Run()
	if stdoutW.overflowed {
		// SIGKILL the process group implicitly via CommandContext cancel
		// (no group setup here — baseline oracles are simple shell
		// scripts, and CommandContext already kills the leaf on ctx
		// deadline / cancel).
		cancel()
		return OracleResult{Stderr: stderrBuf.Bytes()}, ErrOracleOutputTooLarge
	}
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return OracleResult{Stderr: stderrBuf.Bytes()}, fmt.Errorf("harness: oracle exec: %w", err)
		}
	}

	res := OracleResult{
		ExitCode:  exitCode,
		RawStdout: stdoutW.Bytes(),
		Stderr:    stderrBuf.Bytes(),
	}

	// Parse first newline-terminated line as JSON.
	line, rest := firstLine(res.RawStdout)
	var wire oracleWire
	if err := json.Unmarshal(line, &wire); err != nil || wire.Passed == nil {
		return res, ErrOracleStdoutNotJSON
	}
	res.Passed = *wire.Passed
	res.DetailsJSON = string(wire.Details)
	res.MetricsJSON = string(wire.Metrics)
	if res.DetailsJSON == "" {
		res.DetailsJSON = "{}"
	}
	if res.MetricsJSON == "" {
		res.MetricsJSON = "{}"
	}
	// 13 号 §1.3 exactly-one-line enforcement. `rest` is everything the
	// oracle emitted after the first line's terminator; anything
	// non-empty (even a bare "\n" of a second blank line) violates the
	// contract. The parsed shape is retained so the caller can still
	// see the intended verdict, but the returned error signals failure.
	if len(rest) > 0 {
		return res, ErrOracleStdoutHasExtraLines
	}
	return res, nil
}

// firstLine returns (line, rest) where line is everything up to (but not
// including) the first '\n', and rest is everything after that '\n'. If
// stdout ends with a single '\n' and nothing follows, rest is empty
// (that's the permitted case). If there is no '\n' at all, line = b and
// rest is empty.
func firstLine(b []byte) ([]byte, []byte) {
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return b, nil
	}
	return b[:i], b[i+1:]
}

// boundedBuffer wraps bytes.Buffer with a hard byte cap. When a write
// would exceed the cap the buffer records `overflowed=true` and rejects
// subsequent writes so the underlying subprocess sees a broken pipe on
// its next stdout flush (which cmd.Run() maps to process termination).
type boundedBuffer struct {
	buf        bytes.Buffer
	cap        int
	overflowed bool
}

func newBoundedBuffer(cap int) *boundedBuffer {
	return &boundedBuffer{cap: cap}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.overflowed {
		return 0, io.ErrShortWrite
	}
	if b.buf.Len()+len(p) > b.cap {
		// Absorb up to the cap so the first-line parser can still see
		// what the oracle managed to emit; then flip the flag.
		room := b.cap - b.buf.Len()
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.overflowed = true
		return 0, io.ErrShortWrite
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }
