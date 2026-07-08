# WT-4-codex-only — Handoff

Companion to `wt4-codex-only.spec.md` + `wt4-codex-only.plan.md`.
Records everything this worktree CONSCIOUSLY does NOT do, so the
next reviewer / operator / follow-up worktree knows what to pick up.

## Follow-up worktrees required before Phase 4 real-run

### Route-b support (agent-only credential forwarding)

The credential path (b) — `env_key = "SOME_NAME"` in
`~/.codex/config.toml [model_providers.modelserver]` — is DETECTED
in preflight but NOT ACCEPTED as satisfying `credential-bound-model`.

Reason: the existing `tests/eval/baselines/harness/env.go`
`LOOM_` passthrough (`filterAndInject isLoomNS`) forwards to BOTH
agent AND oracle. Using a `LOOM_`-prefixed env-key would leak the
secret to the oracle subprocess, breaking T1 "no implicit credential
forwarding to oracle".

Follow-up worktree TBD adds an agent-only forwarding path. Two
candidate designs:
1. New `alwaysAllowedIfSetEnvKeysAgentOnly` map in `harness/env.go`
   consumed by `WhitelistEnvForAgent` but NOT `WhitelistEnvForOracle`.
2. New `--forward-modelserver-env-key <NAME>` opt-in flag on
   `single_machine_codex/main.go` that propagates the named var to
   the agent only. Symmetric to `--forward-openai-api-key`.

Design choice deferred; this PR does not pre-commit.

