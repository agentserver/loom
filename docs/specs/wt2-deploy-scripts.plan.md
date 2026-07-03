# WT-2-deploy-scripts — Plan

> Companion to `wt2-deploy-scripts.spec.md`. TDD order:
> tests / golden files first per file, then minimal impl, then
> refactor. Every spec deliverable maps to exactly one commit; every
> §7 security clause (a)–(h) maps to at least one test-matrix row.

## Files (created in this order — TDD bricks first)

1. `multi-agent/deploy/topology_schema.json` + a bats/jq schema-validate
   fixture — the payload contract is nailed down before either script
   emits it.
2. `multi-agent/deploy/machine_topology.sh` + inline shellcheck +
   `test_machine_topology.bats` — pure shell helper, easiest brick to
   lock down; also serves as the field-set truth for the PowerShell
   mirror.
3. `multi-agent/deploy/windows/observer/config.yaml.template` +
   `expected_placeholders.txt` + `test_template_parity.bats` — locks
   the Windows-observer inline-render contract before deploy.ps1 uses
   it (spec §3.2 clause 2, §6.2 T19).
4. `multi-agent/deploy/linux/deploy_test.bats` — write the full §6.2
   Linux row set (T1–T18d minus T13/T14 which are PowerShell) with
   golden fixtures under `deploy/linux/testdata/`. Every assertion is
   red at this stage.
5. `multi-agent/deploy/linux/deploy.sh` — implement enough to green
   §6.2 rows in ascending order: dry-run first (T1, T2), flag
   validation (T3–T5, T10), stub bring-up + readiness (T6, T6b, T6c,
   T17, T17b, T17c), env whitelist (T15, T15b, T15c, T15d), secret
   redaction (T8, T9), topology (T11, T12), lifecycle (T18, T18b,
   T18c, T18d), install.ps1 boundary (T16).
6. `multi-agent/deploy/windows/deploy.Tests.ps1` — write the
   PowerShell row set (T13 DryRun-on-Linux; T14 script signing;
   PowerShell counterparts of T15 / T15c / T18 / T18b / T18c
   guarded by `if ($IsWindows) { ... }` when needed).
7. `multi-agent/deploy/windows/deploy.ps1` — implement to green the
   Pester rows in the same order.
8. `multi-agent/deploy/linux/README.md` + `multi-agent/deploy/windows/README.md`
   — operator docs (spec §7(f) ExecutionPolicy note lives in the
   Windows README).

## Interfaces (frozen at plan time)

- **`machine_topology.sh` CLI** (source of truth for the emit shape):
  ```
  machine_topology.sh --mode {stub|prod} \
      --observer-port <int> --driver-port <int> --slave-port <int> \
      [--stub-port <int>] \
      [--out PATH]                   # default: emit to stdout as one line
  ```
  Exits 0 on success; exit 5 if hostname redaction fails (as spec §3.3
  code 5). Called by deploy.sh at pipeline step 7.
- **`deploy.sh` PID-file layout** (consumed by `--shutdown`):
  `$LOOM_HOME/.pids/{agentserver-stub,observer,slave,driver}.pid`,
  each a single line with the PID, 0600.
- **Env whitelist function** in deploy.sh (`emit_whitelisted_env`) —
  echoes `KEY=VAL` pairs; consumed by every spawn line via
  `env -i "$(emit_whitelisted_env)" nohup ... &`. The function's set
  matches `subprocess.go:AlwaysAllowedEnvKeys/AlwaysAllowedIfSet/LOOM_*`
  + optional `OPENAI_API_KEY`/`ANTHROPIC_API_KEY` gated on
  `--allow-model-key-passthrough` **and** `$MODE == prod`.

## Test matrix (mirrors spec §6.2; 24 rows — one row per column below)

