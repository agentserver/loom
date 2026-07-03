package main

// Codex config dual-path validation (spec: WT-2-credential-workload §7).
//
// Two runtime paths against which the workload can be exercised:
//   mode "a" — local proxy holds the credential; the codex config uses
//              `experimental_bearer_token = "..."`.
//   mode "b" — upstream direct; the codex config uses
//              `env_key = "OPENAI_API_KEY"` and reads the raw key from
//              the process environment.
//
// The runner reads the config from disk (never embeds it) and asserts
// the auth-field shape matches the declared mode before spawning any
// subprocess. This defends against the failure mode documented in
// prod_test/E2E_RUNBOOK.md line 266: `env_key` against the local proxy
// silently sends the wrong bearer, producing a plausible-but-fake
// latency measurement that would poison ModelProxyOverhead.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Sentinel errors surfaced as exit 2 by the pre-flight validator.
// Callers test with errors.Is; error strings are not part of the API.
var (
	ErrCodexConfigMismatch     = errors.New("eval-runner: codex config auth-field does not match --codex-config-mode")
	ErrCodexConfigPathForbidden = errors.New("eval-runner: --codex-config-path resolves outside the allowed roots (repo or /tmp)")
	ErrCodexConfigModeInvalid   = errors.New("eval-runner: --codex-config-mode must be \"a\" or \"b\"")
	ErrOpenAIKeyMissing         = errors.New("eval-runner: mode=b requires a non-sentinel OPENAI_API_KEY in the environment")
	ErrCodexConfigUnreadable    = errors.New("eval-runner: --codex-config-path could not be read")
)

// authFieldLine matches a bare `experimental_bearer_token = ...` or
// `env_key = ...` at the start of a line (ignoring leading whitespace).
// The value side is deliberately ignored — we assert the shape of the
// KEY only, and we never log the value.
var authFieldLine = regexp.MustCompile(`^[[:space:]]*(experimental_bearer_token|env_key)[[:space:]]*=`)

// codexConfigInspection is the result of scanning a config file.
type codexConfigInspection struct {
	HasBearer bool // `experimental_bearer_token` field seen
	HasEnvKey bool // `env_key` field seen
}

// inspectCodexConfig line-scans path and reports which of the two auth
// fields are present. Comments (`#` to EOL) are stripped before the
// regex match, so `foo = "bar"  # note` still matches. Multi-line
// TOML strings are not handled — the two fields we care about are
// single-line by operator convention.
//
// The function reads at most 1 MiB from the file — codex configs are
// small and a hostile "config" much larger than that is an attack.
// Read errors are wrapped with ErrCodexConfigUnreadable; the wrapped
// message includes only the path, never the file contents (spec §7.c).
func inspectCodexConfig(path string) (codexConfigInspection, error) {
	var out codexConfigInspection
	f, err := os.Open(path)
	if err != nil {
		return out, fmt.Errorf("%w: %s: %v", ErrCodexConfigUnreadable, path, err)
	}
	defer f.Close()

	// 1 MiB cap; codex config in prod is <2 KiB.
	limited := &limitedReader{R: f, N: 1 << 20}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64<<10), 512<<10)
	for scanner.Scan() {
		line := scanner.Text()
		// Strip inline comments — everything from the first `#`.
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		m := authFieldLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "experimental_bearer_token":
			out.HasBearer = true
		case "env_key":
			out.HasEnvKey = true
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("%w: %s: %v", ErrCodexConfigUnreadable, path, err)
	}
	if limited.Overflow {
		return out, fmt.Errorf("%w: %s: exceeded 1 MiB size cap", ErrCodexConfigUnreadable, path)
	}
	return out, nil
}

// limitedReader wraps a Reader with a hard byte cap. Setting Overflow
// tells the caller the cap was hit — distinguishing "clean EOF" from
// "we chopped the file".
type limitedReader struct {
	R        interface{ Read(p []byte) (int, error) }
	N        int64
	Overflow bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.N <= 0 {
		l.Overflow = true
		return 0, nil // signal EOF; caller sees a truncated file
	}
	if int64(len(p)) > l.N {
		p = p[:l.N]
	}
	n, err := l.R.Read(p)
	l.N -= int64(n)
	return n, err
}