Paper impact: `paper_outputs/evaluation_v3.md` §6.1.3's dual-config
claim ("path (a) local proxy AND path (b) workspace-scoped credential
alias") cannot be evaluated on codex-only until this lands. Mention
this deferral in the §6.1.3 footnote or move the dual-path claim to
§Threats to validity.

### `run.sh --reps` / `--config-subset`

Phase 4 p4-experiment-plan.md §"每个 runner script 的统一形态"
listed `--reps` and `--config-subset` alongside `--dry-run` /
`--results-root`. This PR ships `--dry-run`, `--results-root`,
`--sample N`, `--workload` — but NOT `--reps` and `--config-subset`.

Reason: `run.sh` already has `--sample N` + `--resume` + `--parallel`
covering related semantics; converging them is a design step outside
a rename PR.

Follow-up worktree adds these when the real-run planning starts.
Wrappers reject unknown flags via `run.sh`'s existing "unknown flag"
handler, so accidentally passing `--reps 3` today exits 2 with a
clear message — no silent no-op.

## Paper-writing repo — separate PR (not this worktree)

24 occurrences of `single_machine_claude_code` in the paper drafts:
- `paper_writing/paper_outputs/motivation_v3.md` — 9 occurrences
- `paper_writing/paper_outputs/evaluation_v3.md` — 15 occurrences

Also `paper_writing/paper/main.tex` and `paper/main_cn.tex` currently
have 0 occurrences; a CI check that greps `main*.tex` for the old
label and fails on hit is a good defensive addition — put it in the
paper_writing repo's CI, not here.

Small PR outside this worktree: `sed -i 's/single_machine_claude_code/single_machine_codex/g' paper_outputs/{motivation,evaluation}_v3.md` + a CI grep guard.

## Consciously NOT touched (record-only, no follow-up needed)

- `multi-agent/internal/driver/slave_tools.go` — the tool names
  `get_slave_claude_permissions` / `update_slave_claude_permissions`
  are stable API surface (LLM-facing tool name), not implementation.
  Backend is already agent-kind-agnostic (`LOOM_AGENT_KIND=codex`).
- `multi-agent/compose-test/entrypoint-driver.sh` — dev-only test
  scaffold; default agent is `claude` because that's what the dev's
  laptop has installed. Codex support is opt-in via env var.
- `mock-model/mock-claude-opus-4-8/` — provider label is referential
  (identifies the mock's target). Codex's mock provider is a separate
  fixture; renaming would confuse existing usage.
- `paper_outputs/related_work_outline.md` mentions of "Claude Code /
  Codex" — these describe products, not baselines. Unchanged.
- `tests/eval/baselines/single_machine/` — the Claude baseline is
  KEPT for reference / paper §Related Work / hypothetical reviewer
  re-run. Only removed from the fulltable matrix.
- `tests/eval/baselines/README.md` — retains the `single_machine_claude_code`
  row as a reference; the `single_machine_codex` row is added
  alongside as the active baseline.

## Findings recorded but not fixed here

### From spec review (5 rounds)

- P2#1 (Windows guard test table) → UPGRADED to acceptance test in Task 19.
- P2#2 (`FULLTABLE_RUN_SHIM` naming) → UPGRADED to
  `LOOM_FULLTABLE_WRAPPER_SHIM` rename + dual-guard in Task 11.
- P2#3 (snapshot baseline path exactness) → UPGRADED to acceptance
  test in Task 8.
- Stale layout comment in spec §2 → fixed round-5.

### From plan review (9 rounds)

- Fixture tokens rename to `sk-TEST-NOT-REAL-*` — applied in Task 13.
- All shell tests use `mktemp -d` per test invocation — applied progressively.
- `secretscrub.Sanitize` truncation (256-rune cap) — accepted as-is for
  leak prevention; broader coverage is out of scope for a rename PR.

### From code review (Phase A/B/C/D, ~2 rounds each)

- **Phase A P0**: Bearer scrub via local `bearerRE` in impl.go (avoids
  modifying shared secretscrub).
- **Phase B**: no P0/P1. P2 noted: snapshot leak patterns narrower than
  secretscrub — snapshot test scope is intentionally narrower (T4
  patterns only).
- **Phase C P0**: run.sh workload allowlist regex-bypass + empty string —
  fixed to exact match + reject empty in r1.
- **Phase C P1**: resume/E4 test assert-11 exact — fixed in r1.
- **Phase D P1**: fake codex silence — fixed with IMPOSSIBLE sentinel + scan.
- **Phase D P2**: equals-form injection tests, windows unguarded uname —
  fixed in r1.
- **Phase D P2 remaining** (recorded): credential preflight leak checks
  are per-case, not global sweep of all fixture output. Direct probe
  confirms no leak; global sweep is test-hardening for future.
- **P3 (recorded)**: wrapper script headers say `cross_device_code_mod.sh`
  in non-cross-device wrappers (sed clone artifact). Cosmetic.

## Cross-repo pinning

The paper_writing 24-md swap PR should reference THIS worktree's PR
commit sha (record after merge) so the paper-writing reviewer can
verify the enum landed on the multi-agent side before they merge
the sed swap. Once both merge, the `test_no_claude_baseline_repo.py`
guard here + a symmetric grep guard in paper_writing prevent
regression.

## Repository-level acceptance checklist (spec §7 mirror)

- [x] `go test ./tests/eval/baselines/single_machine_codex/... -race` — 9 tests green
- [x] `go test ./tests/eval/baselines/... -race` — full sweep green (5 packages)
- [x] `pytest tools/eval/fulltable/tests/ tools/eval/experiments/tests/ -q` — all green (159 tests)
- [x] `bash tools/eval/fulltable/run.sh --dry-run` — no `single_machine_claude_code` in output
- [x] `bash tools/eval/fulltable/run.sh --workload cross-device-code-mod --dry-run` — exactly 12 rows
- [x] 5 wrappers `--help` → exit 0
- [x] 5 wrappers `--dry-run` (with codex + config preflight satisfied) → dispatch + correct `--workload` filter
- [x] `windows_only_artifact.sh` on Linux without `--skip-if-not-windows` → exit 2
- [x] `windows_only_artifact.sh --skip-if-not-windows` on Linux → exit 0 with warning
- [x] `credential_bound_model.sh` on host without `~/.codex/config.toml` → exit 2, no token leak
- [x] Repo-wide grep of `single_machine_claude_code` in `multi-agent/{tests,tools}/` returns only whitelisted paths (single_machine/ dir + baselines/README.md + test's own source)
- [x] Codex code review has no unresolved P0/P1 across Phase A/B/C/D