| # | Test | Verifies | §7 |
|---|---|---|---|
| T1 | `stub_dry_run_golden` | `deploy.sh --stub --dry-run` matches golden JSON at `deploy/linux/testdata/dryrun.stub.golden.json` (with paths / SHA normalized). | §4.3, §7(h) |
| T2 | `prod_dry_run_omits_stub` | `deploy.sh --prod --dry-run` omits `agentserver_stub` from `component_ports` + `planned_commands`. | §4.2 prod |
| T3 | `port_blacklist_rejects_well_known` | `deploy.sh --stub --observer-port 22 --dry-run` exits 2 with stderr `well-known port`. Sub-cases: 80, 443, 3389, 5432, 6379. | §7(e) |
| T4 | `port_range_check` | `--observer-port 100000` and `--observer-port 80` (out of `[1024,65535]`) both exit 2. | §3.1 |
| T5 | `port_collision_check` | `--observer-port 18091 --driver-port 18091` exits 2. | §3.1 |
| T6 | `stub_bin_argv_loopback_pinned` | shim `agentserver-stub` records its argv; deploy.sh's spawn sends `--listen 127.0.0.1:$STUB_PORT`. **Pester mirror T6-ps** in `deploy.Tests.ps1` asserts the same shape for deploy.ps1's stub Start-Process argv (via a Windows-side shim script that writes its argv to a temp file). | §7(a) |
| T6b | `stub_mode_driver_readiness_gate` | shim stub returns 200 on /healthz but stalls on `/agents-tunnel`; deploy.sh reaches driver TCP+HTTP gate within timeout. | §4.2 stub |
| T6c | `stub_mode_slave_config_patched` | after deploy.sh reaches its stub readiness gate, `$LOOM_HOME/slave/config.yaml` shows `daemon.auto_start: false` + `server.url: http://127.0.0.1:$STUB_PORT`. Pester mirror **T6c-ps** asserts the same for deploy.ps1. | §4.2 slave patch |
| T7 | `no_bind_host_override_flag_or_env` | Four `grep` invariants on deploy.sh's source: (1) `grep -F -c '0.0.0.0' deploy.sh == 0` (fixed-string; literal `0.0.0.0` does not appear); (2) `grep -Ec '\-\-listen[[:space:]]+["'"'"']?(\$|\\$\{)' deploy.sh == 0` (no variable interpolation immediately after `--listen`); (3) `grep -Ec '\-\-listen[[:space:]]+["'"'"']?127\.0\.0\.1:' deploy.sh >= 1` (loopback host literal DOES appear at least once); (4) `grep -Fc 'LOOM_STUB_LISTEN' deploy.sh == 0` (spec §7(a): the env override channel is explicitly forbidden). Plus the T6 argv assertion still holds even when the caller sets `LOOM_STUB_LISTEN=0.0.0.0:...` in the env — proves deploy.sh does not read that name. **Pester T7-ps** performs the same four assertions against deploy.ps1 — see Task 10 body for the authoritative `Select-String` syntax; if this row's inline grep snippet ever drifts, the Task-4 body is authoritative. | §7(a) |
| T8 | `dry_run_redacts_secrets` | `LOOM_API_KEY=SECRET_TOKEN_1234 deploy.sh --prod --dry-run`; stdout+stderr contains neither `SECRET_TOKEN_1234` nor its base64 encoding. | §7(h) |
| T9 | `prod_mode_no_new_credential_writes` | synthetic pre-existing `$LOOM_HOME/slave/config.yaml` with `proxy_token: PROXY_TOKEN_SECRET`; run `deploy.sh --prod` preflight-fixture that stops before spawning; `grep -r PROXY_TOKEN_SECRET $LOOM_HOME` returns the original file **only**, no other files. | §7(b) |
| T9-ps | `prod_mode_no_new_credential_writes_windows` (Pester, guarded `if ($IsWindows)`): synthetic pre-existing `$LoomHome/slave/config.yaml` with `proxy_token: PROXY_TOKEN_SECRET_WIN`. Run `deploy.ps1 -Prod`. Assert (i) the file's content post-run equals pre-run byte-for-byte (via `Get-FileHash`) — proves no install.ps1 invocation, which would `Set-Content` the file back to the blank template (`deploy/windows/slave/config.yaml.template:5-10`); (ii) `Select-String -Path (Join-Path $LoomHome '*/*') -Pattern 'PROXY_TOKEN_SECRET_WIN'` returns exactly one hit (the original file). On Linux CI, the test is skipped via `Set-ItResult -Skipped -Because 'requires Windows'` so Pester-on-Linux still runs green. | §7(b), §3.2 -Prod branch |
| T10 | `fail_fast_first_line` | `head -20 deploy.sh` first executable line is `set -euo pipefail`; `head -20 deploy.ps1` before first meaningful line has `$ErrorActionPreference = 'Stop'`. | §7(c) |
| T11 | `hostname_at_redaction` | run machine_topology.sh with `hostname=alice@corp-laptop` (env override); assert `.host` matches `^[0-9a-f]{8}@[0-9a-f]{8}$` and no field contains `alice` / `corp-laptop`. | §7(d) |
| T12 | `topology_schema_valid` | topology JSON produced by real (non-dry-run) `--stub` run validates via `ajv validate -s topology_schema.json -d <blob>` (or a Python `jsonschema` fallback if ajv absent). | §5.1 / §5.2 |
| T13 | `pester_dry_run_on_linux` | `pwsh -c 'Invoke-Pester deploy.Tests.ps1 -Filter *DryRun*'` on Linux: `-Stub -DryRun` emits `component_ports` with 4 keys, exits 0. | §3.2 parity |
| T14 | `ps1_no_setexecutionpolicy_and_unsigned` | `! Select-String -Pattern 'Set-ExecutionPolicy' -Path deploy.ps1`; `(Get-AuthenticodeSignature deploy.ps1).Status -eq 'NotSigned'`. | §7(f) |
| T15 | `whitelist_drops_sensitive` | shim `observer-server` dumps env; parent env `AWS_ACCESS_KEY_ID=NOPE`, `GITHUB_TOKEN=NOPE`, `DOCKER_CONFIG=NOPE`, `NPM_TOKEN=NOPE`; shim's env log contains none. | §7(g) drop |
| T15b | `whitelist_passes_allowed` | parent env includes `PATH=/x`, `HOME=/y`, `LOOM_OBSERVER_URL=...`, `MOCK_MODEL_URL=...`, `AGENTSERVER_ROOT=/z`; shim env log includes exactly these with exact values. | §7(g) pass |
| T15c | `model_key_gate_matrix` | 4 sub-cases: (i) stub — no keys ever pass; (ii) prod, no flag — keys don't pass; (iii) prod, flag — keys pass, WARN on stderr; (iv) stub + flag — exit 2. | §7(g) gate |
| T15d | `model_key_grep_invariant` | `grep -c 'allow-model-key-passthrough' deploy.sh == 3`; `grep -c 'AllowModelKeyPassthrough' deploy.ps1 == 3`. | §7(g) grep guard |
| T16 | `install_ps1_boundary` | `git diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1` produces empty output. | §0 |
| T17 | `stub_end_to_end_liveness` | real bring-up on CI Linux runner: stub /healthz 200, observer TCP LISTEN, driver TCP + HTTP 401 (or any response), slave whoami→stub returns 200; topology JSON on last stdout line schema-valid. | §4.2 stub |
| T17b | `prod_preflight_happy` | fixture pre-registered `$LOOM_HOME/*` (all four preflight checks pass — see Task 7 step 2); network mocked so the slave's tunnel dial to `agent.cs.ac.cn` is intercepted and answered by a local `hosts` override or a `LOOM_SERVER_URL` env-patch fixture. `deploy.sh --prod` exits 0 with exactly **three** readiness gates satisfied: observer TCP LISTEN, slave TCP LISTEN (real daemon path, `auto_start` on), driver TCP LISTEN + HTTP any. Prod mode does NOT wait on an `agentserver-stub` gate — spec §4.2 prod row 1 = "*(skip stub)*". Assertion covers both: (a) 0 stub process ever spawned (grep the PID files: `!test -e $LOOM_HOME/.pids/agentserver-stub.pid`); (b) all three non-stub gates return LISTEN within timeout. | §4.2 prod |
| T17c | `prod_preflight_fails_missing_token` | same fixture but `proxy_token: ""`; assert exit 2 + stderr names the yaml + `E2E_RUNBOOK`. | §4.2 prod preflight |
| T18 | `spawn_then_exit_pids_written` | deploy.sh --stub exits 0; all 4 pid files present and PIDs live. | §4.1 |
| T18b | `shutdown_reaps_all` | after T18, `deploy.sh --shutdown` exits 0, all 4 PIDs reaped in ≤5 s, `.pids/` gone. | §4.1.1 |
| T18c | `readiness_timeout_exit4` | shim stub never responds on /healthz; deploy.sh exits 4; trap cleans up spawned PIDs; no partial `.pids/`. | §3.3 code 4 |
| T18d | `subinstaller_failure_exit3` | shim slave install.sh exits 1; deploy.sh exits 3; earlier stub/observer .pids reaped. | §3.3 code 3 |
| T19 | `template_parity_bats` | `tail -n +2 deploy/windows/observer/config.yaml.template` byte-equals `deploy/linux/observer/config.yaml.template`; the `__PLACEHOLDER__` set in the linux template equals the golden `expected_placeholders.txt` line-for-line. | §3.2 clause 2 |

## Task decomposition (12 tasks — one commit each)

Every task ends with a single `git commit` whose subject is the task
name. The trailer `Co-Authored-By: Claude Opus 4.8 (1M context)
<noreply@anthropic.com>` is required on every commit per the WT-2
prompt.

### Task 1 — topology JSON schema

**Files**:
- Create: `multi-agent/deploy/topology_schema.json`
- Create: `multi-agent/deploy/testdata/topology_valid.json`
- Create: `multi-agent/deploy/testdata/topology_invalid.json`
- Create: `multi-agent/deploy/test_topology_schema.bats`

**Steps**:
1. Write `topology_schema.json` as JSON Schema draft-07 covering
   every field from spec §5.1 (with `additionalProperties: false`;
   `required` lists every non-optional field; `component_ports.agentserver_stub`
   optional per §5.1's "omitted if mode == 'prod'").
2. Write valid + invalid fixtures.
3. Write bats file with two rows: `ajv validate -s ... -d topology_valid.json`
   → exit 0; `-d topology_invalid.json` → exit 1.
4. `bats deploy/test_topology_schema.bats` — green.
5. Commit.

### Task 2 — machine_topology.sh helper

**Files**:
- Create: `multi-agent/deploy/machine_topology.sh`
- Create: `multi-agent/deploy/test_machine_topology.bats`

**Interfaces**:
- Consumes: nothing (`uname`, `hostname`, `nproc`, `/proc/meminfo`
  only).
- Produces: JSON per spec §5.1 to stdout (or file via `--out`);
  exit 5 on redaction failure.

**Steps**:
1. Write `test_machine_topology.bats`:
   - `TestHost_At_Redaction` (T11 fixture: `hostname="alice@corp-laptop"`
     via env override `LOOM_TEST_HOSTNAME` — deploy.sh reads env before
     `$(hostname)`).
   - `TestFieldSet_Complete` (all §5.1 fields present, correct types).
   - `TestModeProd_OmitsStubPort` (with `--mode prod`, output has no
     `agentserver_stub` key under `component_ports`).
   - `TestOutFile_Perm0600` (`--out /tmp/topo.json` → file mode 0600).
2. Red.
3. Implement `machine_topology.sh` with `set -euo pipefail` first
   executable line, `sha256sum` hostname (split on last `@`), gate on
   `command -v jq` (fallback to hand-written JSON only for the 3 primitive
   values — nested `component_ports` object composed via jq if
   available; preflight-fail with actionable msg if jq missing).
4. Green.
5. Commit.

### Task 3 — Windows observer template + parity CI gate

**Files**:
- Create: `multi-agent/deploy/windows/observer/config.yaml.template`
- Create: `multi-agent/deploy/windows/observer/expected_placeholders.txt`
- Create: `multi-agent/deploy/test_template_parity.bats`

**Steps**:
1. Copy `deploy/linux/observer/config.yaml.template` verbatim into the
   Windows dir; prepend a first line
   `# duplicated from linux/observer/config.yaml.template; keep in sync — enforced by test_template_parity.bats`.
2. Extract placeholders from linux template:
   `grep -oE '__[A-Z_]+__' deploy/linux/observer/config.yaml.template | sort -u`
   → currently `__LISTEN_ADDR__`, `__LOOM_HOME__`, `__WS_APIKEY__`.
   Write each on its own line to `expected_placeholders.txt`.
3. Write `test_template_parity.bats` with T19 body: diff of
   `tail -n +2 windows/observer/template` vs `linux/observer/template`
   returns empty; `grep -oE '__[A-Z_]+__' linux/... | sort -u | diff -
   expected_placeholders.txt` returns empty.
4. Green.
5. Commit.

### Task 4 — deploy.sh Linux entrypoint (dry-run + flag validation)

**Files**:
- Create: `multi-agent/deploy/linux/deploy.sh`
- Create: `multi-agent/deploy/linux/deploy_test.bats`
- Create: `multi-agent/deploy/linux/testdata/dryrun.stub.golden.json`
- Create: `multi-agent/deploy/linux/testdata/dryrun.prod.golden.json`

**Interfaces**:
- Consumes: `deploy/machine_topology.sh` (spec §5.4 emit).
- Produces: exit codes 0/2/3/4/5 per spec §3.3.

**Steps**:
1. Write `deploy_test.bats` rows T1, T2, T3, T4, T5, T7, T8, T10, T16.
   All red.
2. Implement enough of `deploy.sh` for T10 first: shebang, `set -euo
   pipefail`, `param(...)`-style flag parse, `--mode` / `--stub` /
   `--prod` unification, mode-mutex check.
3. Add port validators (range + blacklist + pairwise-distinct); T3–T5
   green.
4. Add `--dry-run` code path that prints JSON per spec §4.3 with
   redaction; T1 + T2 + T8 green.
5. Grep-invariant assertions T7 + T16 green (test file reads deploy.sh
   source with `grep -c`).
6. Commit.

### Task 5 — Env whitelist + subprocess spawn plumbing

**Files**:
- Modify: `multi-agent/deploy/linux/deploy.sh` (add
  `emit_whitelisted_env`, `spawn_subprocess`).
- Modify: `multi-agent/deploy/linux/deploy_test.bats` (add T15, T15b,
  T15c, T15d).
- Create: `multi-agent/deploy/linux/testdata/fake-observer-server.sh`
  (shim that dumps its env to `$1`).

**Steps**:
1. Write T15 / T15b / T15c / T15d rows; red.
2. Implement `emit_whitelisted_env`: hard-coded arrays of always-keys,
   if-set keys (mirror `subprocess.go:AlwaysAllowedEnvKeys,
   AlwaysAllowedIfSetEnvKeys`), `LOOM_` prefix + non-empty suffix
   guard.
3. Implement `--allow-model-key-passthrough` parse + `$MODE==stub`
   preflight reject + WARN log on pass.
4. Wire `spawn_subprocess` to `env -i $(emit_whitelisted_env) nohup
   "$@" >"$log" 2>&1 & echo $! > "$pid_file"`.
5. Green all four rows.
6. Commit.

### Task 6 — Stub bring-up path (observer → slave → driver)

**Files**:
- Modify: `multi-agent/deploy/linux/deploy.sh` (stub-mode branch of
  §4.2 stub table).
- Modify: `multi-agent/deploy/linux/deploy_test.bats` (add T6, T6b,
  T6c, T17).
- Create: `multi-agent/deploy/linux/testdata/fake-agentserver-stub.sh`
  (shim: `/healthz` returns 200; `/api/agent/whoami` returns 200;
  `/api/agent/agents-tunnel` stalls).
- Create: `multi-agent/deploy/linux/testdata/fake-agentserver-stub-issue.sh`
  (shim: `issue` subcommand prints deterministic 5-tuple JSON).

**Interfaces**:
- Consumes: pre-existing `deploy/linux/{observer,slave,driver}/install.sh`
  (invoked with real flag surfaces; no source changes).
- Consumes: `yq -i` for YAML patching (preflight requires
  `command -v yq`; fail with actionable msg otherwise).

**Steps**:
1. Write T6 / T6b / T6c / T17 rows; red.
2. Preflight: `command -v yq` check.
3. Spawn stub → readiness (curl /healthz) → spawn observer install.sh
   + observer-server → readiness (TCP LISTEN via `bash -c '</dev/tcp/127.0.0.1/PORT'`
   or `nc -z`).
4. Slave: install.sh render → `yq -i` patch server.url + credentials +
   daemon.auto_start=false + daemon.listen → spawn slave-agent →
   readiness (curl whoami with slave.proxy_token).
5. Driver: install.sh render → yq patch server.url + credentials →
   spawn driver-agent serve-daemon → readiness (TCP LISTEN + any HTTP).
6. Emit topology (via machine_topology.sh from Task 2).
7. Green T6 + T6b + T6c + T17.
8. Commit.

### Task 7 — Prod bring-up path + preflight guards

**Files**:
- Modify: `multi-agent/deploy/linux/deploy.sh` (prod-mode branch).
- Modify: `multi-agent/deploy/linux/deploy_test.bats` (add T9, T17b,
  T17c).
- Create: `multi-agent/deploy/linux/testdata/fixture-prod-preflight/`
  (pre-populated `$LOOM_HOME` tree with valid + invalid config
  fixtures).

**Steps**:
1. Write T9 + T17b + T17c rows; red.
2. Prod preflight — enforce spec §4.2 prod-table checklist verbatim.
   For each of the four checks, on failure emit `deploy.sh: prod
   preflight failed: <specific reason>; see tests/prod_test/E2E_RUNBOOK.md:83-108`
   and exit 2:
   - `$LOOM_HOME/observer/observer.yaml` exists AND is readable AND
     its `listen_addr` field (via `yq eval '.listen_addr' file`)
     equals `127.0.0.1:$OBSERVER_PORT` when `--observer-port` was
     supplied (else copy the yaml's value into `$OBSERVER_PORT` and
     proceed).
   - `$LOOM_HOME/slave/config.yaml` exists AND is readable AND
     `.credentials.proxy_token != ""` AND `.credentials.short_id != ""`
     AND `.credentials.workspace_id != ""` (yq eval on each). Also
     read back `.daemon.listen` and validate against `--slave-port`
     per spec §4.2's "operator-registered slave uses port X" abort
     rule.
   - `$LOOM_HOME/driver/config.yaml` exists AND is readable AND
     `.credentials.proxy_token != ""` AND `.credentials.short_id != ""`
     (matches the `cmd/driver-agent/main.go:318,325` guards — without
     this, deploy.sh would successfully spawn the driver only for it
     to `die("serve-daemon requires credentials.short_id")` mid-flight
     and the timeout would surface as exit 4 instead of the actionable
     exit 2).
   - `$LOOM_HOME/observer/observer-server`,
     `$LOOM_HOME/slave/slave-agent`, `$LOOM_HOME/driver/driver-agent`
     each exist AND are executable (`[[ -x $path ]]`).
   Add T17c-b sub-cases in `deploy_test.bats` covering each of the
   four failure modes above (missing file, missing proxy_token,
   missing short_id, non-executable binary) — each asserts exit 2 +
   the error message names the specific missing field/file.
3. Prod spawn: `nohup` observer/slave/driver directly (no install.sh
   invocation, no config edits). Read `daemon.listen` from slave
   config for readiness port; abort preflight if `--slave-port`
   mismatches.
4. Green T9 + T17b + T17c.
5. Commit.

### Task 8 — Lifecycle: PID file, trap, --shutdown

**Files**:
- Modify: `multi-agent/deploy/linux/deploy.sh` (`.pids/` writes,
  SIGTERM/SIGKILL trap, `--shutdown` mode).
- Modify: `multi-agent/deploy/linux/deploy_test.bats` (add T18, T18b,
  T18c, T18d).
- Create: `multi-agent/deploy/linux/testdata/fake-observer-that-fails.sh`
  (shim for T18d — sub-installer failure).

**Steps**:
1. Write T18–T18d rows; red.
2. Wire `SPAWNED_PIDS` array. **Trap is failure-only, not EXIT-blanket**
   — spec §4.1 step 8 says successful deploy.sh exits 0 while the four
   spawned processes keep running (matching `tests/prod_test/run_e2e.sh`'s
   `nohup ... & disown` pattern). Wire:
   ```bash
   _cleanup_spawned_on_failure() {
     local ec=$?
     [[ $ec -eq 0 ]] && return 0     # success path: leave children alive
     for pid in "${SPAWNED_PIDS[@]}"; do
       kill -TERM "$pid" 2>/dev/null || true
     done
     sleep 3
     for pid in "${SPAWNED_PIDS[@]}"; do
       kill -KILL "$pid" 2>/dev/null || true
     done
     rm -rf "$LOOM_HOME/.pids/"
     exit "$ec"
   }
   trap _cleanup_spawned_on_failure EXIT
   ```
   The `[[ $ec -eq 0 ]] && return 0` guard is the load-bearing line
   that keeps successful runs from killing their own children. T18
   asserts this by observing PIDs live after exit 0; T18c/T18d assert
   the failure-path branch.
3. Wire `--shutdown` mode: reads `$LOOM_HOME/.pids/*.pid`, `kill -TERM`,
   `sleep 5`, `kill -KILL` survivors, `rm -rf .pids/`. Runs in a
   completely separate `case "$MODE" in shutdown) ... ;;` branch that
   bypasses the trap install above.
4. Wire readiness-gate timeout → exit 4 branch (trap runs, kills the
   partial fleet).
5. Wire sub-installer failure detection (check return status of
   install.sh call in Task 6) → exit 3 branch (trap runs).
6. Green.
7. Commit.

### Task 9 — Topology emit + machine_topology.sh integration

**Files**:
- Modify: `multi-agent/deploy/linux/deploy.sh` (final pipeline step:
  call `machine_topology.sh` and print/write per `--topology-out`).
- Modify: `multi-agent/deploy/linux/deploy_test.bats` (add T11, T12).

**Steps**:
1. Write T11 / T12 rows; red.
2. Wire the pipeline's final step to `bash "$SCRIPT_DIR/../machine_topology.sh"
   --mode "$MODE" --observer-port ...` and either print to stdout or
   `--out` per `--topology-out`.
3. Green.
4. Commit.

### Task 10 — deploy.ps1 PowerShell entrypoint

**Files**:
- Create: `multi-agent/deploy/windows/deploy.ps1`
- Create: `multi-agent/deploy/windows/deploy.Tests.ps1`
- Create: `multi-agent/deploy/windows/testdata/dryrun.stub.golden.json`

**Interfaces**:
- Consumes: `deploy/windows/observer/config.yaml.template` (Task 3),
  pre-existing `deploy/windows/{slave,driver}/install.ps1` (real
  param sets — no fabricated flags).
- Consumes: Pester 5.x (`pwsh -c 'Install-Module Pester -Force'` in
  CI setup).

**Steps**:
1. Write `deploy.Tests.ps1` with the following row set. **Cross-host
   partitioning**: rows that only inspect script *source* (grep, hash,
   signature status, argv-of-shim spawned via pwsh-cross-platform
   Start-Process) run on both Linux and Windows — no skip guards. Only
   rows that require Windows-native cmdlets unavailable on Linux
   (e.g., `Test-NetConnection`, `Get-CimInstance Win32_ComputerSystem`,
   Windows-only file-hash edge cases) get the
   `Set-ItResult -Skipped -Because 'requires Windows'` guard when
   `-not $IsWindows`. Every §7 clause has ≥1 Pester assertion that
   runs on Linux CI too, so a Windows-blind implementation cannot
   green the suite.

   **Any-host rows** (run on both Linux Pester-run and Windows Pester-run):
   - **T13** (DryRun-on-Linux mirror): `pwsh -c 'Invoke-Pester ...
     -Filter *DryRun*'`; -Stub -DryRun emits component_ports with 4
     keys, exits 0. (Source name kept for cross-ref; runs both.)
   - **T14**: `! Select-String -Pattern 'Set-ExecutionPolicy' -Path
     deploy.ps1`; `(Get-AuthenticodeSignature deploy.ps1).Status -eq
     'NotSigned'`.
   - **T10-ps**: `head` of deploy.ps1 shows `$ErrorActionPreference =
     'Stop'` before the first meaningful command.
   - **T7-ps**: three assertions on deploy.ps1's source:
     ```powershell
     # (1) Literal '0.0.0.0' does not appear anywhere.
     ((Select-String -Path deploy.ps1 -SimpleMatch -Pattern '0.0.0.0') `
       | Measure-Object).Count | Should -Be 0

     # (2) No variable interpolation follows a `--listen` flag anywhere
     # (regex, not -SimpleMatch — regex needed to alternate $ and ${).
     ((Select-String -Path deploy.ps1 -Pattern '--listen\s+["'']?\$') `
       | Measure-Object).Count | Should -Be 0

     # (3) Positive: the loopback host string `127.0.0.1:` DOES
     # appear next to a `--listen` argv token. PowerShell splits
     # -ArgumentList into a comma-separated list per spec §3.2 line
     # 205-207 (`-ArgumentList '--listen','127.0.0.1:'+$StubPort`),
     # so we assert that both tokens appear in adjacent source
     # positions rather than as one contiguous substring. Using a
     # multi-line regex that tolerates newline+whitespace between
     # the two array elements:
     ((Select-String -Path deploy.ps1 -Pattern `
        "'--listen'\s*,\s*'127\.0\.0\.1:'") `
       | Measure-Object).Count | Should -BeGreaterThan 0
     # This matches the canonical spec form. A hypothetical variant
     # style (`-ArgumentList "--listen 127.0.0.1:$StubPort"` — one
     # combined string) would need a second alternative added here
     # if the implementer chose that form; for the audit invariant to
     # remain effective, deploy.ps1 SHOULD stick to the comma-array
     # form the spec shows.

     # (4) `LOOM_STUB_LISTEN` env override MUST NOT appear anywhere
     # in deploy.ps1's source — spec §7(a) explicitly forbids this
     # override channel. Substring search catches every reference
     # (variable read, comment, quoted string).
     ((Select-String -Path deploy.ps1 -SimpleMatch `
                     -Pattern 'LOOM_STUB_LISTEN') `
       | Measure-Object).Count | Should -Be 0
     # And its bash sibling on deploy.sh (mirror of T7 assertion 4):
     # `grep -Fc LOOM_STUB_LISTEN deploy.sh == 0`. Add that line to
     # the T7 bash matrix row body when implementing Task 4.
     ```
     With `-SimpleMatch`, PowerShell treats the pattern as a literal
     string (dots are literal, no regex escaping). Without
     `-SimpleMatch`, dots and `$` need standard regex escaping. The
     three-line block is the authoritative T7-ps body; the top matrix
     row's prose summarizes it. **Windows-blind check for §7(a)
     parity.**
   - **T3-ps / T4-ps / T5-ps**: `pwsh` invocation of `deploy.ps1
     -Stub -ObserverPort 22 -DryRun` (blacklist), `-ObserverPort
     100000 -DryRun` (range), `-ObserverPort 18091 -DriverPort 18091
     -DryRun` (collision) — each expects Pester to catch a non-zero
     exit / thrown error. **Windows-blind coverage of §7(e).**
   - **T8-ps**: `$env:LOOM_API_KEY = 'SECRET_TOKEN_WIN_1234'; pwsh
     deploy.ps1 -Prod -DryRun`; capture stdout+stderr; assert neither
     `SECRET_TOKEN_WIN_1234` nor its base64 encoding appears.
     **Windows-blind coverage of §7(h).**
   - **T15d-ps**: `(Select-String -Path deploy.ps1 -Pattern
     'AllowModelKeyPassthrough' \| Measure-Object).Count -eq 3`
     (grep-invariant for the model-key gate).
   - **T15-ps** (any host — uses a `pwsh`-authored shim binary that
     dumps its env to a file; works on Linux too): set `$env:AWS_ACCESS_KEY_ID
     = 'NOPE'`, `$env:GITHUB_TOKEN = 'NOPE'`, `$env:DOCKER_CONFIG =
     'NOPE'`, `$env:NPM_TOKEN = 'NOPE'`; run `deploy.ps1 -Stub
     -BinDir <shim-dir>` (short-circuit before Windows-specific TCP
     probes via a `-BypassReadiness` test-only flag that only exists
     when `$env:LOOM_TEST_MODE = '1'`); read the shim's env log;
     assert none of the four keys appear. **Windows-blind coverage of §7(g) drop.**
   - **T15b-ps** (any host): set the whitelist keys + `MOCK_MODEL_URL`
     etc.; assert the shim env log includes exactly them. **§7(g)
     pass parity.**
   - **T15c-ps** (any host): four sub-cases of the model-key gate —
     stub-drops, prod-drops-without-flag, prod-passes-with-flag,
     stub+flag-preflight-fail. Uses the same shim + `-BypassReadiness`
     test hook.
   - **T11-ps** (any host): topology emit from PowerShell — set
     `$env:LOOM_TEST_HOSTNAME = 'alice@corp-laptop'` (a test seam the
     PowerShell topology helper honours identically to the Bash one);
     invoke deploy.ps1's `-DryRun` path (which for the topology-emit
     helper is a stand-alone `Get-MachineTopology` function we can
     `Import-Module deploy.ps1; Get-MachineTopology`); assert the
     resulting JSON's `.host` matches `^[0-9a-f]{8}@[0-9a-f]{8}$` and
     no field contains `alice` or `corp-laptop`. **Windows-blind
     coverage of §7(d) for the PowerShell side.**
   - **T9b-ps** (any host): **static source-level §7(b) guard for
     Windows prod.** Pester test parses deploy.ps1 AST via
     `[System.Management.Automation.Language.Parser]::ParseFile(...)`
     and asserts (i) every AST node that invokes
     `deploy/windows/slave/install.ps1` OR
     `deploy/windows/driver/install.ps1` (via `&` call-operator or
     `Invoke-Expression`) lives inside a code path guarded by a
     `-Stub` / `$Mode -eq 'stub'` conditional (walk parent nodes for
     an `if` whose predicate contains `$Stub` or a comparison to
     `'stub'`); (ii) grep search for the literal strings
     `install.ps1` and `Set-Content -LiteralPath.*config.yaml`
     confirms zero occurrences inside the prod code path. Any
     regression that puts an install.ps1 call in the prod branch
     fails this test on Linux CI without needing a Windows host. This
     is the any-host counterpart of T9-ps and closes the last §7
     Linux-CI gap. | §7(b), §3.2 -Prod branch |
   - **T19-ps** (any host): re-runs the template-parity assertion
     from Pester so the Windows CI leg catches drift too.

   **Windows-only rows** (guarded with `Set-ItResult -Skipped
   -Because 'requires Windows'` when `-not $IsWindows`):
   - **T6-ps**: shim `agentserver-stub.windows-amd64.exe` records
     argv; deploy.ps1's Start-Process passes `--listen 127.0.0.1:$StubPort`.
     (Any-host T7-ps already covers the grep-invariant flavour of
     §7(a); T6-ps is the Windows-runtime observation.)
   - **T6c-ps**: after -Stub readiness, slave config.yaml shows
     `daemon.auto_start: false` and `server.url: http://127.0.0.1:$StubPort`.
   - **T9-ps**: synthetic pre-existing config with `proxy_token:
     PROXY_TOKEN_SECRET_WIN`; run `deploy.ps1 -Prod`; assert file
     hash unchanged AND `Select-String -Path ... -Pattern
     'PROXY_TOKEN_SECRET_WIN'` returns exactly one hit.
   - **T17-ps** (Windows-only fresh-host equivalent of T17): full
     `-Stub` bring-up on Windows-native TCP + Invoke-WebRequest gates.
   - **T18-ps / T18b-ps / T18c-ps / T18d-ps**: PID files under
     `$LoomHome\.pids\`, `-Shutdown` reaps via `Stop-Process`,
     readiness timeout exits 4, sub-installer failure exits 3.

   **Result**: every §7 clause (a)–(h) has ≥1 any-host Pester
   assertion. A Windows-only bug (e.g., deploy.ps1 accepts
   `-ObserverPort 22`) is caught by Linux CI running Pester;
   Windows-only-runtime bugs (e.g., stub Start-Process argv drift)
   are caught by the Windows CI leg. Task 12 step 2 runs Pester on
   Linux; Task 12 step 4 additionally runs Pester on Windows.
2. Implement `deploy.ps1` `param(...)` block with the two parameter
   sets (Switch + Mode), `Set-StrictMode -Version Latest`,
   `$ErrorActionPreference = 'Stop'` as the second line, T14 green.
3. Implement `-DryRun` code path (mirror of §4.3 JSON shape); T13 green.
4. Implement `-Stub` bring-up (agentserver-stub Start-Process,
   inline-render observer.yaml, invoke slave install.ps1 with real
   params, invoke driver install.ps1 with real params, post-process
   both configs, spawn slave-agent + driver-agent). Env cleared+whitelisted
   via `ProcessStartInfo.EnvironmentVariables.Clear()`.
5. Implement `-Prod` mode (spawn-only from pre-registered $LoomHome).
6. Implement `-Shutdown` mode.
7. Green all Pester rows.
8. Commit.

### Task 11 — READMEs (Linux + Windows)

**Files**:
- Create: `multi-agent/deploy/linux/README.md`
- Create: `multi-agent/deploy/windows/README.md`

**Steps**:
1. Linux README: quick-start (`bash deploy.sh --stub`), full flag
   list, exit-code table, security notes summary (a)-(h) with links
   into the spec, `--dry-run` example (copy from spec §4.3 shortened),
   `--shutdown` example.
2. Windows README: same shape, plus the `-NoProfile -ExecutionPolicy
   Bypass` invocation (spec §7(f)) with a call-out that
   `Set-ExecutionPolicy` is NOT to be run at any scope, plus the
   Windows-native readiness cmdlet notes from §4.4.
3. Both README first lines cross-link the spec + plan files.
4. Commit.

### Task 12 — Final integration test + fresh-host acceptance transcript

**Files**:
- Modify: `multi-agent/deploy/linux/deploy_test.bats` (final
  wired-up run; assert no regressions in any prior test).
- Modify: `multi-agent/deploy/windows/deploy.Tests.ps1` (same).

**Steps**:
1. Run the full `bats deploy/linux/deploy_test.bats` +
   `bats deploy/test_template_parity.bats` +
   `bats deploy/test_topology_schema.bats` +
   `bats deploy/test_machine_topology.bats` — all green.
2. Run `pwsh -c 'Invoke-Pester deploy/windows/deploy.Tests.ps1
   -Output Detailed'` on Linux — all green.
3. On a fresh Ubuntu 22.04 VM (or CI runner), run the §6.3 Linux
   leg steps 1-3; capture the exit code + last-line topology JSON.
4. On a fresh Windows 11 host, first run
   `pwsh -c 'Invoke-Pester deploy/windows/deploy.Tests.ps1 -Output
   Detailed'` (this is where the Windows-only Pester rows
   T6-ps/T6c-ps/T9-ps/T17-ps/T18-ps series actually execute); assert
   all pass. Then run the §6.3 Windows leg steps 4–6. Paste both
   Pester and fresh-host transcripts into the PR body.
5. Run `git diff origin/paper/v3-integration --
   multi-agent/deploy/windows/slave/install.ps1` — assert empty
   output. (T16 already asserts this in the automated suite;
   Task 12 asserts one more time before the PR.)
6. Commit.

## Test → §7 security clause coverage matrix

| Spec §7 clause | Test rows |
|---|---|
| (a) stub loopback-only | T6, T7 |
| (b) OAuth never touches disk | T9 |
| (c) fail-fast preflight | T10 |
| (d) hostname / user redaction | T11 |
| (e) port validation + blacklist | T3, T4, T5 |
| (f) PS ExecutionPolicy | T14 |
| (g) subprocess env whitelist | T15, T15b, T15c, T15d |
| (h) --dry-run secret redaction | T8 |

Every clause has ≥1 test, matching spec §6.2's own coverage claim.
`§0` install.ps1 boundary is T16. `§4.2` stub tunnel-plane workaround
is T6b + T6c. `§4.1.1` lifecycle+teardown is T18–T18d. `§3.3` exit
codes are T3/T4/T5 (2), T18d (3), T18c (4), machine_topology.sh
redact-fail (5). `§5` topology is T11+T12. `§6.3` fresh-host is
Task 12.

## Commit order + dependency graph

```
Task 1 (schema)          ─┐
Task 2 (topology.sh)     ─┤
Task 3 (win/obs template) ┼── independent, can commit in any order
                          │
Task 4 (deploy.sh dry-run) ── needs Task 2 (topology.sh) for topology in --dry-run
Task 5 (env whitelist)     ── needs Task 4
Task 6 (stub bring-up)     ── needs Tasks 4, 5
Task 7 (prod bring-up)     ── needs Tasks 4, 5
Task 8 (lifecycle)         ── needs Tasks 6, 7
Task 9 (topology emit)     ── needs Tasks 6/7 + Task 2
Task 10 (deploy.ps1)       ── needs Tasks 1, 3 (templates); parallel with 4-9 but easier after
Task 11 (READMEs)          ── needs Tasks 4–10 (documented behaviour must exist)
Task 12 (integration+PR)   ── last; runs the full test tree + fresh-host transcripts
```

Total: 12 commits. Each commit is independently reviewable and passes
its own subset of the test matrix.

## Baseline binary requirement (recorded here, not enforced by scripts)

The `--bin-dir` default (`multi-agent/deploy/linux/bin`) is empty on
a fresh checkout (`multi-agent/deploy/linux/bin/.gitignore:*`); Task
12's fresh-host recipe rebuilds every binary with the arch-suffixed
name (spec §6.3 Linux step 1). CI runs must build those binaries in a
setup step before invoking bats — CI YAML lives in
`.github/workflows/multi-agent.yml` (**not owned by this worktree**;
CI wiring is deferred to the WT-3 integration pass). Meanwhile, the
bats tests that need real binaries (T17 e2e) skip via `skip
"requires deploy/linux/bin/*.linux-* — see spec §6.3"` when the
binaries are absent, so `bats deploy_test.bats` still runs green on a
fresh checkout for every non-e2e assertion.