// sentinelOpenAIKeys — obviously-fake OPENAI_API_KEY values that would
// let mode=b run without a real credential and produce a wholly
// fabricated latency measurement. Rejected pre-flight (spec §7.d).
//
// The list is deliberately small. It is NOT a leak scanner and is not
// meant to catch all bad values; it catches the ones that operators
// have historically pasted in during development.
var sentinelOpenAIKeys = map[string]struct{}{
	"":         {},
	"test-key": {},
	"sk-fake":  {},
	"changeme": {},
	"x":        {},
}

// sentinelOpenAIKeyPattern additionally rejects `sk-XXXX…` shapes with
// 6+ consecutive `X` characters after `sk-`. These are the placeholder
// keys the openai docs use.
var sentinelOpenAIKeyPattern = regexp.MustCompile(`^sk-X{6,}$`)

// isSentinelOpenAIKey reports whether v is a known-fake placeholder.
func isSentinelOpenAIKey(v string) bool {
	if _, ok := sentinelOpenAIKeys[v]; ok {
		return true
	}
	return sentinelOpenAIKeyPattern.MatchString(v)
}

// codexConfigInputs is the subset of Opts consumed by the validator.
// Passing it explicitly (rather than *Opts) keeps the validator
// unit-testable without constructing a full Opts.
type codexConfigInputs struct {
	Path    string // --codex-config-path
	Mode    string // --codex-config-mode ("a" | "b" | "")
	RepoRoot string
	// GetEnv indirects os.Getenv so tests can inject sentinel values
	// without setenv contamination across parallel test cases.
	GetEnv func(string) string
}

