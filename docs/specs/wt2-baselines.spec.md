# WT-2-baselines — Spec

> Phase 2 §E1 + §E2 + §E3. Worktree `paper/v3/p2-baselines`.
> Baseline: `origin/paper/v3-integration` (HEAD `d053897` at spec time).
> Scope: `multi-agent/tests/eval/baselines/` (NEW). Nothing else moves.

## 1. Purpose

Stand up three baseline "agent"s that consume the Phase 0 workloads and the
same oracle protocol (13 号 §1.3) that Loom itself consumes — so E1–E3 have
comparable non-Loom baselines for C5 (full-system vs baselines) and the
per-ablation tables in E1–E4.

The three baselines mirror 12 号 §E1 / §E2 / §E3:

1. **`manual_ssh`** (§E1) — user manually SSH-into-target-machine harness.
   Executes a hand-written **local** shell script per workload; no Loom
   driver, no capability discovery, no typed contract, no dynamic-MCP
   promotion. Simulates "what if the human just did it by hand". See §7(c)
   — this baseline uses **local shells only**, never a real SSH connection.
2. **`single_machine_claude_code`** (§E2) — single-machine Claude Code
   coding agent (chosen from `{Claude Code, Codex, OpenHands}`; Claude Code
   picked because it is already installed and usable on the eval host per
   the todo_list operational assumption). No multi-slave dispatch, no
   contract enforcement.
3. **`cloud_sandbox_e2b`** (§E3) — cloud sandbox baseline (chosen from
   `{E2B, Daytona, Modal}`; **E2B** picked because its API surface is the
   smallest and it has a first-class `--dry-run` story via its JSON API
   protocol). Uploads a workspace tempdir into an E2B sandbox and lets a
   single-machine agent run inside that sandbox.

Each baseline is a **separate binary** with a thin `run.sh` bash wrapper. A
shared `harness/` package owns the parts every baseline needs: workspace
setup, env whitelist, secret scan, oracle invocation, run-row emission.

**Explicitly out of scope for this worktree**:

* **Real Loom integration** — the baselines emit their own `BaselineRunRow`
  (see §5) that mirrors 08 号 §Data collection schema fields relevant to
  baselines. Wiring the row into the D1 `runs` SQLite table lives in
  `WT-1-run-schema` follow-ups; the CSV output is the persistence surface
  this worktree ships. When D1 is available, a follow-up plugs the writer
  in behind the existing seam.
* **True multi-device prod** — §C5 prod-multi-device is a separate Phase 3
  worktree (`WT-3-prod-multidevice`); see §7(c). This worktree covers
  baseline-side rows on the stub / single-host path only.
* **The other 8 ablation flags** (`NoCapabilityDiscovery` /
  `NoTypedContracts` / ... / `NoObserver`) — those are §E5, wired via
  `WT-2-flag-integration` into Loom itself, not baselines. This worktree
  stops at the 3 non-Loom baselines.

## 2. Module layout

```
multi-agent/tests/eval/baselines/
├── README.md                        overview + how to add a baseline
├── harness/                         shared Go package (baseline-agnostic)
│   ├── flags.go                     shared CLI flags + parsed Opts
│   ├── env.go                       env whitelist (mirrors runner §7(a))
│   ├── workspace.go                 fixture-copy tempdir (mirrors runner §7(b))
│   ├── scan.go                      pre-upload secret scan (wraps secretscrub)
│   ├── oracle.go                    oracle.sh invocation + JSON parse
│   ├── row.go                       BaselineRunRow + baseline_or_ablation regex
│   ├── writer.go                    CSV writer + row-append-forbidden guard
│   ├── run.go                       shared orchestrator; per-baseline runner
│   │                                supplies BaselineImpl interface
│   └── *_test.go                    per-file blackbox tests
├── manual_ssh/
│   ├── main.go                      CLI shim → harness.Run(..., ManualSSHImpl{})
│   ├── run.sh                       bash entrypoint (go run ./…/manual_ssh)
│   ├── impl.go                      BaselineImpl for manual_ssh
│   ├── workloads.go                 per-workload local shell script
│   └── impl_test.go
├── single_machine/
│   ├── main.go
│   ├── run.sh
│   ├── impl.go                      BaselineImpl for single_machine_claude_code
│   ├── workloads.go                 per-workload Claude Code prompt + expected outputs
│   └── impl_test.go
└── cloud_sandbox/
    ├── main.go
    ├── run.sh
    ├── impl.go                      BaselineImpl for cloud_sandbox_e2b
    ├── e2b.go                       E2B API client (dry-run + real)
    ├── workloads.go                 per-workload upload-fixture + remote-exec plan
    └── impl_test.go
```

