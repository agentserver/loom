# WT-2-deploy-scripts — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 2 table row
> **WT-2-deploy-scripts** (line 105); 12 号 §D6c.
> Branch: `paper/v3/p2-deploy-scripts`.
> Base: `origin/paper/v3-integration` HEAD `d053897`.
> Downstream consumers: 12 号 §D6c one-key bring-up for E1/E6
> (`TimeToFirstTask` / `SetupFailureRate` / `ManualSetupStepCount` /
> `ConfigTouchCount` metrics); D1 run schema `machine_topology` column
> (`internal/evalrun/schema.go:30`, populated indirectly via the eval-runner).
>
> **Stage**: this is the **Stage 1 design spec** of the three-stage workflow
> (Spec → Plan → Code). Neither `deploy/linux/deploy.sh` nor
> `deploy/windows/deploy.ps1` exists on disk yet — Stage 3 creates both.
> A reviewer at Stage 1 is judging **design intent**: does the proposed CLI
> shape, execution ordering, topology payload, and security envelope form a
> coherent, complete contract? Whether the scripts are implemented is out of
> scope until Stage 3's `git diff` review.

---

## 0. Boundary against WT-0-windows-slave

Phase 0 **WT-0-windows-slave** owns
`multi-agent/deploy/windows/slave/install.ps1` (see todo_list line 49 —
"deploy/windows/install-slave.ps1 让 Windows slave 能跑起来（不含一键全栈，全栈拉起归 WT-2-deploy-scripts）").
This worktree — WT-2-deploy-scripts — owns the **one-key full-stack
orchestrator** and must **not** modify `install.ps1` in any way. Enforcement:
Stage 3 gates on `git diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1`
returning empty output.

The orchestrator **calls** `install.ps1` (and its Linux siblings) as
subprocesses; it never edits them.

---

## 1. Task boundary & file scope

Files this worktree owns (all NEW):

| Path | Action |
|---|---|
| `multi-agent/deploy/linux/deploy.sh` | **NEW**. Bash entrypoint; implements the `--mode {stub,prod}` matrix, port arg parsing, topology-emit, `--dry-run`. |
| `multi-agent/deploy/linux/README.md` | **NEW**. Operator-facing usage: examples for both modes, the port map, security notes ((a)-(h)), the `--dry-run` example, the exit-code table. |
| `multi-agent/deploy/windows/deploy.ps1` | **NEW**. PowerShell 7+ entrypoint; parameter set, port validation, topology-emit, `-DryRun` — behavioural parity with `deploy.sh`. |
| `multi-agent/deploy/windows/README.md` | **NEW**. Windows-flavoured operator doc; ExecutionPolicy note (see §7(f)), `-DryRun` example, the same port map / security notes. |
| `multi-agent/deploy/machine_topology.sh` | **NEW**. Bash helper — collect + redact + emit topology JSON to stdout. Both `deploy.sh` and (indirectly, via a tiny inline PowerShell mirror in `deploy.ps1`) consume this contract. |
| `multi-agent/deploy/linux/deploy_test.bats` | **NEW**. bats-core test file for Bash entrypoint (per §6). Runs under `bats` — see §6.1 for the runner command that is expected to be present on the CI host. If bats-core is unavailable, `deploy_test.bats.md` documents each assertion in shell prose so a human reviewer can run them manually; the bats file is authoritative. |
| `multi-agent/deploy/windows/deploy.Tests.ps1` | **NEW**. Pester 5.x tests for PowerShell entrypoint (§6). Runs on Linux under `pwsh -c 'Invoke-Pester deploy.Tests.ps1'` — Pester 5 does not require Windows. |
| `multi-agent/deploy/topology_schema.json` | **NEW**. JSON schema (draft-07) for the topology payload; §5 references it as the source of truth so evaluators and future eval-runner writers agree on the field set. |
| `multi-agent/deploy/windows/observer/config.yaml.template` | **NEW**. Byte-for-byte copy of `deploy/linux/observer/config.yaml.template` (observer config is OS-agnostic YAML) plus a leading `# duplicated from linux/observer/config.yaml.template; keep in sync — enforced by test_template_parity.bats` header comment. See §3.2 for why (Windows has no observer install.ps1, so deploy.ps1 renders inline from this template). CI test asserts equality modulo the header line so drift breaks the build. |
| `multi-agent/deploy/windows/observer/expected_placeholders.txt` | **NEW**. Golden list — one `__PLACEHOLDER__` token per line — of the placeholders the Windows inline-render substitutes (currently `__LISTEN_ADDR__`, `__LOOM_HOME__`, `__WS_APIKEY__`). T19 (§6.2) compares this against `grep -oE '__[A-Z_]+__' deploy/linux/observer/config.yaml.template \| sort -u`. Adding a new placeholder to the Linux template forces an update here + a new substitution branch in deploy.ps1. |
| `multi-agent/deploy/test_template_parity.bats` | **NEW**. bats test file for T19; lives under `deploy/` (not `deploy/linux/`) because it inspects both OS templates. |

Files this worktree **must not** modify:

- `multi-agent/deploy/windows/slave/install.ps1` — WT-0-windows-slave. §0.
- `multi-agent/deploy/windows/driver/install.ps1` — pre-existing (see git
  ls-tree above); we call it as a subprocess, not edit it.
- `multi-agent/deploy/linux/{driver,observer,slave}/install.sh`,
  `.../bootstrap.sh` — pre-existing; call, don't edit.
- `multi-agent/tools/eval/agentserver-stub/` — the stub binary + CLI; we
  invoke `agentserver-stub --listen 127.0.0.1:PORT` but never patch its
  source. (`main.go:34` already defaults to `127.0.0.1:18080`; §7(a) hardens
  this at the deploy-script layer regardless.)
- `multi-agent/internal/evalrun/` — D1 schema; we produce a JSON blob that
  the eval-runner writes into `Schema.MachineTopology` verbatim.
  `schema.go:30` and `writer.go:66` remain the sole writers.
- `multi-agent/tests/prod_test/` — prod runbook and configs; we align to it
  but do not touch. §3 cross-references `E2E_RUNBOOK.md:40-44` for the
  canonical prod port map.

`multi-agent/go.mod` is not modified. No new Go code; scripts are Bash + PowerShell only.

---

## 2. Background — what the scripts are for

### 2.1 Why two modes

12 号 §D6c is explicit: "默认走 §C4 stub auto-auth（绕过 OAuth），可选 `--prod`
切换到真 agentserver（按 prod runbook）". The scripts thus have exactly two
modes:

- **`--mode stub`** (default) — bring up `agentserver-stub` at
  `127.0.0.1:18080`, then observer / driver / slave against it. No OAuth,
  no network egress off-host. This is the mode the E1/E6 eval loop uses
  (§D3 → §D6c).
- **`--mode prod`** — skip the stub entirely; observer / driver / slave
  point at `agent.cs.ac.cn` and expect the operator to have already
  completed the driver-agent + slave-agent device-code OAuth flows (per
  `tests/prod_test/E2E_RUNBOOK.md:230-244`). No credentials touch disk from
  this script — that's the pre-existing bootstrap's job.

### 2.2 What "one-key full-stack" means here

Per todo_list line 105, the scope is *four* processes on one host:
`agentserver-stub` (mode=stub only) → `observer-server` → `driver-agent
serve-daemon` → `slave-agent`. Each is spawned in an order that mirrors
`tests/prod_test/E2E_RUNBOOK.md` §Step 2: observer first, slave second,
driver last, with per-component readiness gates (§4.4).

**Not in scope**: multi-slave fan-out (E2E_RUNBOOK runs 2 slaves — see 12 号
§C1 for the future 4-slave Windows/cloud sandbox extension), remote hosts
(WT-2-deploy-scripts is per-host; multi-host is Phase 3 WT-3-prod-multidevice),
container / systemd install (both existing installers handle that; we invoke
the foreground path).

### 2.3 Downstream contract — `machine_topology`

D1 stores machine topology as a `TEXT NOT NULL` column
(`internal/evalrun/writer.go:66`), one row per run, so it must serialise
losslessly to a string. We define the shape (JSON) and redaction rules
(§5.1, §5.3, §7(d)); the eval-runner reads the JSON blob our scripts emit
and writes it verbatim into `Schema.MachineTopology`. No schema-level
coupling — the eval-runner integration seam is stdout: `deploy.sh --mode
stub` prints a single-line JSON topology blob on its last line, and the
eval-runner captures that. See §5.4.

---

## 3. CLI surface

### 3.1 Linux (`deploy.sh`)