// validateCodexConfig is the pre-flight assertion invoked from
// runner.go alongside validateStubListen / validateObserverDB.
//
// Behaviour matrix (spec §2.2, §7.a):
//   - Both flags empty      → no assertions run, returns nil (PR #53
//                             callers see no behaviour change).
//   - Mode set, path empty  → returns nil for now; the resolution of
//                             --codex-config-mode → path is a caller
//                             concern (the harness resolves it before
//                             invoking the runner). The env-var
//                             precondition below still runs when mode=b.
//   - Path set, mode empty  → path sandbox check runs; auth-field
//                             mismatch check runs against whichever
//                             field is present (both/neither reject).
//   - Both set              → full check; path wins on the actual
//                             read, mode drives the field-match rule
//                             AND the env-var precondition.
//
// GetEnv indirection: tests use it to avoid os.Setenv, which is
// racy in parallel tests.
func validateCodexConfig(in codexConfigInputs) error {
	if in.GetEnv == nil {
		in.GetEnv = os.Getenv
	}

	// Mode charset check runs first — even when path is empty, an
	// invalid mode is user error worth surfacing.
	if in.Mode != "" && in.Mode != "a" && in.Mode != "b" {
		return fmt.Errorf("%w: got %q", ErrCodexConfigModeInvalid, in.Mode)
	}

	// Env-var precondition for mode=b applies whether or not the
	// config path was passed — the operator is asserting they intend
	// to run against upstream direct, and the raw key must exist.
	if in.Mode == "b" {
		v := in.GetEnv("OPENAI_API_KEY")
		if isSentinelOpenAIKey(v) {
			// NB: we deliberately do NOT include the observed value
			// in the error — even a sentinel value could turn out
			// to be a real key rotated by an unwitting operator.
			return fmt.Errorf("%w: check OPENAI_API_KEY is set to a real credential", ErrOpenAIKeyMissing)
		}
	}

	// Neither flag → nothing else to check.
	if in.Path == "" && in.Mode == "" {
		return nil
	}
	if in.Path == "" {
		// Mode was set but no path — nothing to inspect. The harness
		// is expected to resolve mode → path before invoking the
		// runner; if it didn't, that's the harness's bug, not ours.
		return nil
	}

	// Path sandbox — spec §7.b.
	//
	// The sandbox check runs in TWO stages:
	//   1. The literal path passed on the CLI is Clean/Abs-resolved and
	//      confined to repo || /tmp || /var/tmp.
	//   2. filepath.EvalSymlinks resolves any symlinks along the path,
	//      and the RESULT is confined to the same allowlist. Without
	//      this second stage, an attacker can drop `/tmp/cfg` as a
	//      symlink to `/etc/shadow` (or `/proc/self/environ`, etc.) and
	//      the runner would happily open it — the lexical check would
	//      not know the difference.
	// EvalSymlinks fails for a non-existent path; we treat that as a
	// read error (surfaced by the subsequent os.Open) and only enforce
	// the second-stage confinement when EvalSymlinks succeeds.
	abs, err := filepath.Abs(filepath.Clean(in.Path))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCodexConfigPathForbidden, err)
	}
	repoRoot := in.RepoRoot
	if repoRoot != "" {
		if repoAbs, err := filepath.Abs(repoRoot); err == nil {
			repoRoot = repoAbs
		}
	}
	allowed := func(p string) bool {
		return pathUnderRoot(p, repoRoot) ||
			pathUnderRoot(p, "/tmp") ||
			pathUnderRoot(p, "/var/tmp")
	}
	if !allowed(abs) {
		return fmt.Errorf("%w: %s", ErrCodexConfigPathForbidden, abs)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if !allowed(resolved) {
			// Do NOT leak the resolved target in the message beyond
			// the abs-path the caller already knows they passed —
			// resolved may point at a system file whose existence
			// or path is itself sensitive. Message says "symlink
			// escapes allowlist" and cites only the input path.
			return fmt.Errorf("%w: %s (symlink resolves outside allowed roots)", ErrCodexConfigPathForbidden, abs)
		}
	}

	// Field inspection — spec §7.a.
	insp, err := inspectCodexConfig(abs)
	if err != nil {
		return err
	}

	// Mismatch matrix.
	//
	// If mode is empty we still reject the ambiguous / no-field
	// cases so an operator who typed only --codex-config-path can't
	// silently run against a broken config; but we can't decide
	// which single-field case is "right" without a mode. To stay
	// safe, require an unambiguous single field when mode is empty.
	switch {
	case insp.HasBearer && insp.HasEnvKey:
		return fmt.Errorf("%w: both experimental_bearer_token and env_key present in %s", ErrCodexConfigMismatch, abs)
	case !insp.HasBearer && !insp.HasEnvKey:
		return fmt.Errorf("%w: neither experimental_bearer_token nor env_key present in %s", ErrCodexConfigMismatch, abs)
	}

	switch in.Mode {
	case "a":
		if !insp.HasBearer {
			return fmt.Errorf("%w: mode=a requires experimental_bearer_token in %s (found env_key)", ErrCodexConfigMismatch, abs)
		}
	case "b":
		if !insp.HasEnvKey {
			return fmt.Errorf("%w: mode=b requires env_key in %s (found experimental_bearer_token)", ErrCodexConfigMismatch, abs)
		}
	}

	// Env precondition (spec §7.d) also fires when the CALLER did not
	// declare a mode but the CONFIG unambiguously requires env_key —
	// otherwise `--codex-config-path <env_key.toml>` (no --codex-config-mode)
	// would silently accept a sentinel or empty OPENAI_API_KEY and
	// produce a fake latency measurement. Mode=b already ran the same
	// check earlier in this function; here we cover the "mode empty +
	// env_key config" case.
	if in.Mode == "" && insp.HasEnvKey {
		v := in.GetEnv("OPENAI_API_KEY")
		if isSentinelOpenAIKey(v) {
			return fmt.Errorf("%w: check OPENAI_API_KEY is set to a real credential (config at %s requires env_key)", ErrOpenAIKeyMissing, abs)
		}
	}
	return nil
}

// findCodexRepoRoot walks up from the current working directory looking
// for a `go.mod` whose first line names the multi-agent module. Returns
// the directory or "" on failure. Used by runner.go to seed
// codexConfigInputs.RepoRoot; the sandbox check falls back to /tmp when
// this returns "".
func findCodexRepoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	d := cwd
	for {
		p := filepath.Join(d, "go.mod")
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "module github.com/yourorg/multi-agent") {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// pathUnderRoot reports whether abs is root itself or a descendant of it.
// Empty root always returns false.
func pathUnderRoot(abs, root string) bool {
	if root == "" {
		return false
	}
	root = filepath.Clean(root)
	if abs == root {
		return true
	}
	return strings.HasPrefix(abs, root+string(filepath.Separator))
}