Go module: same `github.com/yourorg/multi-agent`. Package paths:

* `.../tests/eval/baselines/harness` — importable library.
* `.../tests/eval/baselines/manual_ssh` — `package main`.
* `.../tests/eval/baselines/single_machine` — `package main`.
* `.../tests/eval/baselines/cloud_sandbox` — `package main`.

Rationale for one-binary-per-baseline (vs a single multiplexed binary with
`--baseline` flag):

1. The file-domain constraint in the prompt (`baselines/manual_ssh/` /
   `baselines/single_machine/` / `baselines/cloud_sandbox/`) is *directory*
   scoped — a single-binary design would put all baseline logic in a
   shared `cmd/`, which crosses the constraint.
2. Isolation of failure modes — the cloud baseline pulls in the E2B API
   surface (HTTP client + JSON schemas); the manual_ssh baseline is
   POSIX-only. Isolating them by binary keeps `go build ./tests/eval/baselines/manual_ssh`
   fast on hosts without cloud creds and prevents accidental import of
   cloud SDKs from the "manual" path.
3. Adding a fourth baseline is a new directory + `main.go`, not a switch
   arm in a growing dispatch table.

## 3. CLI surface (shared across all three baselines)

Each `main.go` is a thin shim. Shared flags live in `harness/flags.go`.

Every baseline binary exposes a single `run` subcommand (mirroring the
Phase 1 `eval-runner run` convention so future subcommands — `list`,
`replay` — can land without breaking the flag surface). All examples in
this spec and the 15-run smoke MUST include the `run` verb:

```
<baseline> run \
  --workload <id>                # required; must match a dir under --workload-dir
  --workload-dir <path>          # default multi-agent/tests/eval/workloads
  --baseline-name <name>         # override the default baseline_or_ablation
                                 # value; must match §5 regex.
                                 # Default per binary:
                                 #   manual_ssh       → "manual_ssh"
                                 #   single_machine   → "single_machine_claude_code"
                                 #   cloud_sandbox    → "cloud_sandbox_e2b"
  --run-id <id>                  # optional; default = derived
                                 # "brun-<unix>-<baseline>-<workload>-<rand-hex>"
  --timeout <duration>           # default = spec.timeout_seconds
  --out <path>                   # required; CSV output (header + 1 data row)
  --dry-run                      # Security §7(f). Skips external side effects
                                 # (claude CLI invocation / E2B API call).
                                 # Fixture copy + mock_workspace projection +
                                 # oracle invocation still run so a full row is
                                 # emitted.
  --keep-tempdir                 # debug; default false
```

Baseline-specific extras (each registered only by the relevant binary's
`main.go`):

```
# single_machine only:
  --forward-anthropic-api-key   # default false; when true, ANTHROPIC_API_KEY
                                 # from parent env is forwarded to the `claude`
                                 # subprocess ONLY (never to oracle).
                                 # --dry-run does not require it.

# cloud_sandbox only:
  --e2b-api-key-env <name>       # default E2B_API_KEY; the env var name
                                 # the harness reads the E2B key from.
                                 # --dry-run does not require the env var
                                 # to be set.
  --forward-e2b-api-key          # default false; when true, the harness
                                 # reads the env var named by --e2b-api-key-env
                                 # and passes it to the E2B HTTP client
                                 # in-process. Never reaches a subprocess.
```

### Exit codes (same across baselines)