```
bash deploy/linux/deploy.sh [--stub | --prod | --mode {stub|prod}] \
    [--observer-port 18091] \
    [--driver-port 18092] \
    [--slave-port 18093] \
    [--stub-port 18080] \
    [--loom-home DIR] \
    [--bin-dir DIR] \
    [--dry-run] \
    [--allow-model-key-passthrough] \
    [--topology-out PATH]

bash deploy/linux/deploy.sh --shutdown [--loom-home DIR]
```

- **Mode selection** — three equivalent ways: bare `--stub` / bare
  `--prod` (todo_list line 105 uses these), or `--mode stub` / `--mode
  prod` (long form for scripts). **Default is `--stub`** when no mode
  flag is present, matching 12号 §D6c "默认走 §C4 stub auto-auth". Passing
  more than one mode flag, or `--mode` with any value other than `stub` /
  `prod`, is a preflight error (exit 2). `--stub` and `--mode stub`
  parse to the same internal `$MODE=stub` variable — subsequent spec
  sections refer only to `$MODE` for clarity.
- `--observer-port`, `--driver-port`, `--slave-port`, `--stub-port` —
  optional. Defaults `18091`, `18092`, `18093`, `18080`. All four must:
  - be an integer in `[1024, 65535]` (parses via `[[ "$x" =~ ^[0-9]+$ ]]`
    + comparison);
  - not appear on the well-known blacklist §7(e);
  - be pairwise distinct.
- `--loom-home` — install / runtime dir for the sub-installers (default
  `$HOME/.loom/eval-deploy`, created 0700). Passed through as `--loom-home`
  to `deploy/linux/{observer,slave}/install.sh`.
- `--bin-dir` — pre-built binary cache dir (default
  `<repo>/multi-agent/deploy/linux/bin`). Passed as `--bin PATH` when
  present.
- `--dry-run` — print the resolved plan (see §4.3) to stdout, exit 0
  without touching disk or spawning any process. `--dry-run` output MUST
  NOT contain any OAuth token, API key, or proxy token — see §7(h).
- `--allow-model-key-passthrough` — opt in to inheriting
  `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` from the parent env into the
  driver+slave subprocesses. See §7(g). Only meaningful in `--mode prod`
  (preflight-rejects in `--mode stub`); default off.
- `--topology-out PATH` — write topology JSON to `PATH` instead of stdout
  last-line (still 0600-created). Default: emit to stdout as the final
  line for the eval-runner to capture.

### 3.2 Windows (`deploy.ps1`)

```
pwsh -File deploy/windows/deploy.ps1 [-Stub | -Prod | -Mode {stub|prod}] `
    [-ObserverPort 18091] `
    [-DriverPort 18092] `
    [-SlavePort 18093] `
    [-StubPort 18080] `
    [-LoomHome DIR] `
    [-BinDir DIR] `
    [-DryRun] `
    [-AllowModelKeyPassthrough] `
    [-TopologyOut PATH]

pwsh -File deploy/windows/deploy.ps1 -Shutdown [-LoomHome DIR]
```

Same semantics as §3.1. `-Stub` and `-Prod` are `[switch]` parameters in
one parameter set; `-Mode` is `[ValidateSet('stub','prod')]` in a second
set. **Default `$Mode = 'stub'`** when neither `-Stub`, `-Prod`, nor
`-Mode` is passed. Passing two mode selectors → PowerShell parameter set
error (surfaced as our exit 2 by the outer `try { }`). Ports use
`[ValidateRange(1024,65535)]` **and** an explicit blacklist check inside
the script body (validation attributes fire before the blacklist).

**Windows `-Stub` full-stack sub-installer wiring.** deploy.ps1 in
`-Stub` mode brings up all four processes on Windows. Because
`multi-agent/deploy/windows/` ships a **slave** installer
(`deploy/windows/slave/install.ps1`, WT-0-owned, not modified here) and
a **driver** installer (`deploy/windows/driver/install.ps1`,
pre-existing), but **no Windows observer installer**, deploy.ps1
handles the three sub-components differently in `-Stub`:

1. **agentserver-stub** — `Start-Process -FilePath (Join-Path $BinDir
   'agentserver-stub.windows-amd64.exe') -ArgumentList
   '--listen','127.0.0.1:'+$StubPort,'--workspace-id','auto'` with
   `[System.Diagnostics.ProcessStartInfo]::EnvironmentVariables.Clear()`
   + explicit env whitelist adds (§7(g)). Same PID recorded in
   `.pids/agentserver-stub.pid`.

2. **observer-server** — deploy.ps1 renders `observer.yaml` **inline**
   (no Windows observer installer exists to defer to). The rendered
   file is a copy of `deploy/linux/observer/config.yaml.template` (the
   observer config schema is OS-agnostic; see the actual template body
   in that file — it contains exactly three placeholders:
   `__LISTEN_ADDR__`, `__LOOM_HOME__`, `__WS_APIKEY__`) with those
   three substituted:
   - `__LISTEN_ADDR__` → `127.0.0.1:$ObserverPort`
   - `__LOOM_HOME__`   → resolved absolute path of `Join-Path $LoomHome 'observer'`
     (Windows uses `\`; observer's Go code passes the string through
     `sql.Open("sqlite", ...)` which accepts `\` on Windows —
     `internal/observerstore/store.go:50`)
   - `__WS_APIKEY__`   → freshly-generated 32-hex bootstrap api-key
     (via `[byte[]]::new(16); [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b); ($b|%{$_.ToString('x2')}) -join ''`)
   Written 0600 (`Set-Content` + `icacls ... /inheritance:r /grant:r "${env:USERNAME}:F"`
   for approximate parity — Windows ACLs are a coarser tool than POSIX
   0600 but the effect is "only the current user has R/W"). Then
   `Start-Process (Join-Path $BinDir 'observer-server.windows-amd64.exe')
   -ArgumentList '-config',(Join-Path $LoomHome 'observer\observer.yaml')`.

   deploy.ps1 ships a companion `deploy/windows/observer/config.yaml.template`
   (**this worktree owns** — see §1) that is a byte-for-byte copy of
   `deploy/linux/observer/config.yaml.template` **plus** a leading
   `# duplicated from linux/observer/config.yaml.template; keep in sync
   — enforced by test_template_parity.bats` header comment. A bats
   test (`test_template_parity.bats`, listed under §6) asserts
   equality modulo the header line so any placeholder drift in the
   linux template forces this file to be updated — the Windows
   inline-render substitutes the same three keys and would silently
   leave `__NEW_PLACEHOLDER__` unresolved if we didn't gate on parity.
   The Windows template exists purely so the Windows inline-render has
   an in-tree source; deploy.ps1 reads `deploy/windows/observer/config.yaml.template`,
   strips the header line, and substitutes.

3. **slave-agent** — invoke slave's install.ps1 with its real
   pre-existing parameter set (no fabricated `-Mode`; every parameter
   below matches `multi-agent/deploy/windows/slave/install.ps1`
   `param(...)`):

   ```powershell
   & deploy/windows/slave/install.ps1 `
       -Name         'eval-slave' `
       -ObserverUrl  ("http://127.0.0.1:" + $ObserverPort) `
       -Workspace    'ws-eval-auto' `
       -LoomHome     (Join-Path $LoomHome 'slave') `
       -Bin          (Join-Path $BinDir 'slave-agent.windows-amd64.exe')
   ```

   Then post-process the just-rendered `config.yaml` in place to
   (i) inject stub-issued credentials from `agentserver-stub issue`,
   (ii) set `server.url` to `http://127.0.0.1:$StubPort`,
   (iii) set `daemon.auto_start: false`,
   (iv) set `daemon.listen: 127.0.0.1:$SlavePort`.

