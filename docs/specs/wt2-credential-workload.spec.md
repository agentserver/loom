# WT-2-credential-workload — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 2 table row
> **WT-2-credential-workload** (line 101 of the Phase 2 block, §C3 of
> the 12 号 workload doc).
> Branch: `paper/v3/p2-credential-workload`.
> Base: `origin/paper/v3-integration @ d053897` (already includes
> WT-1-capability-snapshot merge).
> Consumes: `multi-agent/tests/eval/workloads/credential-bound-model/`
> (Phase 0 workload — `spec.yaml`, `oracle.sh`, fixtures — **not modified**
> by this worktree), and `multi-agent/tools/eval/runner/` (Phase 1
> skeleton, PR #53 — extended with two flags + startup assertions).
> Companion documents:
> - `/root/paper_writing/docs/intermediate/12_workload_spec.md` §C3
>   (credential-bound-model, dual-path codex config).
> - `multi-agent/tests/prod_test/E2E_RUNBOOK.md` line 266 (混配 warning).
> - `docs/specs/wt1-run-schema.spec.md` §3 (`baseline_or_ablation`
>   free-form TEXT column — this worktree extends the documented enum).

---

## 1. Task boundary & file scope

Six files are owned by this worktree; nothing else is touched.

| Path | Action |
|---|---|
| `multi-agent/tools/eval/runner/main.go` | **edit** — add `--codex-config-path <path>` and `--codex-config-mode {a,b}` flags; pass into `Opts` |
| `multi-agent/tools/eval/runner/runner.go` | **edit** — thread the two flag values into a startup validation step (new function `validateCodexConfig`); on failure return `preflight(...)` with exit 2 |
| `multi-agent/tools/eval/runner/codexconfig.go` | **new** — `validateCodexConfig` + `ErrCodexConfigMismatch` / `ErrCodexConfigPathForbidden` / `ErrCodexConfigModeInvalid` / `ErrOpenAIKeyMissing` sentinels; TOML sniff (no third-party toml dep — the file is inspected line-by-line for the two field names) |
| `multi-agent/tools/eval/runner/codexconfig_test.go` | **new** — 8-row table matrix from the Plan doc |
| `multi-agent/tests/eval/workloads/credential-bound-model/eval/eval-modelproxy-overhead.sh` | **new** — shell harness that runs the workload twice (mode=a, mode=b) at the same prompt + seed, prints `ModelProxyOverhead = latency_a - latency_b` |
| `multi-agent/tests/eval/workloads/credential-bound-model/eval/hops_loopback_check.sh` | **new** — helper invoked by the harness: given `route.json` + mode, exit 0 iff the `hops[].proxy_addr` values match the mode's loopback expectation |

Hard rules:

- **Do not modify** any file under
  `multi-agent/tests/eval/workloads/credential-bound-model/` OTHER than
  the new `eval/` subdirectory — `spec.yaml`, `oracle.sh`, `README.md`,
  and every file under `fixtures/` stays untouched. The oracle's
  contract (Phase 0) is the ground truth for the workload; this worktree
  only adds the **evaluation** wrapper.
- **Do not modify** the runner's core pipeline — no changes to
  `writer.go`, `subprocess*.go`, `redact.go`, `fixtures.go`, or the
  `RunRow` schema. The new flags are additive; the recorded value
  reuses the existing `codex_config_path` CSV column.
- **Do not** vendor a TOML parsing library. The runner already
  stays stdlib-only. `codexconfig.go` inspects the file with a
  hand-rolled line scanner that recognises the exact two field
  keys we assert on (`experimental_bearer_token`, `env_key`) —
  false positives are cheap (rejected as `ErrCodexConfigMismatch`),
  false negatives are the risk we are engineering against.
- **Do not** vendor an SSH / codex client. This worktree runs the
  existing workload harness — the two runs invoke the runner CLI
  the same way, only the `--codex-config-*` flags differ.
- **No `go.mod` changes.** The new Go files import only stdlib.
- The two `prod_test/driver-codex-local/` files (`.codex/config.toml`
  and `codex-config.toml`) are **operator-owned** — this worktree
  reads them but does not write them, and does not include them in
  the git tree (they are already gitignored for token safety).

### 1.1 Why the workload files stay frozen

Phase 0 landed `oracle.sh` and `spec.yaml` after seven review rounds; the
oracle's leak grep, its regex-escape of `EXPECTED_MODEL_ALIAS`, and the
non-empty `proxy_context_id` assertion are load-bearing. Reopening those
files here would drag the whole Phase 0 review surface back in scope.
The dual-path work is orthogonal — it changes **how** we invoke the
workload, not **what** the workload asserts — so it lives in a sibling
`eval/` directory.

The **only** additional route-trace assertion is the loopback check on
`hops[].proxy_addr` (§2.4). That check is performed by the new
`hops_loopback_check.sh` helper — invoked by the eval harness after the
oracle passes — not by the oracle itself. Keeping it out of the oracle
means Phase 0 workloads that populate `hops` for other reasons (e.g.
future workloads that legitimately use direct upstream in mode=a) are
not silently invalidated.

---

## 2. Behaviour

### 2.1 The two paths

Path (a) — local proxy:

- Codex talks to a loopback modelserver (`http://127.0.0.1:53452/v1`)
  that holds the upstream credential.
- `.codex/config.toml` uses `experimental_bearer_token = "<opaque>"`.
- Downstream persisters (NOT the runner — see §2.5) label the run's
  `baseline_or_ablation` column with the string `"credential-a"`.

Path (b) — upstream direct:

- Codex talks to the upstream endpoint itself
  (`https://code.ai.cs.ac.cn/v1` or the environment's equivalent).
- `codex-config.toml` uses `env_key = "OPENAI_API_KEY"`; the credential
  is sourced from the process environment.
- Downstream persisters (NOT the runner — see §2.5) label the run's
  `baseline_or_ablation` column with the string `"credential-b"`.

### 2.2 The runner flags

Two flags, both additive:

- `--codex-config-path <path>` — absolute or repo-relative filesystem
  path to a codex `config.toml`. When set, the runner:
  1. resolves and normalises the path (`filepath.Abs` + `filepath.Clean`);
  2. asserts the resolved path is inside the repository root OR inside
     `/tmp/` (§7.b — path traversal defence);
  3. reads the file, extracts the token/env_key fields, matches against
     the mode (§7.a);
  4. records the ORIGINAL (unresolved) path into the CSV's
     `codex_config_path` column, unchanged from PR #53's semantics.
- `--codex-config-mode {a,b}` — convenience flag. This flag NEVER
  resolves a path on its own. Its only responsibilities are:
  1. select the auth-field assertion (mode=a → require
     `experimental_bearer_token`; mode=b → require `env_key`);
  2. (mode=b only) trigger the `OPENAI_API_KEY` env-var precondition
     (§7.d).

  Path resolution is the harness's job — `eval-modelproxy-overhead.sh`
  computes the concrete `--codex-config-path` for each mode from a
  well-known layout (see §2.3) and always passes both flags to the
  runner in the same invocation. The runner refuses to guess a path
  from a mode alone (rejecting `--codex-config-mode b` with no
  `--codex-config-path` would be user-friendly but would drag a search
  root, an override flag, and a resolution table into the runner's
  contract — none of which are needed once the harness owns the layout).

  When `--codex-config-mode` is set without `--codex-config-path`, the
  runner still runs the env-var precondition for mode=b and the mode
  charset validation (rejecting `--codex-config-mode c`), then returns
  nil. This keeps the flag useful for callers that only want the
  precondition check without also opening a file.

The runner does NOT invent codex configs. If neither flag is passed the
runner behaves exactly like PR #53 today (no assertions run, no path
recorded beyond whatever the caller passes via the pre-existing
`--codex-config` flag). This preserves the Phase 1 harness for callers
that don't care about the dual-path story.

### 2.3 The eval harness `eval-modelproxy-overhead.sh`

Contract: given a workload id (default `credential-bound-model`) and a
sample count `N` (default 3, minimum 3), invoke the runner **N × 2**
times — N with `--codex-config-mode a`, N with `--codex-config-mode b`
— using the **same prompt fixture and same provider stack** across the
whole set. Sampler-level seed pinning is NOT part of the invariant
(see §7(e) — the codex CLI at this base ref exposes no seed knob);
"same" here means every input the harness can pin (prompt file,
model provider block, run env-var whitelist). Between each pair of
adjacent runs the harness sleeps 30 s to defuse rate limit edges (§7.e).

For each run it:

- invokes the runner with `--keep-tempdir` so the workspace (which
  contains `route.json`, `completion.txt`, `run.log`) survives past
  the runner's exit. Without this flag the runner's default `Cleanup`
  removes the tempdir before the harness can read it (per
  `multi-agent/tools/eval/runner/fixtures.go:78`).
- captures the runner's stderr to `/tmp/wt2-modelproxy-<mode>-<i>.err`
  and greps out the workspace path from the
  `eval-runner: --keep-tempdir set; workspace at <path>` line
  (existing PR #53 diagnostic, fixtures.go:85). The regex is
  `s/.*workspace at //` on that line; the extracted path is the
  argument passed to `hops_loopback_check.sh`.
- passes `--out /tmp/wt2-modelproxy-<mode>-<i>.csv`, reads the
  `duration_ms` column back out.
- runs `hops_loopback_check.sh <workspace>/route.json <mode>` against
  the extracted workspace. If the runner did NOT keep the tempdir
  (e.g. it exited pre-flight), the workspace path grep fails and the
  harness aborts with a clear diagnostic instead of silently skipping
  the check.
- deletes the workspace and the stderr file at the end of each iteration
  regardless of pass/fail (the harness owns the temp-lifetime because
  it opted the runner out of cleanup); on the failure path the paths
  are printed first so an operator can re-inspect before rerun.
- aborts (exit 1) with a diagnostic if either the oracle or the loopback
  check fails.

At the end the harness computes:

```
latency_a_ms = mean(duration_ms over the N mode=a runs)
latency_b_ms = mean(duration_ms over the N mode=b runs)
ModelProxyOverhead = latency_a_ms - latency_b_ms   # signed; may be negative
```

and prints them to stdout as a single line
`ModelProxyOverhead: latency_a=<f> latency_b=<f> delta=<f> (n=<N>)`.

The harness supports `--dry-run` — in that mode it validates every
input (mode flag, sample count, resolved config paths, `OPENAI_API_KEY`
presence for mode=b, executable bits on the runner + oracle) and prints
the exact runner command lines it WOULD execute, then exits 0 without
spawning the runner. `--dry-run` is what CI exercises; the real
measurement runs live only on operator machines with real credentials.

The header comment of the script explicitly labels the two-run pattern
as **"同 prompt 双跑"** (§7.e), so a reviewer scanning the harness for
correctness sees the semantic contract in the first ten lines.

### 2.4 The loopback-hop check

`hops_loopback_check.sh <route.json> <mode>` scans `route.json.hops[]`
for any element whose `proxy_addr` field is set. For:

- `mode=a`: at least one hop MUST have a `proxy_addr` equal (exact
  string, no substring) to one of `127.0.0.1`, `::1`, or `localhost`.
  The `proxy_addr` MAY carry a port (`127.0.0.1:53452`) — the check
  splits on the LAST colon and compares the host part alone against the
  allowlist. This defeats the substring-attack shape `127.0.0.1.evil.com`
  (§7.f), because `filepath.Base`-style splits do not apply to
  hostnames and a fake host that ENDS in `.evil.com` will fail the
  exact-equality test on the host component.
- `mode=b`: NO hop may have a `proxy_addr` matching the loopback
  allowlist (upstream-direct means the proxy hop MUST be absent). A
  `proxy_addr` field pointing at an upstream hostname is fine; the
  check only vetoes the presence of a loopback proxy.

The check exits 0 on match, 1 on mismatch. `hops` is optional in the
oracle (Phase 0 explicitly left it un-asserted), so a run with an
EMPTY `hops` array passes `mode=a`'s check ONLY IF the array is present
AND the check finds a loopback entry — an empty `hops` array in mode=a
fails the check with exit 1 (contract requires evidence, not absence
of evidence). The check reads the JSON with plain grep + sed (no jq
dependency); the workload's fixture always writes canonical JSON with
one field per line, and any run that violates canonical shape is
already caught by the oracle's `TestWorkloadJSONOutputsAreValid` Go
test upstream.

### 2.5 The `baseline_or_ablation` enum extension

The evalrun schema (`multi-agent/internal/evalrun/schema.go` line 25,
also documented in `docs/specs/wt1-run-schema.spec.md` line 110) has a
`BaselineOrAblation string` field, free-form TEXT. Existing documented
members: `"full"`, `"manual_ssh"`, `"NoCapabilityDiscovery"`.

This worktree extends the DOCUMENTED enum with two members that
DOWNSTREAM PERSISTERS will use — the runner itself does not populate
this column:

- `"credential-a"` — a run performed with `--codex-config-mode a`
  (local-proxy path).
- `"credential-b"` — a run performed with `--codex-config-mode b`
  (upstream-direct path).

The runner does NOT write directly into the `runs` table — that is
WT-1-run-schema's SQLite writer (`multi-agent/internal/evalrun/writer.go`),
which is a DIFFERENT consumer downstream of this worktree's CSV
output. The runner's own CSV row schema (`multi-agent/tools/eval/runner/writer.go:17`)
has NO `baseline_or_ablation` field and this worktree does not add
one — the runner just emits a `codex_config_path` and a
`codex_config_mode` semantic marker (via the mode being one of the
existing runner CLI flags recorded in the emitted diagnostic line).
Any tool that later ingests these CSVs into the `runs` table
(`multi-agent/internal/observerstore/schema.sql:199`, TEXT NOT NULL)
must set the column to one of the two strings above based on the
recorded mode.

The labels are stable so the scorer can filter by them without
ambiguity; that is the concrete answer to the §7.g consumer-view
audit.

Because the schema field is free-form TEXT, this is a documentation
change only — no DDL migration, no schema.sql edit, no evalrun/writer.go
change. The two new members are recorded here so the next reviewer
touching `wt1-run-schema.spec.md` § "documented enum values" merges
them into that spec's list.

---

## 3. What is out of scope

- Real codex execution or network calls in test / CI (§7.d guards).
- Changes to the workload oracle, spec.yaml, README, or fixtures.
- The SQLite writer itself (that is WT-1-run-schema's file scope).
- Any new provider block in `prod_test/driver-codex-local/` — the
  files are already present on the operator's machine per Phase 0.
- Ablation registry entries (`NoCredentialBound`, etc.) — those live
  in `multi-agent/internal/ablation/` and are out of this worktree.
- Changes to the runner's `CodexConfigPath` CSV semantics (the value
  is still the caller-provided path string, verbatim). We do NOT add
  a new CSV column for the mode — see §2.5 on why the labelling lives
  in `baseline_or_ablation` rather than a new column.

---

## 4. Acceptance criteria

1. `go test ./tools/eval/runner/... -count=1 -shuffle=on -race` passes,
   including the 8-row `codexconfig_test.go` table matrix.
2. `go vet ./...` clean.
3. `bash tests/eval/workloads/credential-bound-model/eval/eval-modelproxy-overhead.sh --mode a --dry-run`
   exits 0 and prints the planned commands. Same for `--mode b --dry-run`.
   (No real runner is spawned in `--dry-run`.)
4. On an operator machine with real driver-codex-local configs and a real
   `OPENAI_API_KEY`, `eval-modelproxy-overhead.sh` collects **≥ 3
   samples per mode** and prints `ModelProxyOverhead`. This is checked
   manually — CI only exercises `--dry-run` (§7.h).
5. The Phase 0 oracle passes on the fixture workspace unchanged
   (`bash oracle.sh fixtures/mock_workspace` → exit 0). This is a
   guardrail that the workload files have not drifted.

---

## 5. Design notes

### 5.1 Why the runner reads the config and doesn't embed it

Embedding codex config contents in the runner would fork the source of
truth. `prod_test/driver-codex-local/{.codex/config.toml,codex-config.toml}`
is the operator's file — changing modelserver ports, rotating bearer
tokens, or adding new provider fields would have to be mirrored into
the runner every time. Reading from disk keeps a single source of truth
per operator. The trade-off is that the runner MUST tolerate config
drift — the assertion in §7.a fails LOUDLY (exit 2) rather than
silently, so drift is a caught error, not a fake-data-emitting one.

### 5.2 Why the mode flag is a convenience, not a schema

`--codex-config-mode` exists because writing
`--codex-config-path multi-agent/tests/prod_test/driver-codex-local/.codex/config.toml`
in every eval invocation is error-prone. It is a nickname, not a schema
axis. The truth is `--codex-config-path`. When both are present, the
path wins because it is more specific. The mode is still used for the
field-mismatch assertion (`--codex-config-mode a --codex-config-path <b's
config>` MUST exit 2 — the operator lied about which mode this was) and
for the env-var precondition (§7.d).

### 5.3 Why we sleep 30 s between runs

The local proxy in prod (`modelserver`) throttles at roughly 1 request
per 10 s per client; upstream direct behaves similarly under load. A
30 s gap gives both paths a full recovery window and avoids the "run 3
timed out because the previous two exhausted the token bucket" latency
inflation. The number is a magic constant in the harness; the reviewer
comment explains it and points at the runbook line that documents the
token-bucket shape.

### 5.4 Why hops-loopback is a helper, not part of the oracle

`oracle.sh` is the workload's contract — it says "the completion is
correct, the credential did not leak, the trace has the required
fields." The mode-specific proxy topology is an **evaluation** concern:
it is what distinguishes credential-a from credential-b, not what
distinguishes credential-bound-model from any other workload. Keeping
it in a sibling script preserves the workload's ability to be run
outside our two-path evaluation harness.

---

## 6. Interfaces to downstream consumers

### 6.1 The CSV emitted by the runner

Two runs → two CSV files. The consumer (`ModelProxyOverhead` scorer)
distinguishes them by:

- Filename convention imposed by the harness:
  `/tmp/wt2-modelproxy-<mode>-<i>.csv`. This is a harness convention,
  not a runner contract.
- `codex_config_path` column (already present per PR #53). The distinct
  path values are:
  - mode a: `.../driver-codex-local/.codex/config.toml`
  - mode b: `.../driver-codex-local/codex-config.toml`
- `baseline_or_ablation` label inserted by the downstream persister
  (§2.5): `credential-a` / `credential-b`.

Any one of the three identifies the mode; the harness prints all three
in its final summary so a human comparing CSVs sees redundant labels.

### 6.2 The `hops` schema

Not defined by this worktree — the workload's Phase 0 README documents
the field as `["driver","model_proxy",...]`. The mode-a check reads
each element as either a string (legacy shape) or an object with a
`proxy_addr` key (extended shape). The mode-b check only vetoes the
extended-shape object shape whose `proxy_addr` matches the loopback
allowlist; legacy string-only hop arrays are silently ignored by the
mode-b check (they carry no address information to falsify).

---

## 7. Security & safety

### (a) Config/mode mismatch → exit 2

If `--codex-config-mode a` is combined with a config whose provider block
contains `env_key = "OPENAI_API_KEY"`, the runner returns
`ErrCodexConfigMismatch` at pre-flight. Symmetrically, `--codex-config-mode b`
+ a config with `experimental_bearer_token = "..."` is rejected.

The check inspects lines that (a) are not preceded by a `#` comment
character on the same line and (b) match `^\s*(experimental_bearer_token|env_key)\s*=`.
Both fields, one field, or neither field may be present:

| Config fields present         | Mode a verdict | Mode b verdict |
|-------------------------------|----------------|----------------|
| `experimental_bearer_token`   | pass           | reject (mismatch) |
| `env_key`                     | reject (mismatch) | pass       |
| both                          | reject (mismatch, ambiguous) | reject (mismatch, ambiguous) |
| neither                       | reject (mismatch, no auth field) | reject (mismatch, no auth field) |

Rationale: the E2E runbook (`prod_test/E2E_RUNBOOK.md` line 266) has a
first-hand report that misconfigured `env_key` against the local proxy
silently sends the wrong bearer. This is exactly the fake-data-emitting
failure mode that would poison the `ModelProxyOverhead` measurement.

### (b) `--codex-config-path` sandboxing

The resolved absolute path MUST satisfy
`filepath.HasPrefix(resolved, repoRoot) || filepath.HasPrefix(resolved, "/tmp/")`.
`repoRoot` is computed from the runner binary's working directory by
walking up to the nearest `go.mod` (same technique as
`findRepoModuleRoot` in the existing tests). Paths outside both
prefixes exit 2 with `ErrCodexConfigPathForbidden`.

Rationale: without this, `--codex-config-path /etc/shadow` would let a
malicious workload driver point the runner at arbitrary root-readable
files. Even though §7(c) guarantees file contents never enter error
messages, an off-path config is still an operator-error signal worth
rejecting BEFORE the file is opened — that closes the class of attack
where the file's mere READ (permission-required, audit-logged, or
having open side-effects such as `/proc/*` triggering) is the payload.
`/tmp/` is on the allowlist because CI writes ephemeral configs under
`t.TempDir()` (which is under `/tmp/` on Linux).

### (c) Config contents never leak into logs

The runner logs `codex-config: mode=<a|b> path=<path> auth_field=<name>`
and nothing else about the file. Token values, JWTs, env var names other
than the two we assert on — none of these are emitted to stdout, stderr,
the CSV row, or the observer database.

Error messages produced by the validator name the PATH only. They
NEVER include bytes read from the config — not truncated, not
snippet-quoted, not hex-dumped. The only piece of file-derived state
that surfaces is the boolean answer to "which of the two known
field keys appears" (`experimental_bearer_token` / `env_key`), which
carries no credential material. If the file cannot be opened
(permission denied, does not exist, exceeds the 1 MiB size cap), the
error message contains the path and the OS error string only.

Rationale: an eval CSV lives forever in a repo or artifact bucket;
tokens in it are indistinguishable from tokens in production. The
oracle's leak grep would flag `sk-…` / `eyJ…` in an artifact but the
CSV is EMITTED by the runner itself — it is upstream of the oracle,
and no one greps eval outputs for leaks a second time. A truncated
"128-byte diagnostic snippet" would also live in the CI log stream
that captured the failing run — bytes we cannot recall are bytes we
cannot afford to touch.

### (d) mode=b env-var precondition

When `--codex-config-mode b` is set (or `--codex-config-path` points at
a config with `env_key = "OPENAI_API_KEY"`), the runner asserts at
pre-flight that:

- `os.Getenv("OPENAI_API_KEY")` is non-empty; AND
- the value is not one of the sentinel strings:
  `sk-fake`, `test-key`, `` (empty), `changeme`, `x`, `sk-XXXXXX…`
  (any run of ≥6 `X` characters after `sk-`).

If either check fails, the runner returns `ErrOpenAIKeyMissing` (exit
2) with a message that names the failed check but NOT the observed
value (§7.c).

Rationale: `OPENAI_API_KEY=test-key bash eval-modelproxy-overhead.sh`
would run to completion, hit a synthetic auth failure, and record a
`duration_ms` that is entirely fabricated (mostly TCP handshake +
response body serialisation). That number, subtracted from a real
mode=a latency, would produce a `ModelProxyOverhead` figure that is
believable and wrong. The check refuses to run rather than emitting
poisoned data.

### (e) Same-prompt sampling + 30 s sleep

The harness pins the workload's prompt to `fixtures/prompt.txt` (the
Phase 0 fixture — one file, checked into git, never overridden by the
harness) and passes `--run-id wt2-modelproxy-<mode>-<i>-<epoch>` so
the two paths differ only in `<mode>` and iteration number. Between
adjacent runs it sleeps 30 s (§5.3).

**On "seed"**: the codex CLI (the upstream Rust binary the runner
delegates to via the workload's agent stage) does NOT expose a
sampling-seed knob at either its command-line or its `.codex/config.toml`
provider level as of the version pinned in this worktree's base ref.
"Same seed" in this spec therefore means "same everything the harness
CAN pin": same prompt, same model, same provider (the two paths
differ ONLY in whether the model provider is the local proxy or
upstream direct), same run env-var whitelist. Sampling temperature and
any internal RNG state remain provider-controlled. The harness cannot
assert on those; the paper's methodology section documents this as a
known measurement noise floor.

The harness DOES assert what it can pin, via three concrete guards:

1. The prompt file MUST be `fixtures/prompt.txt` and MUST exist. If
   the harness cannot stat that file, it exits 1 before any run.
2. Every run's `codex_config_path` column MUST resolve to a file that
   the harness itself computed from the mode (mode=a →
   `.codex/config.toml`, mode=b → `codex-config.toml`). A drift
   between the CSV column and the mode aborts the sample set.
3. The 30 s sleep is a fixed constant in the harness — an in-script
   assertion (`sleep 30` with the literal `30`) rather than a
   configurable interval, so a drive-by tunable can't shrink it.

The script's header comment states this in Chinese and English:

```
# 同 prompt 双跑 harness — Same-prompt double-run.
# Two runs per sample: mode=a and mode=b, using the SAME prompt fixture
# and the SAME provider stack (only the codex config differs). The
# codex CLI does not expose a sampling seed at this version, so "same
# seed" means "same everything we can pin"; sampler noise is the
# measurement's known floor.
# Any drift (prompt edited, sleep shortened, provider stack changed)
# invalidates the ModelProxyOverhead measurement.
```

Rationale: a reviewer skimming the harness in five seconds needs to see
the invariant, INCLUDING the honest "we cannot pin sampler seed"
caveat. Without it, someone reading `--codex-config-mode` would
reasonably expect a `--seed` option and, finding none, might silently
add one that lies (e.g. sets it in one path but not the other).

### (f) Loopback host allowlist — exact match, not substring

`hops_loopback_check.sh` compares the host component of `proxy_addr`
against `{"127.0.0.1", "::1", "localhost"}` with EQUALITY. The check
splits `host:port` on the LAST `:` (so IPv6 addresses like `::1` are
handled — the check strips a numeric-only trailing `:port` first, then
compares). A substring check would let `127.0.0.1.evil.com` masquerade
as loopback; the exact-equality test rejects it.

The check does NOT resolve DNS. `localhost` is on the allowlist as a
literal string; if `/etc/hosts` maps it to a non-loopback address that
is a system-level compromise the eval harness will not detect (nor is
it supposed to — the harness measures overhead, not host security).

### (g) Consumer-view reverse audit

Scenario: you are the `ModelProxyOverhead` scorer reading the observer
`runs` table. Given a pool of persisted rows, can you separate the
mode=a rows from the mode=b rows?

Answer: yes, via `baseline_or_ablation` — the field carries the value
`credential-a` (mode a) or `credential-b` (mode b), documented in §2.5.
That alone is sufficient; the scorer's SQL is
`WHERE baseline_or_ablation IN ('credential-a','credential-b')` and
GROUP BY that column.

Backup identifiers, in case the persister omits the label:

- `workload_id = 'credential-bound-model'` narrows to this workload.
- `codex_config_path` contains `.codex/config.toml` for mode a and
  `codex-config.toml` (no leading dot dir) for mode b. This is a
  fragile identifier (an operator with a differently-named config
  file would break it) — the primary identifier is
  `baseline_or_ablation`.

If neither the primary nor the backup identifier is present, the pool
is unresolvable and the scorer MUST refuse to score. The scorer's
own tests will assert this refusal — that spec lives with the scorer,
not here.

### (h) CI-mode perf assertion

CI runs `eval-modelproxy-overhead.sh --dry-run` only. It does NOT
assert on the value of `ModelProxyOverhead` — real measurement
requires real credentials, real network, and a stable host, none of
which CI has. The measured `ModelProxyOverhead` gate is a manual
operator step documented in the harness's `--help` text.

Rationale: a CI job that asserted `ModelProxyOverhead < 500 ms` would
be red every time the upstream had a bad minute — the assertion would
be measuring the upstream network, not the paper's claim. The
assertion belongs in the paper's methodology section, not in CI.

---

## 8. Test surface (summary — full matrix in the Plan doc)

- `codexconfig_test.go` — 8-row table matrix covering §7.a (both modes ×
  both fields), §7.b (path traversal), §7.d (sentinel key rejection).
- `hops_loopback_check.sh` — self-tests with two positive fixtures
  (mode=a with loopback hop, mode=b without) and two negative
  fixtures (mode=a with `127.0.0.1.evil.com`, mode=a with empty hops).
  These live under the workload's `eval/testdata/` subdirectory.
- `eval-modelproxy-overhead.sh --dry-run` — smoke-checks that the
  harness constructs sane command lines for each mode without
  spawning the runner.

---

## 9. Non-goals

- Statistical significance testing of `ModelProxyOverhead` — the
  harness emits raw sample values, downstream tooling does the stats.
- Multi-agent / multi-slave fan-out — this workload is single-driver
  by design (Phase 0 §C3).
- Replacing the `oracle.sh` grep with a real secret scanner — that
  work is scheduled for `p3-fault-injection` per the TODO in
  `oracle.sh`.
- Persisting `baseline_or_ablation` to the `runs` table from the
  runner itself — that is a schema-writer change, out of scope here.
