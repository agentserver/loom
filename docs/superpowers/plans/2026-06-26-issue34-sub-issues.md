# Issue #34 Sub-Issue Breakdown

> Parent: https://github.com/agentserver/loom/issues/34
>
> Audit worktree: `issue34-audit`
>
> Audit date: 2026-06-26 (revised after strict review)
>
> Purpose: turn issue #34 into independently trackable GitHub sub-issues. Each item below is scoped to a PR-sized or milestone-sized unit with **Hard / Soft dependencies**, **labels**, **estimated PR count**, and concrete acceptance criteria.

## Conventions

- **Hard deps:** must merge before this item can start.
- **Soft deps:** improves the work if landed first but not blocking.
- **Estimated PRs:** rough split count; milestone-sized items list checkpoints.
- **Labels:** apply at issue-creation time from the label set below.
- **Path prefix:** every `internal/...`, `cmd/...`, `pkg/...`, `tests/...` (including `tests/prod_test/`), `scripts/...`, `examples/...` reference is relative to `multi-agent/` (the Go module root, `github.com/yourorg/multi-agent`). Paths under `docs/`, `skills/`, and `.github/` are repo-root relative.

## Prerequisites Before Filing Sub-issues

- **Create the required GitHub labels first.** The label set below does not exist on the repo yet (`gh label list` shows only the stock set: `bug`, `documentation`, `enhancement`, `good first issue`, …). Running `gh issue create --label roadmap` today would fail or silently drop the label. One-time bootstrap (replace colors as desired):

  ```sh
  gh label create roadmap --color BFD4F2
  gh label create architecture --color 5319E7
  gh label create capability-routing --color 0E8A16
  gh label create codex-entry --color FBCA04
  gh label create control-plane --color 1D76DB
  gh label create security --color B60205
  gh label create prod-test --color C5DEF5
  gh label create docs --color 0075CA
  gh label create good-first-subissue --color 7057FF
  ```

- **CI scope note.** `.github/workflows/multi-agent.yml` only triggers on `multi-agent/**`, `skills/**`, and the workflow files themselves. Repo-root `docs/**` and `tests/prod_test/**` *outside* `multi-agent/` are not in the trigger list. A pure-docs PR like this plan or item 09 lands with `gh pr checks` reporting *no checks* — expected, do not block. Items that *do* edit Go code or `multi-agent/**` content (10 driver tool descriptions, 17 store init, 19 dynamicmcp marshalling, 21 driver tool description cross-links, etc.) **will** trigger CI; do not assume "the doc says it's docs-flavoured" means CI is skipped — read the diff. If you want docs lint to gate, add a separate lightweight workflow rather than widening the Go test trigger.

## Current Baseline From Code Audit

### Already Landed / Do Not Re-open As Sub-Issues

- Planner 1.5 is present: injection boundaries (`internal/planner/planner.go:26`), schema-validated retry (`planner.go:123-215`), target-id validation (`planner.go:88-115`), provider-agnostic backend path via `agentbackend.LLMRunner`.
- `agentbackend.Backend` is unified across claude/codex/opencode (`pkg/agentbackend/backend.go:35-56`); all three implement the core backend interface.
- `SessionWorkerBackend` exists (`pkg/agentbackend/backend.go:171-173`), but only codex implements it (`pkg/agentbackend/codex/appserver_manager.go:23`).
- `register_mcp` and `unregister_mcp` baseline is present: driver tools delegate to slave skills (`cmd/slave-agent/main.go:307-323`), dynamic MCP persists in `dynamic_mcp.yaml` (`internal/executor/dynamicmcp.go`), slave card republish exists (`internal/executor/registermcp.go:116-119`).
- Observer 401 cooldown queueing (`internal/observerclient/client.go:160-193,249-282`), server-side 5-minute re-register 409 + client-side 60s cooldown + `force` (`client.go:21,44,46`), userspace BlobStore TOCTOU / dual object-key path fixes (`internal/userspace/blob.go:49-113`).
- Security items H1-H3 are already implemented:
  - File jail with symlink resolution in `internal/executor/file.go:19-50,79-109`.
  - MCP HTTP timeout and `LimitReader` in `internal/executor/mcp.go:21,47,276,281`.
  - Permissions patch `*` / empty-entry rejection in `cmd/slave-agent/permissions_executor.go:13-42,72`.

### Still Open In Code

