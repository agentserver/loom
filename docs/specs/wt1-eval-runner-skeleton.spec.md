# WT-1-eval-runner-skeleton — Spec

> Phase 1 #07 (D3). Worktree `paper/v3/p1-eval-runner-skeleton`.
> Baseline: `origin/paper/v3-integration` (HEAD `17f2c3c` at spec time).
> Scope: `multi-agent/tools/eval/runner/` (NEW). Nothing else moves.

## 1. Purpose

Stand up the skeleton of `tools/eval/runner/` — the Go CLI that owns one
benchmark run from "spec.yaml in, CSV row out". This worktree wires:

1. **Workload parsing** against `multi-agent/tests/eval/workloads/<id>/spec.yaml`
   (fields fixed in [`13_workload_spec.md`](../../../paper_writing/docs/intermediate/13_workload_spec.md) §1.2).
2. **agentserver-stub** subprocess lifecycle (Phase 0 `multi-agent/tools/eval/agentserver-stub`,
   PR #47 — its `eval-bootstrap.sh` is the working reference).
3. **fixtures → tempdir copy** + `${workspace}` substitution.
4. **oracle.sh invocation** per §1.3 contract (one JSON line on stdout, exit
   codes 0/1/2).
5. **commit-meta collection** via Phase 0 `tools/eval/commit_meta/` Python CLI
   (PR #40) — JSON-out, parsed by the runner; emails redacted before persist.
6. **RunWriter** abstraction — a noop writer in this worktree; the
   `WT-1-run-schema` worktree swaps in the real SQLite-backed `D1` writer.
7. **CSV fallback** — always written when `--out <path>` is supplied so the
   acceptance smoke (1 workload → 1 data row) holds whether or not run-schema
   has merged.

**Explicitly out of scope for this skeleton**: spinning up `driver-agent` /
`slave-agent` / `observer-server` binaries to *execute* the workload. Those
agents require live OAuth-style five-tuple wiring + real model traffic and
deserve their own worktree (the todo_list entry's "拉起 observer/driver/slave"
clause is satisfied at acceptance time by the agentserver-stub subprocess + a
**deterministic agent stand-in** — `mock_workspace/` is copied in instead of
agent-produced artifacts, exercising the oracle path end-to-end without
inventing an integration that the rest of Phase 1 hasn't designed yet). The
runner exposes the seams (`AgentRunner` interface, subprocess plumbing) so the
real agent fanout can land later without redesigning the CLI.

## 2. Module layout

```
multi-agent/tools/eval/runner/
├── main.go              CLI entry; flag parsing; exit-code mapping
├── runner.go            Orchestrator: parse → start stub → setup workspace →
│                        agent stage (skeleton: copy mock_workspace) → oracle
│                        → collect commit_meta → redact → write CSV+RunWriter
├── subprocess.go        exec.Cmd wrapper: env whitelist + process group +
│                        timeout-kill of full group
├── fixtures.go          copy-tree workload/fixtures → tempdir(0700);
│                        substitute ${workspace} in spec.outputs.write_targets;
│                        unconditional cleanup on exit
├── redact.go            sha256[:8] email redactor; pure func, no IO
├── writer.go            RunWriter interface + NoopWriter + CSVWriter
└── *_test.go            see plan.md test matrix
```

Package path: `github.com/yourorg/multi-agent/tools/eval/runner` (and a
`package main` for the binary; the testable orchestration lives in
sub-packages or in `runner` itself with `package runner_test` blackbox tests).

## 3. CLI surface

Subcommand: `eval-runner run`. Single subcommand only this worktree; the
binary leaves room for future subcommands (`list`, `replay`).

```
eval-runner run \
  --workload <id>                # required; matches dir name under workload-dir
  --workload-dir <path>          # default multi-agent/tests/eval/workloads
  --stub-listen <host:port>      # default 127.0.0.1:18080; MUST be loopback
  --observer-db <path>           # optional; when set, RunWriter = SQL (stub
                                 # interface here; real impl in WT-1-run-schema)
  --codex-config <path>          # optional; transparently forwarded (recorded
                                 # in CSV/run row, not consumed locally)
  --run-id <id>                  # optional; default = derived
                                 # "run-<unix>-<workload>-<rand-hex>"
                                 # (Date.now/Math.random restrictions don't
                                 # apply — runner is a normal Go binary)
  --timeout <duration>           # default = spec.timeout_seconds
  --out <path>                   # required; CSV output (header + data row)
  --keep-tempdir                 # debug; default false. tempdir kept on disk
                                 # for inspection; runner prints its path.
```

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Run completed; oracle decided pass=true |
| 1 | Run completed; oracle decided pass=false (still 1 CSV row written) |
| 2 | Pre-flight failure: bad flag / non-loopback stub / forbidden observer-db / fixture copy refused / workload spec invalid / oracle output > 1 MiB / stub failed to come up |
| 3 | Internal / unexpected error (panic recovered, subprocess crashed without exit code) |

Exit 1 still produces a CSV row so the eval pipeline can aggregate failures.
Exit 2 does NOT write any CSV — pre-flight problems are operator errors.

## 4. Execution pipeline

```
   1. parseFlags()
   2. validateStubListen() ............ Security (d) — exit 2 on non-loopback
   3. validateObserverDB() ............ Security (e) — exit 2 on bad path
   4. loadWorkloadSpec() .............. fields per §1.2 (id, description,
                                        required_contexts, allowed_contexts,
                                        inputs.read_artifacts,
                                        outputs.write_targets,
                                        success_oracle, recovery_hint,
                                        timeout_seconds)
   5. workspace = MkdirTemp(0700) ..... Security (g)
                                        defer RemoveAll(workspace) [unless
                                        --keep-tempdir]
   6. copyFixtures(workload/fixtures → workspace) ... Security (b)
   7. substituteWorkspace(spec, workspace) — rewrite ${workspace} tokens in
      outputs.write_targets paths
   8. startStub(--listen, --workspace-id auto) ...... defer kill
                                        — wait /healthz (loop 50 × 100 ms)
   9. setEnv = whitelistEnv(os.Environ(), allowList) Security (a)
                                        always-allowed: PATH HOME LANG LC_ALL
                                        always-injected: AGENTSERVER_URL,
                                        MOCK_MODEL_URL (if set on parent),
                                        LOOM_<*> (parent passthrough)
                                        oracle-declared: EXPECTED_MODEL_ALIAS
                                        (declared in spec or via dedicated
                                        per-workload allowlist in this file)
  10. agentStage(spec, workspace)
        skeleton: if workload/fixtures/mock_workspace/ exists, copy its
        contents into workspace as the "agent output" — exercises oracle
        path 1:1 with the maintainer-facing self-check command from §1.4.
        Future: spawn driver/slave/observer with creds issued by stub.
  11. timeout = spec.timeout_seconds (or --timeout override)
      runOracle(spec.success_oracle, workspace, env, timeout, group=true)
                                        Security (a) lifecycle:
                                        SysProcAttr.Setpgid=true on Linux;
                                        on timeout kill(-pgid, SIGKILL)
                                        Security (f): cap stdout at 1 MiB;
                                        reject if exceeded; reject if first
                                        line is not valid JSON
  12. parseOracleStdout() — first newline-terminated line is JSON;
                                        verify shape {passed:bool,
                                        details:obj, metrics:obj};
                                        keep raw remainder for debugging only
                                        — never persisted.
  13. collectCommitMeta() — run `python -m commit_meta.collect --format=json`
        in same env policy; parse JSON; collect git author/committer email
        for HEAD of the loom repo (separate `git log -1 --format='%ae|%ce'`
        — commit_meta itself has no email fields today, see plan.md note).
  14. redactEmails(meta) — Security (c). For each field that resembles an
        email, replace with `sha256(lower(email))[:8]`. Empty/N-A strings
        pass through unchanged.
  15. assembleRunRow() — collect into a strongly-typed `RunRow` struct.
  16. runWriter.Insert(runRow) — when --observer-db is "" the writer is
        NoopWriter; otherwise (skeleton) it's still NoopWriter but a
        diagnostic line goes to stderr that "run-schema integration pending".
        WT-1-run-schema lands the real impl behind the same interface.
  17. writeCSV(--out, runRow) — header + one data row. Always run, regardless
        of writer. If --out file exists, fail (no accidental append).
  18. exit code per oracle pass.
```

## 5. CSV columns

Stable order (header line). Strings are CSV-quoted; commas inside JSON-encoded
columns are quoted by `encoding/csv`.

| # | Column | Source |
|---|---|---|
| 1 | `run_id` | flag or derived |
| 2 | `workload_id` | spec.id |
| 3 | `started_at_unix` | runner wall clock at step 1 |
| 4 | `finished_at_unix` | runner wall clock at step 17 |
| 5 | `duration_ms` | finished − started, ms |
| 6 | `passed` | bool from oracle |
| 7 | `oracle_exit_code` | int |
| 8 | `oracle_details_json` | string, the `details` sub-object verbatim |
| 9 | `oracle_metrics_json` | string, the `metrics` sub-object verbatim |
| 10 | `loom_commit` | commit_meta |
| 11 | `agentserver_commit` | commit_meta |
| 12 | `modelserver_commit` | commit_meta |
| 13 | `app_commit` | commit_meta |
| 14 | `os_kernel` | commit_meta.os.kernel |
| 15 | `os_distro` | commit_meta.os.distro |
| 16 | `os_arch` | commit_meta.os.arch |
| 17 | `machine_hostname` | commit_meta.machine_hostname |
| 18 | `author_email_sha8` | redacted |
| 19 | `committer_email_sha8` | redacted |
| 20 | `codex_config_path` | flag (raw string; empty if unset) |
| 21 | `stub_listen` | flag |
| 22 | `tempdir_kept` | "true"/"false" (debug aid) |

Columns 10–17 may be the literal `N/A: ...` strings when the relevant repo
isn't on disk — that's commit_meta's contract.

CSV row count after a successful single-workload run: **2 lines** (header
plus one data row). The acceptance smoke asserts `wc -l == 2`.

## 6. RunWriter interface (D1 seam)

```go
type RunRow struct {
    RunID, WorkloadID                                  string
    StartedAtUnix, FinishedAtUnix                      int64
    DurationMs                                         int64
    Passed                                             bool
    OracleExitCode                                     int
    OracleDetailsJSON, OracleMetricsJSON               string
    LoomCommit, AgentserverCommit, ModelserverCommit  string
    AppCommit                                          string
    OSKernel, OSDistro, OSArch, MachineHostname        string
    AuthorEmailSHA8, CommitterEmailSHA8                string
    CodexConfigPath, StubListen                        string
    TempdirKept                                        bool
}

type RunWriter interface {
    Insert(ctx context.Context, row RunRow) error
}

type NoopWriter struct{}
func (NoopWriter) Insert(context.Context, RunRow) error { return nil }
```

`WT-1-run-schema` will implement `type SQLiteWriter struct { db *sql.DB }`
behind the same interface. Skeleton hands out `NoopWriter` unconditionally.

## 7. Security mitigations

All seven items are testable and must each have at least one test row in the
plan's matrix. A run that violates any of (a)–(g) is a P0 bug.

### (a) `exec.Cmd.Env` whitelist

**Threat.** Parent process may have `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`AWS_*` etc. in its env. oracle scripts are bash; a malicious oracle could
`env > /tmp/leak.txt` or `curl -H "Authorization: Bearer $OPENAI_API_KEY" ...`
the parent's secrets out, OR burn the project's real API budget on its own
prompts.

**Mitigation.** `whitelistEnv(parentEnv, perWorkloadExtra) []string` returns
the explicit allow-list — default empty, plus:

* always: `PATH`, `HOME`, `LANG`, `LC_ALL`, `TZ`, `USER`
* always-if-set in parent: `AGENTSERVER_URL`, `MOCK_MODEL_URL`,
  `AGENTSERVER_ROOT`, `MODELSERVER_ROOT`, `APP_ROOT` (commit_meta consumes
  these), and any key matching `^LOOM_`
* per-workload allowlist: declared in the runner's per-workload allowlist
  table (initial entry: `credential-bound-model` → `EXPECTED_MODEL_ALIAS`).
  Future workloads add a row to that table — a workload cannot expand the
  allowlist through its spec.yaml alone, removing the bash → env declaration
  → leak path.

`AGENTSERVER_URL` is **injected by the runner** from `--stub-listen`, not
forwarded from parent — defending against a parent that pointed real prod URL
at child subprocesses.

### (b) Fixture copy to tempdir (no in-place mutation)

**Threat.** Oracles open files relative to `${BASH_SOURCE[0]%/*}/fixtures/...`
(13 doc §1.3 implementation note). If the runner invokes them with cwd =
`workloads/<id>/fixtures/`, every run leaves the working tree dirty: outputs
land in the source-controlled fixture dir, the next run starts non-clean, and
path traversal via `${workspace}/../` escapes are no longer contained.

**Mitigation.** `copyFixtures(srcDir, dstTempdir)` copies recursively. Cwd
for oracle subprocess = the tempdir's `mock_workspace`/`workspace` sub-path.
`${workspace}` rewriting points to the tempdir. Source tree is read-only from
the runner's POV. tempdir removed on exit (unless `--keep-tempdir`).

Symlink safety: copy refuses any symlink in the source tree that points
outside the source tree (resolved-target prefix-check). Bare in-tree symlinks
are materialized as the file they reference (copy, not symlink), so a later
oracle write can't follow a symlink out of the tempdir.

### (c) Email redaction

**Threat.** Author / committer email lands in the run schema and CSV; CSV
exports get checked into experiment repos, attached to papers, shipped to
external reviewers. Plain emails de-anonymise contributors.

**Mitigation.** Before write:
`redactEmail(addr) = hex(sha256(strings.ToLower(strings.TrimSpace(addr))))[:8]`.
Same author → same 8-hex; cross-comparable, non-recoverable. Empty / `"N/A:
..."` strings pass through unchanged (so missing-data signals are preserved
without inventing a fake hash). The 8-hex truncation is intentional — a
researcher reviewing CSV diffs can still spot "same author across runs"
without standing up a full sha256 column.

### (d) Stub bind must be loopback

**Threat.** agentserver-stub has no auth on `/api/v1/agents/register`. Bound
to `0.0.0.0`, anyone on the LAN mints tokens; a constructed five-tuple lets
an attacker MITM the model-proxy plane.

**Mitigation.** `validateStubListen(s)`:

1. Parse host:port with `net.SplitHostPort`; reject malformed.
2. Resolve host; require ALL resolved addresses to be loopback:
   `net.IP.IsLoopback()` true. Accept literal `127.0.0.1`, `::1`, and host
   `localhost` ONLY when its A/AAAA records all resolve loopback. Reject
   `0.0.0.0`, `::`, any RFC1918 / public IP.
3. exit 2 with stderr `ErrStubMustBeLoopback: <addr>` on any failure.

### (e) `--observer-db` path safety

**Threat.** A typo'd `--observer-db /etc/passwd` overwrites the host's
passwd file with SQLite header bytes; `--observer-db /proc/self/...` /
`/sys/...` similarly poisons kernel pseudo-files.

**Mitigation.** `validateObserverDB(p)`:

1. Empty string is valid (RunWriter = Noop).
2. Resolve to absolute path via `filepath.Abs` + `filepath.Clean`.
3. Reject if resolved path has any of these prefixes:
   `/etc/`, `/proc/`, `/sys/`, `/dev/`, `/boot/`, `/var/log/` (system-owned
   directories that a sqlite file has no business in).
4. Require the resolved path to live under one of: the current working
   directory subtree, `/tmp/`, `/var/tmp/`, the user's `$HOME` subtree, or
   an explicit `--observer-db-root <path>` override (not introduced this
   worktree — listed as the escape hatch for future custom roots).
5. exit 2 with stderr `ErrObserverDBPathForbidden: <addr>` on failure.

The validation runs **before** SQLite touches the file.

### (f) Oracle stdout size cap

**Threat.** A misbehaving / malicious oracle pipes gigabytes to stdout;
runner OOMs trying to read the "first line".

**Mitigation.** Wrap subprocess stdout in `io.LimitReader(stdout, 1 MiB +
1 byte)`. If the reader yields > 1 MiB before EOF or newline, kill the
subprocess group and exit 2 with `ErrOracleOutputTooLarge`. The first line
(if obtained inside 1 MiB) is parsed; the run is marked failed (pass=false)
on parse error rather than escalating to exit 2 — bash oracles with `set -u`
sometimes emit stderr noise and a malformed JSON line is a workload bug, not
a runner pre-flight error. (Size cap is the unconditional exit-2 case;
"oracle exists, ran, returned bad JSON within size cap" stays at exit 1.)

### (g) Tempdir perms 0700

**Threat.** Multi-tenant boxes (shared eval host, CI runner) — another local
user reads run artifacts (proxy_tokens emitted by agentserver-stub, model
prompts/responses, the workload's mock prompts).

**Mitigation.** `os.MkdirTemp("", "evalrun-")` then `os.Chmod(dir, 0700)`
explicitly (MkdirTemp on some unix setups respects umask and yields 0755).
Permission is asserted in test.

## 8. Acceptance

End-to-end smoke against the workload `cross-device-code-mod` (its
`fixtures/mock_workspace/{patch.diff,test.log}` is the maintainer-facing
self-check artifact set; oracle returns pass=true on it):

```
go build -o /tmp/eval-runner ./tools/eval/runner
/tmp/eval-runner run \
    --workload cross-device-code-mod \
    --workload-dir multi-agent/tests/eval/workloads \
    --stub-listen 127.0.0.1:18080 \
    --out /tmp/run.csv
test "$(wc -l < /tmp/run.csv)" = "2"
```

Plus `go test ./tools/eval/runner/... -count=1 -shuffle=on -race` green.
Plus `go vet ./...` and `gofmt -l tools/eval/runner` clean.

## 9. Open seams for future worktrees

| Worktree | Seam this skeleton leaves |
|---|---|
| `WT-1-run-schema` | `RunWriter` interface; SQLiteWriter swaps in; row schema mirrors §5 columns 1:1 (no reshuffling needed). |
| `WT-1-fault-injection` (D5) | `AgentStage` is a pluggable function — fault injector wraps it to drop env vars / kill stub mid-run. |
| `WT-1-routing-trace` (C2) | runner already passes `--codex-config` through; routing-trace worktree adds a stage that collects `route.json` from the workspace and folds `selected_context` into RunRow. |
| `WT-1-capability-snapshot` (A1) | `RunRow.CapabilitySnapshotHash` is the next column; appending columns preserves the CSV's append-only schema policy. |
| Real driver/observer/slave wiring | replace skeleton `agentStage` with a real spawn-and-wait that issues five-tuples from the stub and writes outputs via the driver's MCP loop. The CLI flags don't change. |