| Code | Meaning |
|---|---|
| 0 | Run completed; oracle decided pass=true |
| 1 | Run completed; oracle decided pass=false (still 1 CSV row written) |
| 2 | Pre-flight failure: bad flag / invalid baseline name / secret scan
    | reject / workload spec invalid / fixture copy refused / oracle output
    | > 1 MiB |
| 3 | Internal / unexpected error (panic recovered, subprocess crashed) |

## 4. Execution pipeline

Every baseline binary walks the same 10-step orchestrator (in `harness/run.go`),
with two per-baseline seams — `Prepare(ws)` and `ExecuteAgent(ws)`:

```
   1. parseFlags()
   2. validateBaselineName()  ................ Security §7(d) — regex check
   3. loadWorkloadSpec()  .................... reuses fields from 13 号 §1.2
   4. workspace = MkdirTemp(0700)  ........... Security §7(g)
                                               defer RemoveAll(workspace)
   5. copyFixtures(workload/fixtures → workspace)  ... Security §7(b), §7(e)
   6. agentEnv  = WhitelistEnvForAgent(...)  .. Security §7(a) — includes
                                               per-baseline creds when the
                                               operator opted in.
      oracleEnv = WhitelistEnvForOracle(...) .. Security §7(a) — byte-
                                               identical to the WT-1
                                               eval-runner whitelist;
                                               NEVER contains per-baseline
                                               creds.
   7. impl.Prepare(workspace, agentEnv)  ..... per-baseline setup;
                                               cloud_sandbox: secret-scan+upload;
                                               manual_ssh / single_machine: no-op
   8. impl.ExecuteAgent(workspace, agentEnv, dryRun)  .. per-baseline agent
                                               invocation; dry-run branch
                                               never contacts external
                                               services. On success
                                               ${workspace} contains all
                                               outputs listed in
                                               spec.outputs.write_targets.
   9. runOracle(spec.success_oracle, workspace, oracleEnv, timeout) .. reuses
                                               same 1-MiB cap + oracle-line
                                               contract as WT-1-eval-runner-
                                               skeleton §7(f). ONLY oracleEnv
                                               is passed here; agentEnv is
                                               out of scope for this call.
  10. writeCSV(--out, row)  ................. header + 1 data row; --out file
                                               must not exist (no append).
  → exit 0 if oracle.passed else 1.
```

The oracle contract is identical to WT-1-eval-runner-skeleton §4 step 12
and 13 号 §1.3: the oracle emits **exactly one line of JSON on stdout**
— `{"passed": bool, "details": {...}, "metrics": {...}}` — exit 0/1/2.
The harness treats any of these as a run-level failure (row emitted with
`passed=false`, exit 1):