- `cmd/master-agent/` still exists and still builds; live references remain in `scripts/agents.sh`, all three `examples/*/scripts/e2e.sh`, `tests/scripts/agents_script_test.go`, `tests/runtime/README.md`.
- `RoutingMasterOnly` / `master_only` remains in contract (`internal/contract/types.go:13`), driver capability tools (`internal/driver/capability_tools.go:121,232`), driver/orchestrator tests, and `examples/dynamic-mcp/scenario_test.go`.
- `internal/orchestrator/` and `internal/orchestration/` still both exist; `cmd/driver-agent/main.go:198` uses `orchestration.NewDriverRunner`.
- No canonical `internal/agentcard/` package — card parsing is duplicated across ≥8 sites: `internal/driver/agent_card.go`, `internal/driver/tools.go:352,374,425`, `internal/driver/slave_tools.go`, `internal/driver/capability_tools.go:290`, `internal/orchestrator/route.go:128`, `internal/orchestration/plan_semantics.go:78,83`, `internal/planner/prompts.go:239,265`, `internal/contract/snapshot.go:29`, `internal/capability/types.go:40` (`ExtractFromAgentCard`).
- Agent card carries `MCPTools` as `json.RawMessage` but has no structured `data` field; `contract.CapabilityRequirements` has no `data` or `mcp_servers`.
- Runtime dispatch (`internal/dispatch/dispatch.go:59-162`) still routes by `task.Skill` only, no tools/MCP/data capability matching.
- Planner still receives full agent lists (`internal/planner/planner.go:187` → `prompts.go:182`); no capability prefilter.
- `cancel_task` is a stub returning `ok:false` (`internal/driver/tools.go:1209-1237`); `nudge_task` does not exist anywhere.
- `RequirePlanApproval` and `RequireUserApprovalForRepoWrites` are defined in `internal/contract/types.go` but only referenced by `contract/validate.go` (no orchestrator/driver consumer).
- No `map_reduce` node kind, route retry budget, observer event `seq`, artifact/write two-phase commit, userspace WAL/busy timeout, or humanloop lock GC. (`chat_resume` defers `lock.Unlock()` but never `os.Remove`s the lock file — see `internal/platform/filelock.go:16-27`.)
- Codex app-server worker still requires `LOOM_CODEX_APPSERVER_UNSAFE_HUMANLOOP_ROUTING=1` (`pkg/agentbackend/codex/backend.go:13,111`).
- Driver MCP surface is **23 tools** (`internal/driver/tools.go:238-265`) with pre-codex master/fanout terminology.

## Label Set

- `roadmap`
- `architecture`
- `capability-routing`
- `codex-entry`
- `control-plane`
- `security`
- `prod-test`
- `docs`
- `good-first-subissue` (narrow docs/test-only items only)

## Dependency Graph (Hard edges only)

```text
01 → 02 → 03
03 → 04 → 05 → 06 → {07, 08, 14}
07 → 15
09 → {10, 11, 12}
10 → 20
12b → 22
11 → 22
13 ← 03
16, 17, 18, 21 — independent (no hard deps; 16/17/18 soft-prefer 01)
19 ← 09
```

Soft edges are noted per-item under **Soft deps**. The earlier draft had two contradictions, both fixed: (1) `11 → 12` was a cycle since item 11 also listed 12 as preferred — 12 now depends on 09, and 11 and 12 are siblings whose order is operator-chosen; (2) `09 → 10` was tagged "soft-precedes" in the graph while item 10 listed 09 as Hard — now a hard edge consistent with item 10. Items 16, 17, 18, 21 are explicitly independent (no hard deps); 16/17/18 soft-prefer 01.

## Phase Tracks

Items run on three parallel **tracks**, not sequential phases. Track labels replace the old "Phase Wn" weeks (which were inconsistent — "W1-W9" appeared after "W2-W5" and "W12+" was reused three times).

- **Track A — Topology cleanup:** 01, 02, 03
- **Track B — Capability model & routing:** 04, 05, 06, 07, 08, 14, 15
- **Track C — Codex entry & backend parity:** 09, 10, 11, 12, 22
- **Track D — Control plane backchannel:** 13
- **Track E — Durability & security hardening:** 16, 17, 18
- **Track F — Prod test & docs:** 19, 21
- **Track G — Capability crystallization:** 20

## Track A — Topology cleanup

### 01. Remove `master-agent` build/runtime entrypoints

**Status:** TODO
**Labels:** `roadmap`, `architecture`
**Estimated PRs:** 1

**Problem:** The repository still ships `cmd/master-agent/`, script targets, examples, runtime docs, and tests that build/start `master-agent`.

**Scope:**

- Remove or deprecate `cmd/master-agent/` as a runnable process.
- Remove build/install/start/stop paths from `scripts/agents.sh`, prod/runtime docs, deploy templates, and example scripts (image-pipeline, dynamic-mcp, generic-driver).
- Mark historical master design docs as deprecated instead of deleting them.
- Keep historical docs readable; stop treating master as a live path.

**Non-goals:** removing the `master_only` enum value (covered by 02).

**Acceptance:**