4. **driver-agent** — invoke driver's install.ps1 with its real
   pre-existing parameter set (`multi-agent/deploy/windows/driver/install.ps1`
   `param(...)`):

   ```powershell
   & deploy/windows/driver/install.ps1 `
       -Project      (Join-Path $LoomHome 'driver') `
       -Name         'eval-driver' `
       -ObserverUrl  ("http://127.0.0.1:" + $ObserverPort) `
       -Workspace    'ws-eval-auto' `
       -Bin          (Join-Path $BinDir 'driver-agent.windows-amd64.exe')
   ```

   Then post-process `config.yaml` to inject credentials + set
   `server.url` (same as slave). Finally `Start-Process` the
   `driver-agent.windows-amd64.exe serve-daemon --config ... --listen
   127.0.0.1:$DriverPort` — again with cleared+whitelisted env.

Every edit is made by deploy.ps1's own YAML helper — the two install.ps1
files are never modified. See §3.1's Linux table for the analogous
observer/slave/driver breakdown; the shapes are identical.

In `-Prod`, `deploy.ps1` does NOT invoke `install.ps1` at all —
install.ps1's `Set-Content -LiteralPath $configPath` at line 132 would
clobber the operator's already-registered `credentials.proxy_token`
back to `""` (see `deploy/windows/slave/config.yaml.template:5-10` for
the blank template). The operator's `install.ps1` + device-code run is
a pre-existing prerequisite; deploy.ps1 --prod only spawns the four
processes from the already-registered `$LoomHome` tree. See §4.2 prod
table.

On Linux hosts (where `deploy.ps1` runs only for `pwsh` syntax / Pester
tests), the slave leg no-ops with a clear "windows-only path skipped on
non-Windows host" log line — never a hard error, so Linux CI can exercise
`-DryRun` on the PowerShell file end-to-end.

### 3.3 Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. Foreground handle: all 4 processes healthy after readiness gate. `--dry-run`: plan printed. |
| `2` | Preflight failure — bad flag, bad port, non-loopback stub bind, install.ps1 boundary violation attempted (§7(a),(e),(g)). |
| `3` | Sub-installer failed (observer/driver/slave install.sh non-zero). Script prints which one and forwards its stderr. |
| `4` | Readiness gate timeout — one of the four ports did not come up within `LOOM_DEPLOY_READY_TIMEOUT_SEC` (default 30). |
| `5` | Topology emit failed (hostname redaction error, JSON serialisation error). |

Consumers can distinguish preflight (2) from runtime (3–5) to decide whether
to retry or abort.

---

## 4. Execution pipeline

### 4.1 Overall order

1. **Preflight** (§7(c) fail-fast — `set -euo pipefail` / `$ErrorActionPreference = 'Stop'`
   as the first executable line).
2. Parse + validate flags (§7(e) blacklist; §7(a) stub-loopback check).
3. Build subprocess env (§7(g) whitelist).
4. If `--dry-run`: print plan (§4.3, sanitised per §7(h)) → exit 0.
5. Otherwise: spawn each of the 4 processes into the background via
   `nohup ... &` (Linux) / `Start-Process -PassThru -WindowStyle Hidden`
   (Windows), recording each PID + stderr-log path in
   `$LOOM_HOME/.pids/<role>.pid` (0600) and appending to
   `SPAWNED_PIDS` for the trap handler.
6. Wait for readiness gates (§4.2 per-mode tables).
7. Emit topology JSON (§5).
8. **Exit 0** — deploy.sh is NOT a long-running supervisor. The four
   spawned subprocesses continue running under the operator's session
   (each was `nohup`ed and had its own stderr redirected to a per-role
   log under `$LOOM_HOME/<role>/logs/`). This mirrors
   `tests/prod_test/run_e2e.sh:189-219` (nohup + disown; then the
   script exits and leaves the daemons to be torn down by
   `run_e2e.sh`'s cleanup or by a follow-up `kill`).

### 4.1.1 Teardown

The operator (or `run_e2e.sh`, or the eval-runner) is responsible for
teardown. deploy.sh writes `$LOOM_HOME/.pids/` for exactly this
purpose:

```
$LOOM_HOME/.pids/
├── agentserver-stub.pid   # only in --mode stub
├── observer.pid
├── slave.pid
└── driver.pid
```

There is a companion `deploy.sh --shutdown [--loom-home DIR]` mode:
reads every `*.pid` under `$LOOM_HOME/.pids/`, sends `SIGTERM`, waits
up to 5 s, then `SIGKILL`s any survivor. `--shutdown` also removes
`.pids/`. `deploy.ps1 -Shutdown` is the Windows counterpart
(`Stop-Process -Id ... -Force` after a 5 s grace).

**On preflight or spawn failure**, the trap handler kills every PID
already in `SPAWNED_PIDS` (same SIGTERM → 3 s → SIGKILL sequence),
removes any partially-created `.pids/` files, and exits with the
appropriate error code (§3.3).

### 4.2 Mode-specific sub-plan

**Terminology note — two independent URLs.** Every agent config has
**both** a `server.url` (agentserver — where the yamux tunnel dials) and
`observer.url` (local observer — where the commander daemon dials). They
are distinct: 12号 §D6c "stub 模式" is about `server.url` swapping between
the stub and prod agentserver; `observer.url` in **both** modes points at
the local observer we start on `127.0.0.1:$OBSERVER_PORT`. Confusing the
two produces the "prod-mode wires agentserver as observer" bug.

**Stub mode's tunnel-plane + task-poll gap — scope down to liveness.**
`agentserver-stub` implements only the HTTP surface `/register`,
`/whoami`, `/heartbeat`, `/healthz`
(`tools/eval/agentserver-stub/server.go:74-80`). It does NOT implement:
(i) the yamux tunnel plane at `/api/agent/agents-tunnel` (README table:
"Tunnel routing: Token issued; no connection state"), and (ii) the
task-dispatch endpoints `GET /api/agent/tasks/poll` /
`/api/agent/tasks/{id}` / `PUT /api/agent/tasks/{id}/status` that
`internal/poller/poller.go:97,137,213` calls.

**Consequence: `--mode stub` brings up the processes and proves they
can reach the stub for identity resolution, but end-to-end
driver-to-slave task dispatch does NOT work against the stub — it needs
either prod agentserver or a future stub extension.** This matches the
scope of Phase 1 WT-1-eval-runner-skeleton
(`docs/specs/wt1-eval-runner-skeleton.spec.md` §1: "Explicitly out of
scope for this skeleton: spinning up driver-agent / slave-agent /
observer-server binaries to execute the workload"). deploy.sh --stub
delivers the D6c "拉起完整栈" as *process liveness* plus identity /
config wiring — the harness that will replace the skeleton's mock
workspace stage is a separate worktree (12号 §D6c does not scope
end-to-end task execution to WT-2). §8 acceptance clauses reflect this
scoping explicitly.

Concrete deploy.sh configuration in `--mode stub`, entirely inside
YAML post-processing (no source changes to slave-agent / driver-agent /
stub in this worktree):

1. After `deploy/linux/slave/install.sh` renders `config.yaml`, deploy.sh
   patches these fields (via `yq -i`; `yq` presence is a preflight
   requirement per §4.5):
   - `server.url: http://127.0.0.1:$STUB_PORT` (was `https://agent.cs.ac.cn`).
   - `credentials.{sandbox_id,tunnel_token,proxy_token,short_id,workspace_id}`:
     values from `agentserver-stub issue --role slave` (see §4.5).
   - **`daemon.auto_start: false`** — opts out of the tunnel-gated
     auto-daemon path in `cmd/slave-agent/main.go:405-407` (`AutoStart`
     is a `*bool` in `internal/config/config.go:37`; explicit `false`
     is honoured over the auto policy per that function's contract, and
     the `_test.go:244` case "explicit auto_start=false must override
     the auto policy" is the regression guard). The commander daemon
     does NOT come up in stub mode.
   - `daemon.listen: 127.0.0.1:$SLAVE_PORT` — recorded for uniformity
     and topology emit, but not probed in stub mode (see §4.2 stub
     table readiness gate below).
2. `slave-agent` is started in stub mode. `tn.Run`
   (`internal/tunnel/tunnel.go:197`) enters an infinite reconnect loop
   with exponential backoff against the stub's non-existent
   `/api/agent/agents-tunnel` (agentsdk `Client.Connect` per
   `agentserver/pkg/agentsdk/client.go:106,128` — retries forever, does
   not crash). `poller.Run`
   (`internal/poller/poller.go:55`) enters its own idle-poll loop
   against `/api/agent/tasks/poll` — the stub returns 404 which the
   poller treats as a non-fatal `poll status 404`
   (`internal/poller/poller.go:107-108`; the loop backs off and
   retries, never exits — `Run` only returns on `ctx.Done()`). The
   slave process therefore stays alive but does no task work.
   Readiness gate for slave in stub mode is *not* a TCP port bind or a
   task poll — it is one successful HTTP round-trip to the stub's
   `/api/agent/whoami` using the slave's `proxy_token`, proving the
   token is valid and the slave was configured against the correct
   stub URL. T6c validates this shape.

Same treatment for driver in stub mode:

3. `deploy/linux/driver/install.sh` renders `config.yaml`; deploy.sh
   patches `server.url` + `credentials.*` the same way, seeded from
   `agentserver-stub issue --role driver`. `driver-agent register` is
   **not** run in stub mode (the register subcommand does a real
   device-code flow against `server.url` — would loop on the stub).
4. `driver-agent serve-daemon --config ... --listen 127.0.0.1:$DRIVER_PORT`
   is spawned. Its `sdk.Connect` (`cmd/driver-agent/main.go:214`) enters
   the same reconnect loop against the stub's absent tunnel path. But
   `commander.NewDaemon` binds `--listen` synchronously in
   `cmd/driver-agent/main.go:352-395` before any tunnel work — there is
   no `<-tn.Ready()` gate on the driver path (`grep -n 'tn.Ready\|Ready()' cmd/driver-agent/main.go`
   returns nothing). Readiness gate for the driver in stub mode is TCP
   `$DRIVER_PORT` LISTEN + any HTTP response (401 acceptable). Validated
   by T6b.

**`--mode stub` bring-up order:**

| Order | Process | Command (canonical form) | Readiness gate |
|---|---|---|---|
| 1 | `agentserver-stub` | `bin/agentserver-stub --listen 127.0.0.1:$STUB_PORT --workspace-id auto` (loopback pinned; see §7(a)) | `curl http://127.0.0.1:$STUB_PORT/healthz` == 200 |
| 2 | `observer-server` | invoke `deploy/linux/observer/install.sh --name eval-obs --loom-home $LOOM_HOME/observer --listen 127.0.0.1:$OBSERVER_PORT --api-key <random32hex>` (all flags present on `install.sh:26-42`); then background-spawn `$LOOM_HOME/observer/observer-server -config $LOOM_HOME/observer/observer.yaml`. | `127.0.0.1:$OBSERVER_PORT` TCP LISTEN |
| 3 | `slave-agent` | invoke `deploy/linux/slave/install.sh --name eval-slave --loom-home $LOOM_HOME/slave --observer-url http://127.0.0.1:$OBSERVER_PORT --workspace ws-eval-auto` (all flags present on `install.sh:24-38`); patch `server.url` + credentials + `daemon.auto_start: false` + `daemon.listen: 127.0.0.1:$SLAVE_PORT` per steps 1-2 above; then background-spawn `$LOOM_HOME/slave/slave-agent $LOOM_HOME/slave/config.yaml`. | **stub-mode:** `curl -H "Authorization: Bearer <slave.proxy_token>" http://127.0.0.1:$STUB_PORT/api/agent/whoami` returns 200 (asserts the slave's poller connected). $SLAVE_PORT is **not** probed in stub mode (daemon off). |
| 4 | `driver-agent serve-daemon` | invoke `deploy/linux/driver/install.sh --project $LOOM_HOME/driver --name eval-driver --observer-url http://127.0.0.1:$OBSERVER_PORT` (all flags present on `driver/install.sh:24-40`); patch `server.url` + credentials per step 3; then background-spawn `$LOOM_HOME/driver/driver-agent serve-daemon --config $LOOM_HOME/driver/config.yaml --listen 127.0.0.1:$DRIVER_PORT`. | `127.0.0.1:$DRIVER_PORT` TCP LISTEN + any HTTP response (401 acceptable). |

**`--mode prod` bring-up order.** Prod mode is a **spawn-only wrapper**;
it does NOT invoke any sub-installer, because both
`deploy/linux/slave/install.sh:140-150` and
`deploy/windows/slave/install.ps1:119-132` unconditionally re-render
`config.yaml` from the template — which would clobber the operator's
already-registered `credentials.proxy_token` back to `""`
(templates: `deploy/linux/slave/config.yaml.template:5-10`,
`deploy/windows/slave/config.yaml.template:5-10`). deploy.sh --prod
therefore assumes install.sh / install.ps1 (or the runbook's manual
device-code steps in `tests/prod_test/E2E_RUNBOOK.md:230-244`) already
ran previously and left a valid `$LOOM_HOME/{observer,slave,driver}/`
tree.

Preflight for `--mode prod` explicitly requires:
- `$LOOM_HOME/observer/observer.yaml` exists (readable) AND its
  `listen_addr` matches `127.0.0.1:$OBSERVER_PORT` (if CLI-specified) or
  is copied into `$OBSERVER_PORT` (if not).
- `$LOOM_HOME/slave/config.yaml` exists AND `credentials.proxy_token`
  is non-empty.
- `$LOOM_HOME/driver/config.yaml` exists AND
  `credentials.proxy_token` is non-empty AND
  `credentials.short_id` is non-empty (matches the
  `cmd/driver-agent/main.go:318,325` guards).
- The pre-existing binaries under `$LOOM_HOME/*/` exist and are
  executable.

If any check fails: exit 2 with a message naming the missing file /
empty field and pointing at `tests/prod_test/E2E_RUNBOOK.md:83-108`
(Step 0-1 rebuild+prereg).

| Order | Process | Notes | Readiness gate |
|---|---|---|---|
| 1 | *(skip stub)* | no agentserver-stub in prod. | — |
| 2 | `observer-server` | Background-spawn `$LOOM_HOME/observer/observer-server -config $LOOM_HOME/observer/observer.yaml`. deploy.sh does NOT re-run `deploy/linux/observer/install.sh` in prod mode — the operator already ran it and any api-key / workspace-id state is baked into the yaml. | `127.0.0.1:$OBSERVER_PORT` TCP LISTEN |
| 3 | `slave-agent` | Background-spawn `$LOOM_HOME/slave/slave-agent $LOOM_HOME/slave/config.yaml`. deploy.sh does NOT re-run `install.sh` and does NOT edit `config.yaml` in prod mode — every credential and daemon field is left as the operator last saved it. | Full auto-daemon path: real prod agentserver signs tunnel, `tn.Ready()` closes, commander daemon binds `daemon.listen` (as configured in the yaml — deploy.sh reads it back and probes that address; `--slave-port` CLI is NOT used to override in prod mode — it is validated only against the yaml's actual `daemon.listen` port and mismatches abort preflight with an "operator-registered slave uses port X; --slave-port must match or be omitted" error). Readiness: TCP `$SLAVE_PORT` LISTEN. |
| 4 | `driver-agent serve-daemon` | Same "no install.sh, no config edits" posture. Spawn `$LOOM_HOME/driver/driver-agent serve-daemon --config $LOOM_HOME/driver/config.yaml --listen 127.0.0.1:$DRIVER_PORT`. Driver's `--listen` CLI flag is authoritative (line 336 in driver-agent/main.go); safe to pass. | TCP `$DRIVER_PORT` LISTEN + any HTTP response. |

`--mode prod` is deliberately thin — process start ordering + readiness
gates + topology emit. Zero writes to `$LOOM_HOME/*/config.yaml` or to
disk under the operator's registered directories. §7(b) is therefore
trivially maintained: prod mode never handles OAuth tokens because it
never touches the files that store them.

The Windows counterpart in `deploy.ps1 -Prod` follows the same
posture: no `install.ps1` invocation at all in prod, and the four
`Test-NetConnection` + `Invoke-WebRequest` gates from the Linux table
translated verbatim. **`deploy.ps1` only invokes `install.ps1` in
`-Stub` mode** — the §3.2 command block above documents the stub-only
invocation; in `-Prod` the "call install.ps1" arrow is not drawn.

### 4.3 `--dry-run` output shape

Single JSON document on stdout, pretty-printed for humans, ending with a
trailing newline. Structure:

```json
{
  "mode": "stub",
  "host": "sha256(hostname)[:8]",
  "os": "linux",
  "arch": "amd64",
  "component_ports": {
    "agentserver_stub": 18080,
    "observer": 18091,
    "driver": 18092,
    "slave": 18093
  },
  "planned_commands": [
    ["/abs/path/bin/agentserver-stub", "--listen", "127.0.0.1:18080", "--workspace-id", "auto"],
    ["/abs/path/deploy/linux/observer/install.sh", "--name", "eval-obs", "--loom-home", "/root/.loom/eval-deploy/observer", "--listen", "127.0.0.1:18091", "--api-key", "<REDACTED>"],
    ["/abs/path/$LOOM_HOME/observer/observer-server", "-config", "/root/.loom/eval-deploy/observer/observer.yaml"],
    ["/abs/path/deploy/linux/slave/install.sh", "--name", "eval-slave", "--loom-home", "/root/.loom/eval-deploy/slave", "--observer-url", "http://127.0.0.1:18091", "--workspace", "ws-eval-auto"],
    ["/abs/path/bin/agentserver-stub", "issue", "--server", "http://127.0.0.1:18080", "--role", "slave", "--short-id", "slv-eval-001"],
    ["<yq-patch>", "/root/.loom/eval-deploy/slave/config.yaml", "server.url=http://127.0.0.1:18080", "credentials.*=<REDACTED>", "daemon.auto_start=false", "daemon.listen=127.0.0.1:18093"],
    ["/abs/path/$LOOM_HOME/slave/slave-agent", "/root/.loom/eval-deploy/slave/config.yaml"],
    ["/abs/path/deploy/linux/driver/install.sh", "--project", "/root/.loom/eval-deploy/driver", "--name", "eval-driver", "--observer-url", "http://127.0.0.1:18091"],
    ["/abs/path/bin/agentserver-stub", "issue", "--server", "http://127.0.0.1:18080", "--role", "driver", "--short-id", "drv-eval-001"],
    ["<yq-patch>", "/root/.loom/eval-deploy/driver/config.yaml", "server.url=http://127.0.0.1:18080", "credentials.*=<REDACTED>"],
    ["/abs/path/$LOOM_HOME/driver/driver-agent", "serve-daemon", "--config", "/root/.loom/eval-deploy/driver/config.yaml", "--listen", "127.0.0.1:18092"]
  ],
  "planned_env_whitelist": {
    "always": ["PATH", "HOME", "LANG", "LC_ALL", "TZ", "USER"],
    "if_set": ["AGENTSERVER_ROOT", "MODELSERVER_ROOT", "APP_ROOT", "MOCK_MODEL_URL"],
    "prefix": ["LOOM_*"]
  },
  "loom_home": "/root/.loom/eval-deploy",
  "bin_dir": "/abs/path/multi-agent/deploy/linux/bin"
}
```

Rules:

- Every `argv[]` entry is emitted as a **list of strings**, not a shell-quoted
  string — reviewers can diff without shell-quoting confusion, and §7(h)
  redaction is simpler because there's no place for OAuth material to hide.
- **In `--mode prod`, `agentserver_stub` is absent from `component_ports`
  and `planned_commands`.** This is asymmetric on purpose — the two shapes
  are distinguishable so eval-runner + topology writer never conflate.
- If any command *would* have carried an OAuth token / API key (e.g.
  `--api-key XYZ`), the value in the printed argv is replaced with the
  literal string `"<REDACTED>"` (see §7(h)). Since `--mode prod` refuses to
  auto-supply tokens, in practice this only ever redacts the pre-existing
  installer's optional `--api-key` if the operator provides it via env
  (`LOOM_API_KEY`) — deploy.sh knows to redact both env and argv positions.
- `host` is already redacted (§7(d)); the raw hostname never appears.

### 4.4 Readiness gates

Each background spawn is followed by that row's mode-conditional
readiness gate (see the last column of the §4.2 tables). A hard timeout
(default 30 s, override `LOOM_DEPLOY_READY_TIMEOUT_SEC`) bounds each
gate individually. If any gate times out: exit 4, `kill -TERM` every PID
in `SPAWNED_PIDS` (SIGKILL after a 3-s grace), print which gate failed
and forward the last 30 lines of that subprocess's stderr.

On Windows, TCP LISTEN probes use `Test-NetConnection -ComputerName
127.0.0.1 -Port $port -InformationLevel Quiet`; HTTP probes use
`Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -SkipHttpErrorCheck`.

### 4.5 Stub credential seeding (mode=stub only)

`agentserver-stub` has an `issue` subcommand
(`tools/eval/agentserver-stub/main.go`; documented in that README). For each
of driver / slave, `deploy.sh` runs:

```
bin/agentserver-stub issue --server http://127.0.0.1:$STUB_PORT \
    --role driver --short-id drv-eval-001 > $LOOM_HOME/driver/creds.json
```

Then `deploy.sh` merges `sandbox_id` / `tunnel_token` / `proxy_token` /
`workspace_id` / `short_id` into the rendered `config.yaml` via `yq` (Linux)
/ inline YAML string edits (PowerShell), using a fixed key list — no
templated shell interpolation of token contents (§7(b) applies uniformly).

If `yq` is not installed, we fail preflight with a clear "install yq or use
`--mode prod`" message rather than falling back to `sed` (which risks
mis-quoting tokens).

---

## 5. `machine_topology` payload

### 5.1 Field set

```json
{
  "schema_version": 1,
  "host": "<sha256(hostname)[:8]>",
  "os": "linux" | "windows",
  "os_release": "<uname -sr>" | "<Windows version>",
  "arch": "amd64" | "arm64" | "aarch64",
  "kernel": "<uname -r>" | "",
  "cpu_count": <int>,
  "mem_bytes": <int64>,
  "mode": "stub" | "prod",
  "component_ports": {
    "agentserver_stub": 18080,   // omitted if mode == "prod"
    "observer": 18091,
    "driver": 18092,
    "slave": 18093
  },
  "collected_at_unix": <int64>,   // seconds; provenance-only, not primary key
  "deploy_script_version": "wt2-deploy-scripts@<git-sha[:12]>"
}
```

Total payload is capped at 4 KiB after JSON serialisation. Emitters SHOULD
be well under that (typical ~350 B); the cap is a safety rail against a
future field addition that accidentally embeds a log file, and defends the
`Schema.MachineTopology` 8 KiB `ErrOversizedField` limit in
`internal/evalrun/schema.go` (which would fail the eval-runner insert).

### 5.2 JSON schema

Committed at `multi-agent/deploy/topology_schema.json` (draft-07). §6 test
matrix has a schema-validate assertion — this file is the contract, not the
prose above. If §5.1 and the schema disagree, the schema wins; a future
worktree extending the payload must update both.

### 5.3 Redaction rules

- `host` — SHA-256 of the raw hostname, first 8 hex digits. If hostname
  contains `@` (some corporate laptops embed email-shaped identifiers, e.g.
  `alice@corp-laptop`), split at the last `@`, hash each side independently,
  and rejoin as `"<hash>@<hash>"`. Never include the raw hostname.
- `os_release` — the free-form uname string may contain build metadata
  identifying an individual machine (e.g. `#1 SMP PREEMPT_DYNAMIC Wed Dec
  20 09:33:12 UTC 2023 hostname=alice-laptop`). We strip any `hostname=`
  key-value substring and any bare token matching the raw hostname before
  hashing.
- `os_release` and `kernel` may still contain per-host build IDs (e.g.
  `#54~22.04.1-Ubuntu`); we treat that as acceptable — it's population-level
  information the paper needs for reproducibility, not individual PII.
- The current `$USER` value is **never** included. `PATH`, `HOME`, and
  other env values are never included.

### 5.4 Where the emit goes

- **Default**: JSON blob is the final line of stdout (single-line, minified,
  trailing newline). Everything else the script prints goes to stderr, so
  `deploy.sh 2>/dev/null | tail -n1 | jq .` recovers the payload cleanly.
- **`--topology-out PATH`**: JSON blob is minified into `PATH`, created
  0600, atomically (`write to <PATH>.tmp then rename`). No stdout emit in
  this mode.
- **Not implemented in this worktree**: direct writes into the observer
  `runs` table. The eval-runner is the sole D1 writer
  (`internal/evalrun/writer.go`); we produce the string, it inserts.
- **`--dry-run`**: no topology emit at all (dry-run's own JSON already
  includes the port map; a second JSON blob would confuse eval-runner
  parsers).

---

## 6. Test matrix

### 6.1 Runner commands

```bash
# Bash entrypoint (bats-core) — worktree owns two bats files
bats multi-agent/deploy/linux/deploy_test.bats
bats multi-agent/deploy/test_template_parity.bats

# PowerShell entrypoint (Pester 5)
pwsh -c 'Invoke-Pester multi-agent/deploy/windows/deploy.Tests.ps1 -Output Detailed'

# install.ps1 boundary assertion (git; runs anywhere with git installed)
git diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1
# must produce empty output; test framework asserts on exit-status of the
# accompanying `[ -z "$(git diff ...)" ]` guard.
```

### 6.2 Assertions — one row per security clause + one per functional axis

| # | Assertion | Covers |
|---|---|---|
| T1 | `deploy.sh --mode stub --dry-run` output matches golden JSON in `deploy/linux/testdata/dryrun.stub.golden.json` (with per-run fields — abs paths, git SHA — normalized before compare). | §4.3 shape, §7(h) no-secret-in-dry-run |
| T2 | `deploy.sh --mode prod --dry-run` output omits `agentserver_stub` from `component_ports` and `planned_commands`. | §4.2 prod branch |
| T3 | `deploy.sh --mode stub --stub-port 22 --dry-run` exits 2 with stderr matching `well-known port`. | §7(e) blacklist |
| T4 | `deploy.sh --mode stub --observer-port 100000 --dry-run` exits 2 (out of range). | §3.1 range check |
| T5 | `deploy.sh --mode stub --observer-port 18091 --driver-port 18091 --dry-run` exits 2 (collision). | §3.1 pairwise-distinct |
| T6 | Fake `agentserver-stub` binary that logs its own argv is invoked; the argv MUST start with `--listen 127.0.0.1:` (asserts §7(a)). Achieved by shim-substituting `bin/agentserver-stub` with a bash stub in a tempdir, running deploy.sh with `--bin-dir <tempdir>`, and grepping the shim's log. | §7(a) stub loopback-only |
| T6b | With shim `bin/agentserver-stub` returning 200 on `/healthz` but stalling on `/api/agent/agents-tunnel` (mimics the real stub's no-tunnel-plane behaviour), assert `deploy.sh --mode stub` **does** reach the driver-readiness gate — TCP `$DRIVER_PORT` LISTEN + any HTTP response — within `LOOM_DEPLOY_READY_TIMEOUT_SEC`. This falsifies the Round-1 P0 that the tunnel-plane gap wedges bring-up. | §4.2 stub tunnel workaround |
| T6c | With the same shim, assert the slave-agent process was started with a rendered `config.yaml` whose `daemon.auto_start: false` and whose `server.url` == `http://127.0.0.1:$STUB_PORT`. Grep the file after deploy.sh reaches its readiness gate. | §4.2 slave config patching |
| T7 | Attempt to override loopback: assert deploy.sh has **no** flag whose parsing would allow the user to pass `--listen 0.0.0.0:...` through to the stub. Achieved by grep: `! grep -E "listen.*\\$STUB_HOST\|--listen[^\"]*0\\.0\\.0\\.0" deploy.sh`. Plus: with `LOOM_STUB_LISTEN=0.0.0.0:18080` in the env, deploy.sh still passes `127.0.0.1:$STUB_PORT` to the stub (env override MUST NOT change bind host — the port is configurable via `--stub-port`; the host is hard-coded to `127.0.0.1`). | §7(a) |
| T8 | Run deploy.sh with `LOOM_API_KEY=SECRET_TOKEN_1234 deploy.sh --mode prod --dry-run`; assert stdout+stderr contain neither `SECRET_TOKEN_1234` nor a base64 encoding of it (`grep -q "$(printf %s SECRET_TOKEN_1234 \| base64)"`). | §7(h) |
| T9 | With a synthetic `config.yaml` containing `proxy_token: PROXY_TOKEN_SECRET`, run the `--mode prod` real (non-dry-run) preflight in a fixture that stops before spawning; assert no file under `$LOOM_HOME` — other than the pre-existing `config.yaml` we placed — contains `PROXY_TOKEN_SECRET`. | §7(b) |
| T10 | `head -1 deploy.sh` matches `#!/usr/bin/env bash` and the *first executable* line matches `set -euo pipefail`; `head -20 deploy.ps1 \| grep -q '^\$ErrorActionPreference = .Stop.'` before the first meaningful command. | §7(c) |
| T11 | With `hostname` = `alice@corp-laptop`, running the topology-emit helper produces a `host` field matching `^[0-9a-f]{8}@[0-9a-f]{8}$` and containing neither `alice` nor `corp-laptop` in any field. | §7(d), §5.3 |
| T12 | Topology JSON produced by real (non-dry-run) `--mode stub` run validates against `topology_schema.json`. | §5.1 / §5.2 |
| T13 | `pwsh -c "& { . deploy.ps1 -Mode stub -DryRun }"` on Linux emits a `component_ports` block with 4 keys and exits 0. | §3.2 parity |
| T14 | `Get-Command Set-ExecutionPolicy` invocation in Pester asserts the deploy.ps1 file bears **no** call to `Set-ExecutionPolicy` (grep: `! Select-String -Pattern 'Set-ExecutionPolicy' -Path deploy.ps1`). Plus: the file is unsigned by default; a Pester assertion checks `(Get-AuthenticodeSignature deploy.ps1).Status -eq 'NotSigned'` (see §7(f) for the deliberate no-modify posture). | §7(f) |
| T15 | **Negative (drop):** fake `observer-server` binary logs its own env; run deploy.sh with parent env `AWS_ACCESS_KEY_ID=SHOULD_NOT_LEAK`, `GITHUB_TOKEN=SHOULD_NOT_LEAK`, `DOCKER_CONFIG=SHOULD_NOT_LEAK`, `NPM_TOKEN=SHOULD_NOT_LEAK` set. Assert the fake's env log contains none of those keys or their values. | §7(g) drop-by-default |
| T15b | **Positive (allow):** with parent env `PATH=/x`, `HOME=/y`, `LOOM_OBSERVER_URL=http://127.0.0.1:18091`, `MOCK_MODEL_URL=http://127.0.0.1:9090`, `AGENTSERVER_ROOT=/z`, assert the fake's env log DOES include exactly these keys with the given values (whitelist actually passes the allowed set through; drift from `subprocess.go`'s list would fail this test). | §7(g) whitelist parity |
| T15c | **Model-key gate:** with parent env `OPENAI_API_KEY=SECRET_MODEL_KEY_9zzz` and `ANTHROPIC_API_KEY=SECRET_MODEL_KEY_8yyy`: (i) `deploy.sh --mode stub` → keys ABSENT from every subprocess env log (stub drops even with the flag); (ii) `deploy.sh --mode prod` (with a synthetic pre-registered config) → keys ABSENT (flag not passed); (iii) `deploy.sh --mode prod --allow-model-key-passthrough` → keys PRESENT and a `passing OPENAI_API_KEY through` WARN line appears on stderr; (iv) `deploy.sh --mode stub --allow-model-key-passthrough` → exit 2 preflight error. | §7(g) prod-only opt-in |
| T15d | `grep -c "allow-model-key-passthrough" deploy.sh` returns exactly 3 (one CLI parse, one WARN log, one preflight guard); grep on deploy.ps1 for `AllowModelKeyPassthrough` returns exactly 3. Prevents a future maintainer from silently deleting the gate. | §7(g) grep-invariant |
| T16 | `git diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1` produces empty output (bats test invokes git and asserts). | §0 boundary |
| T17 | End-to-end (mode=stub): fresh Linux host, `bash deploy.sh --stub` runs to completion (exit 0). Readiness gates as per §4.2 stub table: stub `:$STUB_PORT` /healthz 200; observer `:$OBSERVER_PORT` TCP LISTEN; driver `:$DRIVER_PORT` TCP LISTEN + any HTTP response; slave: stub `/api/agent/whoami` with slave proxy_token returns 200. (Slave commander daemon is intentionally OFF in stub mode per §4.2; `:$SLAVE_PORT` is NOT probed — asserting it would contradict `daemon.auto_start: false`.) Topology JSON on last stdout line validates against schema. This is the primary acceptance from todo_list "在 fresh Linux/Windows 上 ... 拉起完整栈" — "完整栈" here means all four processes running and reaching their mode-appropriate liveness gates; end-to-end task dispatch is out of scope (see §4.2 "scope down to liveness"). | §4.1, §4.2 stub table, §5, todo_list line 105 |
| T17b | End-to-end (mode=prod) preflight-happy path — fixture with pre-registered (mocked) `$LOOM_HOME/{observer,slave,driver}/` tree; `deploy.sh --prod` exits 0 with all four TCP-LISTEN gates. Real prod agentserver is not called (fixture stubs the network); the test validates deploy.sh's own preflight + spawn ordering. | §4.2 prod table |
| T17c | End-to-end (mode=prod) preflight-fail path — same fixture but with `credentials.proxy_token: ""` in `slave/config.yaml`; assert exit 2 and stderr names `slave/config.yaml` + `credentials.proxy_token` + points at E2E_RUNBOOK. | §4.2 prod preflight |
| T18 | End-to-end (mode=stub) exits 0 with `$LOOM_HOME/.pids/{agentserver-stub,observer,slave,driver}.pid` present and each containing a `kill -0`-live PID. | §4.1 spawn-then-exit contract |
| T18b | After T18, `deploy.sh --shutdown --loom-home $LOOM_HOME` exits 0; asserts every PID from the `.pids/` files is reaped within 5 s (each `kill -0` returns non-zero) and `.pids/` is removed. | §4.1.1 teardown |
| T18c | Readiness-gate failure: use a fake `agentserver-stub` shim that starts but never responds to `/healthz`; assert `deploy.sh --stub --loom-home <tmp>` exits **4** (readiness timeout — §3.3), the trap fires, any PIDs in `SPAWNED_PIDS` at time of failure are killed, and the partial `.pids/` directory is removed. This falsifies the "silently orphans processes on gate timeout" regression. | §3.3 exit 4, §4.1.1 trap on failure |
| T18d | Sub-installer failure (Linux only; PowerShell counterpart in Pester): shim `deploy/linux/slave/install.sh` (via `--bin-dir`-style redirection to a tempdir sub-installer set) that exits 1; assert `deploy.sh --stub --loom-home <tmp>` exits **3** (sub-installer failure — §3.3), no `slave.pid` is written, and the earlier `.pids/agentserver-stub.pid` + `.pids/observer.pid` are cleaned up by the trap. Distinguishes exit 3 vs exit 4 concretely. | §3.3 exit 3, §4.1.1 trap on failure |
| T19 | **Observer template parity** (`test_template_parity.bats`): assert that `multi-agent/deploy/windows/observer/config.yaml.template` equals `multi-agent/deploy/linux/observer/config.yaml.template` after stripping the Windows file's first header line (`# duplicated from ...`). Concretely: `diff <(tail -n +2 deploy/windows/observer/config.yaml.template) deploy/linux/observer/config.yaml.template` returns empty. Also assert the linux template contains exactly the three placeholder tokens `__LISTEN_ADDR__`, `__LOOM_HOME__`, `__WS_APIKEY__` — a future Linux template adding a fourth placeholder must update the Windows inline-render substitution set at the same time, or T19 fails (via a small `grep -oE '__[A-Z_]+__' | sort -u` comparison against a golden token list committed at `deploy/windows/observer/expected_placeholders.txt`). | §3.2 clause 2, §1 windows/observer template file |

### 6.3 Fresh-host acceptance (manual, out of automated suite)

**Both legs are required for PR merge** — todo_list line 105 says
"在 fresh Linux/Windows 上", not "or". The PR body records the actual
exit code + last-line topology JSON output for each host.

**Linux leg** (Ubuntu 22.04 LTS fresh VM or WSL2, no pre-existing
`~/.loom/`):

1. Clone repo, `cd multi-agent`. Detect arch:
   `ARCH=$(uname -m | sed s/x86_64/amd64/;s/aarch64/arm64/)`. Build with
   the arch-suffixed names the sub-installers expect
   (`deploy/linux/driver/install.sh:88-96`,
   `deploy/linux/slave/install.sh:94-105`,
   `deploy/linux/observer/install.sh:91-99`):
   ```bash
   for cmd in driver-agent slave-agent observer-server; do
     CGO_ENABLED=0 go build -o deploy/linux/bin/${cmd}.linux-${ARCH} ./cmd/${cmd}
   done
   go build -o deploy/linux/bin/agentserver-stub ./tools/eval/agentserver-stub
   ```
   deploy.sh's `--bin-dir` default (`multi-agent/deploy/linux/bin`)
   picks these up.
2. Run `bash deploy/linux/deploy.sh --stub`. Expect exit 0; observer
   `:18091` LISTEN; driver `:18092` LISTEN + HTTP any response; stub
   `:18080` /healthz 200; slave whoami round-trip 200 (§4.2 stub
   readiness); JSON topology on last stdout line validating against
   `topology_schema.json`.
3. Run `bash deploy/linux/deploy.sh --stub --dry-run`. Expect the JSON
   plan with all listed `planned_commands` from §4.3.

**Windows leg** (fresh Windows 11 or Windows Server 2022; PowerShell 7.4+):

4. Clone repo, build the Windows binaries via `GOOS=windows GOARCH=amd64
   go build ...` (or use pre-built release assets under
   `deploy/windows/bin/`).
5. `pwsh -NoProfile -ExecutionPolicy Bypass -File deploy/windows/deploy.ps1
   -Stub`. Expect exit 0 and the same four readiness observations
   translated to `Test-NetConnection` / `Invoke-WebRequest`. Topology JSON
   emit uses Windows-native cmdlets (`Get-CimInstance Win32_ComputerSystem`
   / `Get-CimInstance Win32_Processor`) for `cpu_count` / `mem_bytes`;
   the schema shape is identical.
6. `pwsh ... deploy.ps1 -Stub -DryRun`. Expect the same JSON plan shape
   as step 3 with Windows-style paths and the slave installer invoked
   via `install.ps1 -Name eval-slave -ObserverUrl ... -Workspace ...`
   (§3.2).

**No Windows escape hatch.** Steps 5–6 (fresh Windows host `-Stub`
plus `-DryRun`) are required for merge; the todo_list line-105 phrase
"在 fresh Linux/Windows 上" is a conjunction, not a disjunction, and
running deploy.ps1 only on Linux via Pester (T13) does not exercise the
Windows-native cmdlets (`Get-CimInstance`, `Test-NetConnection`,
`Invoke-WebRequest -SkipHttpErrorCheck`) that steps 5–6 depend on. A
contributor without immediate Windows access must arrange one before
opening the PR (Windows 11 ISO under any hypervisor is sufficient); the
PR reviewer's checklist includes verifying steps 5–6's captured
transcript is in the PR body.

---

## 7. Security (P0)

### (a) stub bind is loopback-only, non-overridable

**Threat**: an operator (or a compromised env var) points `agentserver-stub`
at `0.0.0.0:18080`. Because the stub has no OAuth, any host on the network
can call `POST /api/v1/agents/register` and mint a five-tuple that walks
straight into observer/driver/slave with full privileges. This is the
"最严重后果" spelled out in the WT-2 prompt.

**Mitigation**:
- The stub command line in `deploy.sh` / `deploy.ps1` is hard-coded to
  `--listen 127.0.0.1:$STUB_PORT`. There is **no** CLI flag on deploy.sh
  that lets the operator change the *host* portion of the bind — only
  `--stub-port` (which parses to an integer, ranged and blacklisted, and
  concatenates into `127.0.0.1:<int>`).
- There is no env-var override of the host either. `LOOM_STUB_LISTEN` is
  intentionally not read; §7(a) test T7 grep asserts the source of the
  script contains no reference to that env name or to the string
  `0.0.0.0`.
- If a future maintainer adds a flag that widens the bind, T7's grep
  breaks the build.

Rationale for non-override: the stub is warned "NOT FOR PRODUCTION" in its
own README and defaults to loopback there. Its `--listen 0.0.0.0` capability
exists for a legitimate future use (sealed multi-host eval cluster) but that
use case does NOT flow through the deploy scripts — it would run the stub
directly. Keeping deploy.sh strictly loopback removes the accidental-exposure
footgun in the far more common single-host bring-up.

### (b) OAuth material never touches disk from this script

**Threat**: in `--mode prod`, the operator hands deploy.sh an OAuth token
via an environment variable; the script writes the token into a temp file
under `$LOOM_HOME` or into a rendered config in a way that leaves it on
disk, where a shared CI runner (or a later reader) can pick it up.

**Mitigation**:
- deploy.sh (`--mode prod`) **does not accept** OAuth tokens on any flag
  and does not read them from the environment either. It does not run
  `driver-agent register` or the slave first-run login. Those are operator
  prerequisites (§4.2 prod branch); the token is already in `config.yaml`
  from the pre-existing `driver-agent register` / `slave-agent` flow. If
  those pre-existing files handle the token badly, that is not our regression
  to fix in this worktree — but we do not add a *new* on-disk path.
- The only credential material deploy.sh touches at all is the stub's
  `issue` output in `--mode stub`. That output is 5-tuple **stub** tokens
  (no cryptographic value off-host — the stub's HMAC secret is per-process,
  regenerated on every stub start), written into `$LOOM_HOME/*/config.yaml`
  with `chmod 0600` and never to stdout/stderr.
- `--mode prod` with no pre-existing `config.yaml` under `$LOOM_HOME`
  aborts (exit 2) with a message pointing at the pre-existing bootstrap;
  it does NOT auto-run the register subcommand (that would surface a
  device-code URL through unattended CI).
- Test T9 asserts no file created by deploy.sh contains the fixture
  `PROXY_TOKEN_SECRET` from a synthetic pre-existing config we planted.

### (c) Fail-fast preflight

**Threat**: a partially-executed script leaves half of the stack running,
racy state on disk, and no clear signal to a monitoring script.

**Mitigation**:
- **First executable line** of `deploy.sh` is `set -euo pipefail` (after the
  `#!/usr/bin/env bash` shebang and any comment-only header). T10 grep
  asserts this — "first executable line" here means the first non-shebang,
  non-comment, non-blank line.
- **First executable line** of `deploy.ps1` is `$ErrorActionPreference = 'Stop'`
  (immediately after `param(...)` and `Set-StrictMode -Version Latest`,
  matching the pre-existing style in `deploy/windows/driver/install.ps1`).
  T10 asserts.
- On any preflight failure, no subprocess is spawned. On any post-spawn
  failure, a `trap` handler kills every PID the script has recorded in a
  `SPAWNED_PIDS` array; PowerShell uses `try { } finally { }` around the
  spawn block.

### (d) Hostname / user redaction in `machine_topology`

**Threat**: hostnames like `alice@corp-laptop` embed corporate email
identifiers; `$USER` = `alice` narrows even further. Both would land in
D1 verbatim and, since D1 is the source of the paper's public dataset,
publish identifiable metadata about individual contributors.

**Mitigation**: §5.3 hashing rules; test T11 asserts a hostname containing
`@` produces a `<hash>@<hash>` shape and no substring of the original
survives. `$USER` is never included in any topology field. The redaction
uses SHA-256 (not truncated MD5 — collision resistance matters for the
provenance role even if only 8 hex digits are stored, because we're
choosing 8 chars for readability rather than security).

### (e) Port validation + well-known blacklist

**Threat**: `--observer-port 22` or `--observer-port 443` — bind attempts
against ports commonly used for real services; either fails cryptically or
races a legitimate daemon.

**Mitigation**:
- Range `[1024, 65535]` enforced by regex + comparison.
- Well-known blacklist: `22` (SSH), `23`, `25`, `53`, `80`, `110`, `143`,
  `443` (HTTPS), `465`, `587`, `993`, `995`, `3389` (RDP), `5432`
  (Postgres), `6379` (Redis), `8080` (common HTTP alt), `8443` (common
  HTTPS alt). Any of these = exit 2 with an actionable error.
- Pairwise distinctness — reject if any two of `--observer-port`,
  `--driver-port`, `--slave-port`, `--stub-port` collide.
- Note: we do NOT check availability at preflight (bind race window would
  give false confidence); the readiness gate (§4.4) is the source of truth.
  A collision with a real process on `:18091` surfaces as exit 4 (readiness
  timeout) with observer-server's own bind-fail log forwarded to stderr.

### (f) PowerShell script signing

**Threat**: on locked-down Windows, users hit `Set-ExecutionPolicy` failures
running `deploy.ps1`. Historical scripts have "fixed" this by telling the
user to `Set-ExecutionPolicy Bypass` globally — permanently weakening every
future script the user runs, not just deploy.ps1.

**Mitigation**:
- We do NOT own an Authenticode certificate for this project, so signing at
  release time is out of scope. The script is shipped unsigned; T14 asserts
  `(Get-AuthenticodeSignature deploy.ps1).Status -eq 'NotSigned'` to prevent
  a future commit from silently signing with a compromised cert.
- The **Windows README** (owned by this worktree) documents the correct
  invocation:
  ```powershell
  pwsh -NoProfile -ExecutionPolicy Bypass -File deploy.ps1 -Mode stub
  ```
  which sets Bypass **for that one process only** — no `Set-ExecutionPolicy`
  call at any scope from inside the script. T14 grep asserts deploy.ps1
  contains no `Set-ExecutionPolicy` call at all.
- If / when release automation adds Authenticode signing, that will happen
  in a separate PR that also flips T14's `-eq 'NotSigned'` assertion; this
  worktree commits `NotSigned` as the current-truth baseline.

### (g) Subprocess env whitelist

**Threat**: parent env (a CI runner, a developer's shell) leaks `AWS_*`,
`GITHUB_TOKEN`, `OPENAI_API_KEY`, or a similarly sensitive var into the
observer / driver / slave subprocess. Any of those inheriting into a Codex
CLI invocation could result in the LLM inadvertently reading them, echoing
them back in output, or (if the model has tool access) uploading them to
an unrelated third party.

**Mitigation**:
- deploy.sh spawns all four subprocesses with an explicit env whitelist.
  We copy the actual list from
  `tools/eval/runner/subprocess.go:AlwaysAllowedEnvKeys` (line 223) +
  `AlwaysAllowedIfSetEnvKeys` (line 235) verbatim, so the two harnesses
  can't drift out of sync:
  - **Always** (emit even when absent from parent — empty value
    passes through, matching bash `emit_whitelisted_env` behaviour;
    empty PATH etc. are semantically distinct from unset only on a
    handful of legacy tools we don't care about here): `PATH`, `HOME`,
    `LANG`, `LC_ALL`, `TZ`, `USER`.
  - **If set**: `AGENTSERVER_ROOT`, `MODELSERVER_ROOT`, `APP_ROOT`,
    `MOCK_MODEL_URL`.
  - **Prefix passthrough**: any var whose name starts with `LOOM_` (this
    is the project-namespace test seam used by both harnesses; the
    prefix guard matches subprocess.go's `len(k) > len("LOOM_") &&
    strings.HasPrefix(k, "LOOM_")` — a bare `LOOM_=v` does NOT slip
    through).
  - **Everything else** — including `AWS_*`, `GITHUB_TOKEN`,
    `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `DOCKER_*` — dropped
    unconditionally in `--mode stub` and dropped by default in
    `--mode prod`.
- **Model-key passthrough** (prod only): the pre-existing `codex`/`claude`
  CLIs that the driver+slave will fork **do** need `OPENAI_API_KEY` /
  `ANTHROPIC_API_KEY` in most prod setups (see
  `tests/prod_test/E2E_RUNBOOK.md:80` "OPENAI_API_KEY env var set"). To
  surface this without a silent leak, `--mode prod` **only** propagates
  `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` when the operator passes
  `--allow-model-key-passthrough` (Linux) / `-AllowModelKeyPassthrough`
  (Windows); the script logs a WARN line to stderr each time it does so
  (`deploy.sh: passing OPENAI_API_KEY through to subprocess $pid ($role)`).
  Passing the flag in `--mode stub` is a preflight error (exit 2) — the
  stub has no model plane, so the flag would only serve to leak the key.
  The flag exists nowhere else in deploy.sh; grep asserts (T15c).
- In PowerShell, subprocess env is controlled via
  `[System.Diagnostics.ProcessStartInfo]::EnvironmentVariables.Clear()`
  followed by explicit `.Add(k, v)` for whitelisted keys — NOT via
  `Start-Process -Environment` which inherits by default.
- Test T15 exercises the whitelist with fake binaries (bash / pwsh
  scripts that dump their env to a file) — see §6.2 for the full
  positive+negative matrix.

### (h) `--dry-run` output secret redaction

**Threat**: an operator uses `--dry-run` in `--mode prod` to sanity-check a
plan and pastes the output into a PR or a Slack channel; if the plan carries
an API key in argv or env, that key is now published.

**Mitigation**:
- Before printing `planned_commands`, deploy.sh scrubs every argv token
  matching the pattern `--(api-key|token|secret|password|bearer)[= ]?(.*)`
  and replaces the value with `<REDACTED>`. Also scrubs any env value whose
  key ends with `_KEY`, `_TOKEN`, `_SECRET`, `_PASSWORD`, `_BEARER` — the
  printed `planned_env_whitelist` block only contains **key names**, never
  values.
- The redaction is done at print time, not by removing the entry from the
  argv, so the diff between "planned" and "actual" is small and reviewable.
- Test T8 asserts that a distinctive OAuth-token-shaped fixture string
  planted in the parent env under both `LOOM_API_KEY` and
  `ANTHROPIC_API_KEY` does not appear anywhere in stdout+stderr of
  `--dry-run`, including as a base64 encoding.

---

## 8. Acceptance

todo_list line 105 (verbatim):

> 在 fresh Linux/Windows 上 `bash deploy.sh --stub` / `pwsh deploy.ps1
> --stub` 拉起完整栈

Concretised here as:

1. `git diff origin/paper/v3-integration -- multi-agent/deploy/windows/slave/install.ps1`
   produces empty output.
2. `bash multi-agent/deploy/linux/deploy.sh --mode stub --dry-run` and
   `pwsh -NoProfile -Command "& { . multi-agent/deploy/windows/deploy.ps1 -Mode stub -DryRun }"`
   both exit 0 and print a JSON plan with `component_ports.observer`,
   `.driver`, `.slave`, `.agentserver_stub`.
3. On a fresh Linux host (§6.3 Linux leg), `bash deploy.sh --stub` runs
   to completion (exit 0), all four mode-appropriate readiness gates
   from §4.2 stub table pass, and topology JSON on last stdout line
   validates against `topology_schema.json`.
4. On a fresh Windows host (§6.3 Windows leg), `pwsh -NoProfile
   -ExecutionPolicy Bypass -File deploy.ps1 -Stub` runs to completion
   (exit 0) with the same four readiness gates (translated to
   `Test-NetConnection` / `Invoke-WebRequest`) and matching topology
   JSON. **This clause is mandatory for merge** — see §6.3.
5. All §6 automated tests pass on CI (both bats and Pester suites, the
   latter running on Linux via `pwsh -c Invoke-Pester`).
6. Codex Stage-3 code review returns `VERDICT: CLEAN` (only P2 nits allowed).

---

## 9. Open seams for future worktrees

- **Multi-slave (E2E_RUNBOOK-parity) bring-up**: deploy.sh currently
  spawns a single slave. WT-3-stub-fulltable / WT-3-prod-multidevice can
  extend with `--slaves N` + per-slave ports; the topology emit already
  keys `slave` as a single port field but is trivially widened to
  `slaves: [18093, 18094, ...]` at schema_version 2.
- **Remote hosts**: today WT-2 is per-host. A future D6c extension can
  wrap SSH orchestration by having deploy.sh emit its own plan on stdout
  and a remote runner replay it — the `--dry-run` shape is designed to be
  that IR.
- **Signed PowerShell releases**: §7(f) leaves a deliberate hook — a
  release-signing PR just flips T14's expected `.Status`.
- **`machine_topology` D1 writer contract**: today the eval-runner is the
  sole `internal/evalrun/writer.go` writer. If a later worktree wants to
  let deploy.sh insert directly, it will need to expose a Go seam
  (probably `cmd/topology-emit`) rather than embed SQLite driver deps in
  Bash. Out of scope here.