* zero stdout lines (empty stdout)
* first line is not a parseable JSON object matching the shape
* **any second line whatsoever after the first line's terminator**,
  including a blank line — 13 号 §1.3 Forbidden: "不准向 stdout 多打
  任何额外行" is strict. The single trailing `"\n"` that terminates the
  first line is the only permitted post-content byte; anything after
  that (a second line's opening byte, even `"\n"` itself) is a contract
  violation and the run is marked failed. The harness reads the entire
  stdout buffer (bounded by the 1 MiB cap) and rejects if
  `bytes.IndexByte(rest_after_first_newline, non-empty) != -1` OR
  `len(rest_after_first_newline) > 0`.

Enforcement lives in `harness/oracle.go` and is tested (see plan test
row #20 for the malformed-first-line case; the extra-line case gets its
own test row #20b). Baseline harness does **not** invent a new oracle
interface; `spec.success_oracle` in each workload's `spec.yaml` is
called exactly the same way, from the same tempdir cwd, with the same
1-MiB stdout cap.

### Per-baseline behaviour

* **`manual_ssh`** — `Prepare` is a no-op. `ExecuteAgent` runs a
  per-workload local bash script (bundled inside `manual_ssh/workloads.go`)
  that writes the expected outputs into `${workspace}` using the same
  content the maintainer would write by hand (e.g. for
  `cross-device-code-mod`, a script that echoes the diff header + hunk
  into `patch.diff` and `PASS` into `test.log`). This is the same "hand
  build a mock_workspace from scratch" flow the workload README documents,
  wrapped as a baseline. `--dry-run` mode: skip the script, copy
  `mock_workspace/` directly (still writes a full row); logged as
  `[DRY-RUN]` on stderr. Rationale: dry-run must never require the
  external service (per §7(f)); manual_ssh has no external service, so
  the branch degenerates but keeps the semantics uniform across the three
  baselines.

* **`single_machine_claude_code`** — `Prepare` is a no-op. `ExecuteAgent`
  in **real mode** invokes `claude` (Claude Code CLI) with a per-workload
  prompt that instructs it to produce the workload's `write_targets`
  inside `${workspace}` (cwd = `${workspace}`; env = whitelisted). In
  `--dry-run` mode, projection of `mock_workspace/` is used and the real
  CLI is skipped. When `claude` binary is not on `$PATH`, real mode fails
  fast with `ErrClaudeCLIUnavailable` (exit 2) rather than silently
  falling back to the dry-run stub — a real run must be observably real.

* **`cloud_sandbox_e2b`** — `Prepare`:
  1. Walk the copied workspace tempdir; run each file's bytes through
     `secretscrub.Sanitize`; if `sanitized != original` → refuse to upload
     that baseline run with `ErrPreUploadSecretDetected` (exit 2). See
     §7(b).
  2. In real mode: `POST /sandboxes` on the E2B API (via `--e2b-api-key-env`),
     `PUT` each scanned-clean file into the sandbox.
  3. In dry-run mode: **print the API call plan** to stderr (method +
     path + `Content-Length`) but never dial the API host; no key
     required.

  `ExecuteAgent`:
  * Real mode: `POST /sandboxes/<id>/exec` with a per-workload command
    that produces the workload's `write_targets`; then `GET` those
    targets back into `${workspace}` so the local oracle can grade them.
  * Dry-run mode: copy `mock_workspace/` locally (never contacts E2B);
    logged `[DRY-RUN]`. Terminates a run before any `net.Dial` call — see
    §7(h).

## 5. Data model — `BaselineRunRow` + CSV

Row struct (stable field order; CSV columns follow the same order):

| # | Column | Type | Source |
|---|---|---|---|
| 1 | `run_id`                        | string | flag or derived |
| 2 | `workload_id`                   | string | spec.id |
| 3 | `baseline_or_ablation`          | string | `--baseline-name`; regex §7(d) |
| 4 | `started_at_unix`               | int64  | wall clock at step 1 |
| 5 | `finished_at_unix`              | int64  | wall clock at step 10 |
| 6 | `duration_ms`                   | int64  | finished − started, ms |
| 7 | `passed`                        | bool   | oracle |
| 8 | `oracle_exit_code`              | int    | oracle |
| 9 | `oracle_details_json`           | string | oracle details sub-object verbatim |
| 10 | `oracle_metrics_json`          | string | oracle metrics sub-object verbatim |
| 11 | `dry_run`                      | bool   | `--dry-run` |
| 12 | `metrics_baseline_wall_time_ms` | int64  | §7(g) — total ExecuteAgent wall time |
| 13 | `metrics_baseline_api_calls`   | int    | §7(g) — count of external API calls actually issued (0 in dry-run and for non-cloud baselines) |
| 14 | `metrics_baseline_upload_bytes` | int64  | §7(g) — bytes actually uploaded (0 in dry-run) |
| 15 | `tempdir_kept`                 | bool   | `--keep-tempdir` |

Columns 3–13 are the baseline-specific value-add over `runs`. Columns 12–14
are the "how much did this baseline cost" trio §7(g) mandates. When the D1
`runs` writer lands, the emit-plan is: columns 1–10 + 15 map 1:1 into the
existing `runs` table; column 3 → `runs.baseline_or_ablation`; columns
11–14 fold into `runs.artifact_hashes_json` as a `baseline_metrics` object
(the existing catch-all JSON column keeps the schema append-stable
without a migration).

CSV row count after a successful single-workload run: **2 lines** (header
plus one data row). Fail-mode runs also emit 2 lines — the row's `passed`
is `false` but the row is still complete. The acceptance smoke asserts
`wc -l == 2` per invocation.

## 6. `BaselineImpl` interface (per-baseline seam)

```go
// BaselineImpl is the per-baseline seam. All non-trivial work lives here;
// the harness owns the surrounding pipeline (workspace setup, env, oracle).
type BaselineImpl interface {
    // Name returns the default baseline_or_ablation value used when
    // --baseline-name is empty. Must match §7(d) regex.
    Name() string

    // Prepare is called after the workspace tempdir is populated with the
    // workload's fixtures but before ExecuteAgent. Cloud baselines do
    // upload here; manual_ssh / single_machine are no-op.
    // `agentEnv` is the whitelisted env for the agent subprocess (may
    // contain a per-baseline credential if the operator opted in via
    // --forward-*-api-key). The oracle env is not passed here — the
    // harness constructs it separately and hands it directly to runOracle
    // at pipeline step 9; implementations must never store or read the
    // oracle env.
    // Returning a non-nil error aborts with exit 2 (pre-flight failure).
    Prepare(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) error

    // ExecuteAgent produces the workload's outputs under ws.Root so the
    // oracle can grade them. When dryRun is true, no external service may
    // be contacted; the implementation must fall back to projecting
    // fixtures/mock_workspace (already unpacked into ws.Root by the
    // harness) as the "agent output".
    // `agentEnv` is the same slice passed to Prepare; same caveat about
    // never touching oracleEnv.
    // ExecuteMetrics returns the counters that feed row columns 12–14.
    ExecuteAgent(ctx context.Context, ws *Workspace, agentEnv []string, dryRun bool) (ExecuteMetrics, error)
}

type ExecuteMetrics struct {
    WallTimeMS  int64
    APICalls    int
    UploadBytes int64
}
```

`Workspace` and env whitelist are re-implemented inside `harness/` rather
than imported from `tools/eval/runner/` — the runner is `package main` and
does not expose these types. A future DRY worktree may lift both into a
shared package under `internal/`; called out explicitly here to avoid
drift-blame if reviewers spot the near-duplicate.

## 7. Security mitigations

All eight items are testable and must each have at least one test row in
the plan's matrix (§`docs/specs/wt2-baselines.plan.md`). A run that
violates any of (a)–(h) is a P0 bug.

### (a) `exec.Cmd.Env` whitelist

**Threat.** Parent process may have `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`AWS_*` in its env. Baseline harness spawns per-workload scripts and
per-baseline external tooling (bash for manual_ssh, `claude` CLI for
single_machine, HTTP client for cloud). A malicious workload script could
`env > /tmp/leak.txt`; a shelled-out `claude` process gets whatever env
we hand it — leaking a project-owned API key to a third-party subprocess
is the same class of failure the runner spec calls out.

**Mitigation.** The harness runs TWO separate whitelist calls per run —
`WhitelistEnvForAgent(...)` and `WhitelistEnvForOracle(...)` — so
per-baseline agent credentials can never reach the oracle subprocess by
accident.

**`WhitelistEnvForOracle` is byte-identical to the WT-1-eval-runner
whitelist** (`multi-agent/tools/eval/runner/subprocess.go` §7(a)):

* always: `PATH`, `HOME`, `LANG`, `LC_ALL`, `TZ`, `USER`
* always-if-set: `AGENTSERVER_ROOT`, `MODELSERVER_ROOT`, `APP_ROOT`,
  `MOCK_MODEL_URL`
* per-workload allowlist: `credential-bound-model` → `EXPECTED_MODEL_ALIAS`
* `LOOM_*` prefix passthrough (empty suffix rejected)

No per-baseline credential ever enters the oracle env. This invariant
is directly tested (plan row #8, plan row #9), and a `TestWhitelistEnvForOracle_MatchesRunnerContract`
test round-trips the two lists to catch drift if the runner's list is
ever updated without a matching baseline-harness change.

**`WhitelistEnvForAgent` = the oracle list PLUS per-baseline agent
credentials that are ONLY forwarded when the user explicitly opts in.**
Opt-in is expressed by a dedicated CLI flag on the baseline binary, NOT
by the choice of `--baseline-name`:

* `single_machine` binary registers `--forward-anthropic-api-key`
  (bool, default `false`). When true, `ANTHROPIC_API_KEY` (if present in
  parent env) is forwarded to the `claude` subprocess and to no other
  child. When false, real-mode `claude` still runs, but with no API key
  — the CLI errors out visibly; the operator learns "I asked for a real
  run without opting in to key forwarding" rather than silently getting
  a leak-shaped configuration.
* `cloud_sandbox` binary registers `--forward-e2b-api-key`
  (bool, default `false`). When true, the value of the env var named by
  `--e2b-api-key-env` (default `E2B_API_KEY`) is read INSIDE the harness
  and passed to the E2B HTTP client only. It is NEVER placed into the
  agent-subprocess env at all — E2B's client is in-process, no subprocess
  reads it.
* `manual_ssh` registers NO credential-forwarding flag. Its agent env
  == oracle env == the base whitelist.

This layout satisfies (a) without conflating "which baseline you picked"
with "you consented to forwarding a project-owned secret" — the two are
now separately controlled and both must be true for a key to leave the
harness process.

### (b) Cloud upload secret scan

**Threat.** A workload fixture (e.g. `.env`, an accidentally committed
notebook, a mock prompt containing `sk-...` for red-team purposes) is
uploaded into an E2B / Daytona / Modal sandbox. Cloud vendors persist and
often cache-index sandbox contents; once uploaded, deleting local
fixtures does not scrub the vendor side. This is the single most
dangerous failure mode of this worktree — the prompt calls it out
explicitly.

**Mitigation.** Cloud baseline's `Prepare`:

1. Walk every file under `ws.Root` after fixture copy (BEFORE any
   upload). Returns a `ScanReport` with four disjoint sets: `Flagged`
   (regex hit), `SafeToUpload` (fully scanned, passed), `SkippedBinary`
   (NUL in first 512 bytes), `SkippedLarge` (> 8 MiB).
2. If `Flagged` is non-empty → reject the whole run with
   `ErrPreUploadSecretDetected: <file>` (exit 2). Print the file's
   relative path to stderr; do NOT print the matched substring (would
   defeat the scrub).
3. **`skipped ≠ safe`.** `ExecuteAgent` iterates `SafeToUpload` only.
   Skipped-binary and skipped-large files are NEVER uploaded — a binary
   blob could still hold recognisable API keys further in, and the
   scanner did not read them. A workload that needs to ship binary /
   large fixtures must land an explicit `--allow-binary-upload` escape
   hatch (not this worktree). The skipped counts are logged to stderr
   so an operator sees "3 files skipped, will not be uploaded" rather
   than silently missing them.

The scan runs whether `--dry-run` is set or not — dry-run rejects too, so
a workload maintainer cannot land a leak by testing only with dry-run and
then flipping it later. Tested via a synthetic fixture that embeds
`sk-testonly-secret-1234567890AB` and asserts exit 2 + no upload attempted,
plus a positive-control test that a binary fixture is skipped, not
uploaded, and a plain-text fixture in the same tree IS uploaded.

Non-cloud baselines skip this scan (nothing leaves the host).

### (c) `manual_ssh` uses local shell only

**Threat.** A `manual_ssh` baseline that literally shells out to `ssh`
could accidentally connect to a prod machine — either through a stale
`~/.ssh/config` `Host *` block, an SSH agent that a running user pinned
to a prod key, or a workload fixture that names a prod hostname. Even a
harmless connection attempt against `agent.cs.ac.cn` on a CI run leaves
an authlog trail.

**Mitigation.** `manual_ssh/impl.go` runs a **local `/bin/bash`**
subprocess only. No `ssh`, `scp`, `rsync`, or `sftp` binary invocation is
allowed. Enforced by two tests:

1. Unit: `grep -rn '"ssh"\|"scp"\|"rsync"\|"sftp"'` over
   `tests/eval/baselines/manual_ssh/*.go` returns nothing (a
   `go:generate`-style constant check).
2. Runtime: `impl.go` builds subprocess argv exclusively via a fixed
   `bashPath = "/bin/bash"` constant; any deviation is a code review
   catch. The unit test also asserts the constant.

When (later) `WT-3-prod-multidevice` (C5) needs real multi-machine
plumbing, the design point is: **do not add real ssh here**; it belongs
in a Phase 3 prod-only worktree with its own security review.

### (d) Baseline name regex

**Threat.** `baseline_or_ablation` in D1 `runs` is a free-form string
today. If a baseline binary lets a caller pass `--baseline-name
"foo'; DROP TABLE runs; --"`, the SQL layer in the run-schema worktree
receives arbitrary text. Parameterised queries prevent SQL injection at
the DB layer, but the row value ends up in exported CSVs, paper tables,
LaTeX bibliographies, and shell scripts that grep on it; a shell-escaping
value there is a chain of pain.

**Mitigation.** `harness.ValidateBaselineName(s string) error` enforces
`^[a-z][a-z0-9_-]{2,63}$`:

* leading lowercase letter (no digit / punctuation)
* only `[a-z0-9_-]` thereafter (kebab-case + underscore allowed)
* length in `[3, 64]` — long enough to be meaningful, short enough not
  to blow past sqlite / csv column heuristics.

Rejected values exit 2 with `ErrBaselineNameInvalid: <value>` printed
verbatim. This regex is the ONLY sanitiser — no downstream sanitisation
is required (or trusted) at the storage layer.

### (e) Fixture copy to tempdir (no in-place mutation)

**Threat.** Same class as WT-1-eval-runner-skeleton §7(b). A baseline
whose `ExecuteAgent` writes into the workload's on-disk fixtures dir
leaves the source tree dirty, breaks subsequent runs, and hands the
oracle a moving target.

**Mitigation.** `harness.SetupWorkspace(fixturesDir, keep) (*Workspace, error)`
does exactly what the runner's fixtures.go does: `MkdirTemp("evalbaseline-")`,
`chmod 0700`, recursive copy of `fixtures/` (with the same symlink-escape
refusal), and — for cloud upload — the tempdir is the ONLY source the
uploader is allowed to read. Cloud `Prepare` opens files by rooting them
at `ws.Root` and rejects any path that escapes the root
(`filepath.Rel(ws.Root, path)` must not start with `..`).

### (f) `--dry-run` for cloud baselines is honoured

**Threat.** A CI runner without an E2B key mints a run in dry-run mode.
If the code accidentally short-circuits the guard and dials the API
anyway, either (i) the run fails opaquely with an auth error and the CI
signal degrades, or (ii) worse, a real API call is issued using a
leftover env var, spending budget and leaving audit trail.

**Mitigation.** Cloud baseline's `Prepare` and `ExecuteAgent`:

1. Check `dryRun` at the very top; on true, log `[DRY-RUN]` + the API
   call plan (method + path + Content-Length) to stderr, then return.
2. In-code invariant: the E2B client's `Do(req)` method **panics** if
   `dryRun` was set (the panic recovery layer maps it to exit 3 — a
   hard runtime signal to any test that asserts "dry-run must not
   dial"). Tested by a fake client that fails the test if `net.Dial` is
   called with `--dry-run`.
3. `--dry-run` is exercised for **all three** baselines in the smoke
   loop, not just cloud, so CI can run the 15-run matrix without
   external credentials.

### (g) Metrics for cost accounting

**Threat.** Without per-run cost metrics, the baseline comparison in
paper Table 2 is unfalsifiable — reviewers cannot tell whether the
cloud baseline is 100× slower because of "cloud" or because of "the
workload does 100× more work".

**Mitigation.** Row columns 12–14 (`metrics_baseline_wall_time_ms`,
`metrics_baseline_api_calls`, `metrics_baseline_upload_bytes`) are
populated per §5 and §6 (`ExecuteMetrics`). Numbers are meaningful for
`cloud_sandbox_e2b`; for `manual_ssh` and `single_machine_claude_code`,
`api_calls` and `upload_bytes` are `0` and `wall_time_ms` is real
subprocess wall time. Row is always emitted with these columns even in
dry-run (call count `0`, wall time still measured).

### (h) CI must skip real cloud calls

**Threat.** A CI pipeline that runs `go test ./tests/eval/baselines/...`
in a project account with cloud creds accidentally accessible could
mint real sandboxes on every push.

**Mitigation.** Two layers:

1. `cloud_sandbox` tests that touch the E2B API path (i.e. exercise
   real-mode `Prepare` / `ExecuteAgent`) are guarded by
   `t.Skip("cloud API tests require -tags e2b_live")` when the
   `e2b_live` build tag is absent. Default test invocation
   (`go test ./...`) does NOT set the tag → real API tests are skipped.
2. The 15-run smoke loop invoked from `docs/specs/wt2-baselines.spec.md`
   uses `--dry-run` universally. `run.sh` also refuses to omit
   `--dry-run` when it detects `CI=true` in env, exiting 2 with a
   helpful message. Overriding requires an explicit `--i-know-cost=yes`
   flag (not landed this worktree; reserved for §C5 prod runs).

## 8. Acceptance

End-to-end 15-run smoke (5 workloads × 3 baselines) all in dry-run mode:

```bash
cd multi-agent
go build -o /tmp/manual_ssh       ./tests/eval/baselines/manual_ssh
go build -o /tmp/single_machine   ./tests/eval/baselines/single_machine
go build -o /tmp/cloud_sandbox    ./tests/eval/baselines/cloud_sandbox

mkdir -p /tmp/wt2-smoke
for wl in cross-device-code-mod remote-data-processing windows-only-artifact \
          missing-parser-converter credential-bound-model; do
  for bl in manual_ssh single_machine cloud_sandbox; do
    /tmp/$bl run --workload $wl --dry-run \
                 --out /tmp/wt2-smoke/$wl-$bl.csv \
                 --workload-dir tests/eval/workloads
    test "$(wc -l < /tmp/wt2-smoke/$wl-$bl.csv)" = "2"
  done
done
# 15 CSVs, each 2 lines. Individual `passed` may be true or false; the
# acceptance is "oracle produced a decision and a row was written".
```

`run.sh` per baseline wraps the same invocation so the prompt's smoke
snippet works verbatim:

```bash
bash tests/eval/baselines/$bl/run.sh --workload $wl --dry-run
```

Plus `go test ./tests/eval/baselines/... -count=1 -shuffle=on -race` green.
Plus `go vet ./tests/eval/baselines/...` and
`gofmt -l tests/eval/baselines` clean.

## 9. Open seams for future worktrees

| Worktree | Seam this baselines-worktree leaves |
|---|---|
| `WT-1-run-schema` follow-up | `BaselineRunRow` maps 1:1 into `runs` (col 3 → `runs.baseline_or_ablation`; cols 11–14 fold into `runs.artifact_hashes_json.baseline_metrics`). No schema churn needed — the run-schema writer adds a `WriteBaselineRow(row)` method that reads the CSV and forwards. |
| `WT-2-flag-integration` | Ablation flags (`NoCapabilityDiscovery`, ...) attach to Loom-side runners, not baselines — they are §E5 material and land in the eval-runner CLI, not here. |
| `WT-3-stub-fulltable` | The 60-run Phase-3 matrix includes these 3 baselines × 5 workloads for 15 of its 60 rows. Real (non-dry-run) execution happens there; that worktree pays the wall-clock / API cost and switches `--dry-run` off. |
| `WT-3-prod-multidevice` (§C5) | Multi-machine SSH lives there, NOT in `manual_ssh`. §7(c) enforces the boundary. |
| Additional baselines (Codex, OpenHands, Daytona, Modal) | Each is a new sibling directory under `tests/eval/baselines/` implementing `BaselineImpl`. `harness/` stays unchanged. |