- `go test ./...` no longer compiles `cmd/master-agent`.
- `rg "go build .*cmd/master-agent|bin/master-agent|master-agent config"` matches only files under `docs/history/` (new) or `cmd/master-agent/README.md`'s deprecation banner.
- `scripts/agents.sh` no longer starts a master process.
- The existing topology-describing surfaces — repo-root `README.md` and `README.en.md`, `docs/superpowers/ROADMAP.md`, and `multi-agent/tests/runtime/README.md` — state that supported topology is driver + slave. (`docs/README.md` does not exist today; if creating it is desired, list it in this item's scope and create explicitly, otherwise update the four files above only.)

**Hard deps:** none.
**Soft deps:** none.

### 02. Collapse `master_only` routing to `direct_first`

**Status:** TODO
**Labels:** `roadmap`, `architecture`
**Estimated PRs:** 1 (compat window) + 1 (removal) = **2**

**Problem:** `RoutingMasterOnly` and `master_only` still force LLM/tool users to reason about a removed architecture.

**Scope:**

- PR 1 (compat): keep `master_only` parseable; normalize to `direct_first` at validation/parse boundary. Remove master-candidate / fanout-master selection from `dry_run_contract` and `submit_contract_task`.
- PR 2 (removal, after one release): delete `RoutingMasterOnly` constant and migration of remaining tests/examples.
- Update tests in `internal/driver/tools_test.go`, `internal/orchestrator/route_test.go`, `examples/dynamic-mcp/scenario_test.go`.

**Acceptance:**

- After PR 1: `contract.RoutingMasterOnly` is marked deprecated; validation normalizes to `direct_first` and a deprecation warning is logged once per process.
- After PR 1: `dry_run_contract` never returns `master_fanout`; one test asserts the normalization path.
- After PR 2: `rg "RoutingMasterOnly|master_only"` matches only `CHANGELOG.md` migration notes.

**Hard deps:** 01.
**Soft deps:** none.

### 03. Merge `internal/orchestrator` and `internal/orchestration`

**Status:** TODO
**Labels:** `architecture`
**Estimated PRs:** **2** — PR 1 move + behavior parity tests, PR 2 delete the old serial path.

**Problem:** Two scheduling loops still exist. New capability routing, cancel, approval, retry, and map/reduce would need duplicate changes.

**Scope:**

- PR 1: move the active fanout runner from `internal/orchestrator` into `internal/orchestration`. Move route, contract policy, observer artifact helpers, resume helpers, and tests. Keep `internal/orchestrator/` as a forwarding shim with `// Deprecated` markers.
- PR 1: `cmd/driver-agent` wires the unified runner; existing fanout/orchestration tests pass after move.
- PR 2: delete the old `internal/orchestrator/driver_runner.go` serial path; remove forwarding shim.

**Acceptance:**

- After PR 1: there is exactly one scheduler/runner package for contract DAG execution in production paths.
- After PR 1: `rg -l '"github.com/yourorg/multi-agent/internal/orchestrator"'` matches only the shim file or `_test.go`.
- After PR 2: `internal/orchestrator/` is deleted.

**Hard deps:** 01, 02.
**Soft deps:** none.

## Track B — Capability model & routing

### 04. Add canonical `internal/agentcard` parser

**Status:** TODO
**Labels:** `architecture`, `capability-routing`
**Estimated PRs:** 1

**Problem:** Agent card parsing is duplicated across ≥8 hot spots (see Baseline).

**Scope:**

- Create `internal/agentcard` with `Parsed` and `Parse(json.RawMessage)`.
- Cover skills, tools, structured MCP tools, resources, platform, command interfaces, short_id.
- Replace ad-hoc card unmarshalling at each named hot spot in one PR.

**Acceptance:**

- All ad-hoc card-schema parsing is consolidated into `internal/agentcard`. Specifically:
  - `rg "json.Unmarshal\\(.*Card|parseAgentCard|parseAgentCapabilities"` outside `internal/agentcard/` matches only `_test.go` fixtures.
  - `internal/capability/types.go:40 ExtractFromAgentCard` either moves into `internal/agentcard` or is reduced to a body that delegates to `agentcard.Parse(card)` (acceptance: `rg "json.Unmarshal" internal/capability/types.go` returns no hits — wrapper is fine, but it must not re-parse).
  - `internal/planner/prompts.go agentsJSON` is allowed to keep its name as a production prompt formatter, but its body must consume `agentcard.Parsed` (acceptance: `rg "json.Unmarshal" internal/planner/prompts.go` returns no hits).
- Unit tests cover missing/null/malformed cards and known field extraction.
- No behavior regression in `list_agents`, planner prompts, dry-run, route validation (existing tests for those flows still pass unchanged).

**Hard deps:** 03 (avoids merge collisions with shim move).
**Soft deps:** none.

### 05. Add structured `data` and `mcp_servers` declarations

**Status:** TODO
**Labels:** `architecture`, `capability-routing`
**Estimated PRs:** 1

**Problem:** Routing cannot answer "which machine has which rough class of data?" or match server names because card and contract lack first-class fields.

**Scope:**

- Add `DataAsset` and `MCPServerInfo` to parsed agent card model.
- Add `DataRequirement` and `MCPServers` to `contract.CapabilityRequirements`.
- Add config/env input path for slave data assets.
- Publish `data` and `mcp_servers` in slave discovery cards.
- Keep `data.kind` as an open enum; do not add per-file metadata.
- Document a one-release compatibility window where missing fields parse as empty.

**Acceptance:**

- Slave card can advertise data like `notes`, `finance`, `dataset`, `model`.
- Contract validation accepts `capability_requirements.data` and `mcp_servers`.
- Existing cards without these fields remain valid (golden test).
- Tests cover the private-tag invariant expected by 06's matcher.

**Hard deps:** 04.
**Soft deps:** none.

### 06. Implement `agentcard.Match` and use it in dispatch decisions

**Status:** TODO
**Labels:** `architecture`, `capability-routing`
**Estimated PRs:** **2** — PR 1 matcher + dry-run wired; PR 2 route + contract policy switched over.

**Problem:** Dry-run checks tools/resources, route checks only skills, contract policy mostly checks target allowlists.

**Scope:**

- Add `func Match(card Parsed, req contract.CapabilityRequirements) (ok bool, missing []string)`.
- Match skills, legacy tools, structured MCP tools/server names, resources, and data requirements.
- Enforce private data rule: a `DataAsset` tagged `私密` is matchable only when the contract explicitly requests `by_tags: ["私密"]`.
- Replace dry-run, route target selection, and contract target validation with the same matcher.

**Acceptance:**

- Dry-run and actual dispatch produce identical eligibility verdicts for the same `(card, req)` pair (table-driven test).
- Missing reasons include field-specific entries such as `skill:bash`, `mcp_server:foo`, `data.kind:finance`.
- After PR 2: no production code path performs skill-only matching outside `internal/agentcard.Match`. Concretely:
  - The legacy `agentHasRequiredSkills` helper (today at `internal/orchestrator/route.go:121`, called from `selectAgentForTask` at line 98) is deleted regardless of whether 03 has already moved or removed `internal/orchestrator/`. Phrased as a repo-wide assertion that survives the move: `rg "agentHasRequiredSkills" multi-agent/ --glob '!**/*_test.go'` returns zero hits.
  - The unified route selector — wherever it lives after 03 (`internal/orchestration/...` or the surviving location) — calls `agentcard.Match` and not raw `card.Skills` membership checks. Acceptance: `rg "\\.Skills\\b" multi-agent/internal/{orchestration,orchestrator,dispatch,driver}/ --glob '!**/*_test.go'` either returns zero hits or only the lines inside `internal/agentcard` itself (paths that no longer exist are simply skipped by `rg`, which is fine — the invariant is "no skill-only matcher anywhere in dispatch", not "this specific file still exists").

**Hard deps:** 04, 05.
**Soft deps:** none.

### 07. Planner prefilter and deterministic multi-candidate routing

**Status:** TODO
**Labels:** `capability-routing`
**Estimated PRs:** 1

**Problem:** Planner receives all agents, and multi-candidate routing has no stable policy or fallback budget.

**Scope:**

- Filter agents by `Status == "available"` and `agentcard.Match` before Plan/Route when a contract exists.
- Update prompts to state that candidate agents are prefiltered.
- Add deterministic selection among candidates: first allowed target, otherwise stable hash over task/node id.
- Add `ExecutionPolicy.RouteRetryBudget` and `RouteWaitSeconds` defaults.
- On dispatch failure, fall back to remaining matching candidates within budget.

**Acceptance:**

- Planner test `TestPrefilterOmitsIncapable` proves unavailable or incapable agents are absent from the prompt context.
- Multi-candidate direct dispatch selects deterministically (golden hash test).
- Failure fallback is bounded by `RouteRetryBudget` and emits an observer event with reason.
- No runtime load / ETA / GPU-memory scheduling is introduced.

**Hard deps:** 06.
**Soft deps:** none.

### 08. Data setup and `find_data` task-local skill

**Status:** TODO
**Labels:** `capability-routing`
**Estimated PRs:** **2** — 08a schema + commit tools, 08b `find_data` skill + tests.

**Problem:** Users should not hand-write `data_assets.yaml`, and cards should not become file indexes.

**Scope (08a):**

- Slave-local `data_scan` that scans user-specified roots using metadata only.
- Driver tools `scan_slave_data` and `commit_data_assets`.
- Sensitive path/file blacklist.

**Scope (08b):**

- `find_data` slave-side skill for task-local file discovery by query, kind, tags, mtime, extension.
- Content snippets off by default.
- Test coverage: metadata-only scan, blacklist, private-tag invariant, reset path.

**Acceptance:**

- User can explicitly scan roots and confirm rough categories.
- Committed assets update slave card and trigger card republish.
- Task running on a matched slave can call `find_data` to locate concrete files.
- 08b ships only after 08a is merged.

**Hard deps:** 05, 06.
**Soft deps:** 07 (deterministic routing makes find_data scenarios easier to test).

### 14. Wire approval fields to humanloop

**Status:** TODO
**Labels:** `control-plane`
**Estimated PRs:** 1

**Problem:** `RequirePlanApproval` and `RequireUserApprovalForRepoWrites` exist in contract but are not consumed.

**Scope:**

- Emit `plan_awaiting_approval` after planning when required.
- Add `approve_plan(task_id, approved, edits?)` driver tool.
- Gate repo-write actions through humanloop when `RequireUserApprovalForRepoWrites` is true.
- Preserve existing validation that `repo_commit` requires write approval.

**Acceptance:**

- Test `TestPlanApprovalGate` asserts a contract with `RequirePlanApproval=true` pauses before dispatch and resumes only after `approve_plan(approved=true)`.
- Test `TestRepoWriteApproval` asserts a repo-write action is blocked when the flag is true and unblocks after approval.
- Both events are visible in observer event stream with stable type names.

**Hard deps:** 06.
**Soft deps:** 13 (backchannel plumbing).

### 15. Add `map_reduce` and route retry primitive

**Status:** TODO
**Labels:** `architecture`, `control-plane`
**Estimated PRs:** **2** — 15a `map_reduce` node + expansion; 15b reduce semantics + retry policy hookup.

**Problem:** DAG parallelism does not provide "run same prompt over all matching agents" as a first-class primitive.

**Scope (15a):**

- Add `kind: "map_reduce"` node schema.
- Support `over_agents`, `over_resource`, and `repeat`.
- Expand into sibling nodes using capability matcher; expanded nodes are persisted and resumable.

**Scope (15b):**

- Reduce-node handling or planner guidance for reduce expression.
- Reuse route retry budget from 07.
- Test: failed sibling node respects optional/retry policy (table-driven test enumerates policy×outcome).

**Acceptance:**

- A single `map_reduce` node can run over all agents matching data/skill requirements (integration test).
- Expanded nodes survive a planner restart.
- Failure behavior is asserted by `TestMapReduceRetryPolicy` covering: optional-pass, required-fail-stops-parent, retry-within-budget.

**Hard deps:** 06, 07, 03.
**Soft deps:** none.

## Track C — Codex entry & backend parity

### 09. Codex CLI and Web POCs

**Status:** PARTIAL
**Labels:** `codex-entry`, `docs`, `roadmap`
**Estimated PRs:** 1 (docs-only)

**Problem:** `serve-mcp` prod smoke exists, but issue #34 asks for explicit codex CLI/Web POC docs and misuse findings.

**Scope:**

- Write `docs/codex-cli-poc.md`: codex CLI + `driver-agent serve-mcp` + list slaves + submit + wait.
- Write `docs/codex-web-poc.md`: commander/commanderhub + codex worker turn-state/file-preview flow.
- Capture tool misuse cases and schema friction observed in both paths (input file for 10).

**Acceptance:**

- Both docs include setup, exact commands/config, expected output, and known limitations.
- POC includes at least one end-to-end task through codex CLI run from a clean clone (commands in doc are copy-pasteable).
- Web POC validates turn-state transitions with codex backend.

**Hard deps:** none.
**Soft deps:** 01, 02 (less master-noise in docs).

### 10. Simplify driver MCP tool surface for codex

**Status:** TODO
**Labels:** `codex-entry`
**Estimated PRs:** 1

**Problem:** The driver exposes **23 tools** (`internal/driver/tools.go:238-265`) with legacy master/fanout wording and confusing submit variants.

**Scope:**

- Use findings from 09 to redesign tool names/descriptions.
- Merge or clearly separate `submit_task` and `submit_contract_task`.
- Mark `draft_task_contract` and `dry_run_contract` as planning helpers.
- Remove master/fanout compatibility language from tool descriptions.
- Target ≤ 14 tools after data setup tools are included.

**Acceptance:**

- A `TestToolSurfaceContract` golden test asserts the exact tool list and arities; updates must be intentional.
- `tools/list` exposes no "master only" or "fanout master" language (string assertion in test).
- Backward compatibility aliases are documented in `CHANGELOG.md` if kept.

**Hard deps:** 09.
**Soft deps:** 02 (master-only already normalized), 08 (data-setup tool names known — the "≤ 14 tools after data setup tools are included" target assumes 08a's `scan_slave_data` / `commit_data_assets` are either landed or have agreed-on names; if 10 ships before 08, drop the "after data setup" qualifier from the budget).

### 11. Remove codex app-server unsafe humanloop gate

**Status:** TODO
**Labels:** `codex-entry`, `control-plane`
**Estimated PRs:** 1

**Problem:** Codex is the priority backend, but the hot worker path still requires `LOOM_CODEX_APPSERVER_UNSAFE_HUMANLOOP_ROUTING=1` (`pkg/agentbackend/codex/backend.go:111`).

**Scope:**

- Add codex app-server humanloop smoke coverage (integration test).
- Add metrics/logging for turn latency, RunResume fallback count, and humanloop ack failures.
- Remove the unsafe env requirement once tests pass; keep an opt-out env for rollback.
- Document rollback/fallback behavior.

**Acceptance:**

- `worker_mode: app_server` enables codex hot worker without `LOOM_CODEX_APPSERVER_UNSAFE_HUMANLOOP_ROUTING`.
- Humanloop routing is covered by smoke/integration tests in `pkg/agentbackend/codex/`.
- Metrics/logs distinguish hot-worker success from fallback (assertable in test).

**Hard deps:** 09.
**Soft deps:** 12 (cleaner status constants make tests simpler).

### 22. SessionWorkerBackend implementations for claude and opencode

**Status:** NEW / TODO
**Labels:** `codex-entry`, `architecture`
**Estimated PRs:** **2** — 22a claude hot worker; 22b opencode hot worker.

**Problem:** The `SessionWorkerBackend` interface (`pkg/agentbackend/backend.go:171-173`) is implemented only by codex (`pkg/agentbackend/codex/appserver_manager.go:23`). claude and opencode workers re-spawn the CLI per turn, so they don't benefit from the hot-worker turn-state machine that codex uses. Burying this under item 12 as "lower priority" violated #34's "independently trackable sub-issues" intent — hot-worker parity is a real backend-equivalence story, not a CI line item.

**Scope (22a — claude):**

- Implement `NewSessionWorker` for `pkg/agentbackend/claude` by attaching to a persistent claude-code (or sdk) process per session.
- Map turn lifecycle to `agentbackend.Status*` events with the same semantics codex uses.
- Honour cancel and humanloop routing parity with item 11's contract.

**Scope (22b — opencode):**

- Same as 22a, against the opencode runtime.

**Acceptance:**

- `pkg/agentbackend/claude` and `pkg/agentbackend/opencode` each declare `_ agentbackend.SessionWorkerBackend = (*workerBackend)(nil)`.
- Within this item: enable the parameterised matrix harness from 12b for `worker_mode: app_server` on whichever backend(s) land here (22a → claude row; 22b → opencode row). Codex's app-server row is already on after 11.
- Hot-worker reuse is observable in logs/metrics (turn N+1 reuses the same worker PID as turn N for the same session).
- Documented opt-out env exists for rollback.

**Hard deps:** 11 (the unsafe-env gate must be removed first; the same humanloop routing contract applies), 12b (the parameterised matrix harness is the test substrate).
**Soft deps:** 12a (status constants stable).

### 12. Backend-neutral turn-state and CI matrix

**Status:** PARTIAL
**Labels:** `codex-entry`, `prod-test`
**Estimated PRs:** **2** — 12a status migration; 12b CI matrix.

**Problem:** Status constants exist, but commanderhub still has text fallbacks and runtime tests remain mostly manual/claude-heavy.

**Scope (12a):**

- All backend turn events use `agentbackend.Status*`.
- Remove codex/claude-specific text matching from commanderhub after compatibility window.

**Scope (12b):**

- Runtime matrix for submit, wait, resume, cancel placeholder, register_mcp, artifact passing, and capability matching across claude/codex/opencode.
- 12b ships as a **cold-path-only matrix** at first (each backend runs in its default spawn-per-turn mode); the matrix harness is parameterised so additional rows for `worker_mode: app_server` can be enabled per backend as item 22 lands. Implementing the hot-worker backends themselves is **out of scope** for 12b — that work lives in 22.

**Acceptance:**

- Commanderhub state machine keys only off status constants and command result envelopes (string-search assertion).
- Tests cover all three backends for core flows or explicitly skip with documented reason.
- CI command is documented in `tests/runtime/README.md` and runnable on a dev machine.

**Hard deps:** 09.
**Soft deps:** 11.

## Track D — Control plane backchannel

### 13. Implement real `cancel_task`, `nudge_task`, and concurrency warnings

**Status:** TODO
**Labels:** `control-plane`
**Estimated PRs:** **3** — 13a cancel; 13b nudge; 13c `concurrency_capped` event.

**Problem:** Users need to interrupt and steer long-running tasks. `cancel_task` is currently a stub.

**Scope (13a):**

- Agentserver/proxy cancel integration or nearest available control endpoint.
- Propagate cancel through scheduler to in-flight child tasks.
- Send graceful termination to backend subprocesses/hot workers.
- Observer events `task_cancel_requested` and `task_cancelled`.

**Scope (13b):**

- `nudge_task(task_id, message, semantic?)` driver tool.
- Route nudges through humanloop or a backend-visible pre-turn injection channel.

**Scope (13c):**

- Emit `concurrency_capped` when requested contract concurrency exceeds config.

**Acceptance:**

- Calling `cancel_task` changes task/subtask state and stops in-flight execution. Test enumerates each lifecycle state (pending / assigned / running / completed) and asserts the documented outcome — `running` returns `cancelled`; `completed` returns `noop`; etc.
- User can send a nudge that is visible to the next backend turn (integration test asserts payload reaches backend prompt).
- Concurrency cap is visible in driver logs and observer events.

**Hard deps:** 03.
**Soft deps:** 09 (codex hot worker cancel testing).

## Track E — Durability & security hardening

### 16. Observer artifact/write two-phase commit and event sequence

**Status:** TODO
**Labels:** `control-plane`
**Estimated PRs:** **2** — 16a two-phase commit + orphan reaper; 16b monotonic `seq` + index migration.

**Problem:** Artifact/write object upload can orphan objects, and event ordering depends on wall-clock timestamps.

**Scope (16a):**

- Artifact/write object flow: DB pending → object upload → DB available.
- Orphan reaper for stale pending/orphan object keys.

**Scope (16b):**

- Monotonic `seq` to SQLite and Postgres observer events.
- Rebuild event indexes around `(workspace_id, seq)` while preserving timestamp queries.
- Migration tested on existing SQLite and Postgres DBs.

**Acceptance:**

- DB failure injected after object upload leaves recoverable pending/orphan state handled by reaper (test).
- Event readers can page/order by `seq`.
- Migration works for existing SQLite/Postgres DBs (CI runs `go test ./internal/observerstore/...` against both backends).

**Hard deps:** none.
**Soft deps:** 01 (less topology churn during schema migration).

### 17. Userspace SQLite WAL/busy timeout and humanloop lock GC

**Status:** TODO
**Labels:** `control-plane`
**Estimated PRs:** 1

**Problem:** Userspace SQLite lacks explicit WAL/busy timeout in its migration/open path, and `chat_resume` leaves `.lock` files forever (`internal/platform/filelock.go:16-27` never `os.Remove`s).

**Scope:**

- Apply userspace SQLite pragmas equivalent to observerstore (`internal/observerstore/store.go:54,71`).
- Remove `<session>.lock` after successful/failed `chat_resume` unlock.
- Add startup cleanup for stale lock files older than configured TTL.
- Increase prod humanloop shutdown grace from 10s to 30s where relevant.

**Acceptance:**

- Userspace DB opens with WAL and busy_timeout in SQLite mode (assertion in store init test).
- Lock files do not remain after normal resume (integration test counts files in `FlockDir`).
- Stale lock cleanup is tested.

**Hard deps:** none.
**Soft deps:** 01.

### 18. Marketplace package signing and install restrictions

**Status:** TODO
**Labels:** `security`, `roadmap`
**Estimated PRs:** 1

**Problem:** File jail, MCP timeout, and permissions whitelist are done, but marketplace package signature enforcement is still missing.

**Scope:**

- Ed25519 manifest/tarball signing model.
- Verify signatures before install/publish acceptance.
- Disallow arbitrary `install_script` for marketplace packages.
- Define trust roots for personal userspace vs shared marketplace.

**Acceptance:**

- Unsigned or invalidly signed marketplace package is rejected (test).
- No arbitrary install script path exists for shared marketplace packages.
- Existing userspace/private development flow has an explicit compatibility story documented.

**Hard deps:** none.
**Soft deps:** 01.

## Track F — Prod test & docs

### 19. Prod-test health, codex smoke, and dynamic MCP YAML serialization

**Status:** PARTIAL
**Labels:** `prod-test`
**Estimated PRs:** 1

**Problem:** Prod-test runbook exists, but health and codex smoke scripts are missing. Also, **a current readability bug**: `internal/executor/dynamicmcp.go:20` persists `Tools []capability.MCPToolDescriptor`, whose `InputSchema` field is `json.RawMessage` (`internal/capability/types.go:9`). Default `gopkg.in/yaml.v3` marshalling of `[]byte` (which is what `json.RawMessage` is) emits a base64-encoded `!!binary` blob (or, depending on tag config, a sequence of integers). Bytes round-trip via the existing `UnmarshalYAML` because both forms decode back to the same `[]byte`, but the on-disk `dynamic_mcp.yaml` is unreadable to humans and unusable as configuration — defeating the point of a YAML config file. This is a current correctness/UX bug, not future hardening.

**Scope:**

- Add `tests/prod_test/health.sh`.
- Add `tests/prod_test/codex-smoke.sh`.
- Keep `driver_mcp_e2e.py` as the lower-level stdio JSON-RPC test.
- Implement `MarshalYAML`/`UnmarshalYAML` on `capability.MCPToolDescriptor` (or wrap `InputSchema` in a type with the methods) so the schema serializes as a YAML mapping and round-trips losslessly. Adjust `DynamicEntry.UnmarshalYAML` accordingly.
- Add a regression test in `internal/executor/dynamicmcp_test.go` that registers an MCP tool with a non-trivial JSON schema, marshals to YAML, parses back, and asserts equality.

**Acceptance:**

- One command (`tests/prod_test/health.sh`) validates driver token, observer reachability, tunnel readiness, and absolute audit log paths.
- One command (`tests/prod_test/codex-smoke.sh`) runs minimal codex CLI → driver MCP → slave task → artifact retrieval.
- A test in `internal/executor/dynamicmcp_test.go` registers an MCP tool with `InputSchema={"type":"object","properties":{"x":{"type":"string"}}}`, writes `dynamic_mcp.yaml`, and asserts both (a) bytes round-trip — the decoded `InputSchema` equals the input — *and* (b) on-disk format is a YAML mapping, by reading the raw file and asserting it contains `type: object` rather than `!!binary` / a digit sequence. (Today (a) already holds; (b) currently fails — fix makes both pass.)

**Hard deps:** 09 (codex path clarity).
**Soft deps:** 10 (tool surface stable), 17 (humanloop lock cleanup verified in prod test).

### 21. Document `write_paths` upload flow

**Status:** NEW / TODO
**Labels:** `docs`, `good-first-subissue`
**Estimated PRs:** 1

**Problem:** `TODO.md` flags that `write_paths` usage — chat vs bash skill limitations, token auth for PUT uploads, permissions required for `curl` — is undocumented. This blocks safe write-side adoption.

**Scope:**

- Add `docs/write-paths.md` covering: when to use `write_paths` vs request-body, token authentication for PUT, required slave permissions for upload helpers.
- Cross-link from driver tool descriptions referencing writes.

**Acceptance:**

- New doc exists with at least one end-to-end example.
- Driver tool descriptions for write-capable tools link to the new doc.
- Remove the corresponding entry from `TODO.md`.

**Hard deps:** none.
**Soft deps:** 10 (tool surface stable).

## Track G — Capability crystallization

### 20. After-task capability crystallization wizard (exploratory spike)

**Status:** NEW / TODO
**Labels:** `roadmap`
**Estimated PRs:** **3+** — staged as design spike → local prototype → publish path. Acceptance below is for the design + local prototype only; marketplace publish is a separate follow-up gated by 18.

**Problem:** The "missing tool auto-register" story should not only preserve MCPs that happened to be built during a task. The more valuable workflow is to ask, after a successful task, whether the whole task pattern should become a reusable capability.

**Correct Product Shape:**

- At task completion, review the task goal, steps, commands, files, tools, and decisions.
- Ask the user whether to solidify this class of work into a reusable capability.
- If yes, propose one or more general MCP servers plus corresponding skills.
- The output is not "save this one MCP"; it is "turn the above task into reusable MCP server(s) and skill(s)."

**Scope:**

- Stage 1 (design spike): produce `docs/capability-crystallization.md` covering UX, summarization prompt, candidate taxonomy (MCP / skill / template), and registration path.
- Stage 2 (local prototype): completion-time prompt in driver/commander for successful tasks; generate at least one MCP server spec + one skill markdown from a recorded task transcript; register to local userspace only.
- Stage 3 (publish): marketplace publish behind 18's signing model.

**Acceptance (Stage 1 + 2 only — Stage 3 is a separate issue):**

- Stage 1 doc lands and is reviewed.
- Stage 2: after a completed task, user can decline and no files/configs are changed (test).
- Stage 2: if accepted, the system produces at least one MCP server spec and one skill draft from the completed task transcript (golden test on a recorded transcript).
- Stage 2: generated MCP/skill pair has an acceptance test path before local registration.
- The wizard can split one task into multiple MCP servers and one coordinating skill (asserted on golden transcript).

**Hard deps:** 10 (clearer driver tool surface).
**Soft deps:** 18 (required before Stage 3 — tracked in follow-up).

## Recommended GitHub Issue Creation Order

This order respects hard deps and frontloads work that informs later scope.

1. **01** — Remove `master-agent` build/runtime entrypoints.
2. **02** — Collapse `master_only` routing to `direct_first`.
3. **09** — Codex CLI/Web POCs (early — informs 10).
4. **03** — Merge orchestration packages.
5. **04** — Canonical AgentCard parser.
6. **05** — Data/MCP-server fields.
7. **06** — Unified matcher.
8. **07** — Planner prefilter and deterministic routing.
9. **08a / 08b** — Data setup, then `find_data`.
10. **10** — Driver tool surface.
11. **11** — App-server unsafe gate removal.
12. **12a / 12b** — Status migration, then CI matrix (the parameterised matrix harness lands here so 22 can plug into it).
13. **22a / 22b** — claude / opencode `SessionWorkerBackend` implementations.
14. **13a / 13b / 13c** — Cancel, nudge, concurrency warning.
15. **14** — Approval wiring.
16. **15a / 15b** — `map_reduce` node, then reduce + retry policy.
17. **16a / 16b** — Two-phase commit, then `seq` migration.
18. **17, 18** — In parallel: SQLite/lock GC and marketplace signing.
19. **19** — Prod-test health + codex smoke + YAML marshaler.
20. **21** — `write_paths` docs (good-first-subissue, can start anytime).
21. **20** — Capability crystallization spike → local prototype.

## Template For Copying Into GitHub

```markdown
## Problem

## Scope

## Non-goals

## Acceptance criteria

- [ ]

## Hard deps

## Soft deps

## Estimated PRs

## Labels

## Notes

Parent: #34
```
