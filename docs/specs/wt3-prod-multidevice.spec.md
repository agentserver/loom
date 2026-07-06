# WT-3-prod-multidevice — Spec

> Source: `/root/paper_writing/docs/final/todo_list.md` Phase 3 table row
> **WT-3-prod-multidevice** (line 130); 12 号 §C5 (line 90) + §J.2 (line 212);
> 11 号 §4 (E4 three-stage); prod runbook =
> `multi-agent/tests/prod_test/E2E_RUNBOOK.md`; §6.6 in
> `paper_outputs/evaluation_v3.md` (line 270).
> Branch: `paper/v3/p3-prod-multidevice`.
> Base: `origin/paper/v3-integration` HEAD `1748802`
> (Phase 2 全部合入 + WT-3-stub-fulltable + WT-3-mini-case merged).
>
> **Stage**: this is the **Stage 1 design spec** of the three-stage workflow
> (Spec → Plan → Code). Stage-3 review judges implementation; Stage-1 review
> judges design.

---

## 0. Scope — harness-only (read this first)

**本 worktree = harness + config + dry-run smoke，不接真设备、不做真 OAuth、
不创建/不使用/不销毁真云 droplet**；真设备真跑归后续 worktree
`paper/v3/p3-prod-multidevice-run`。

Reasons (why this is split into two worktrees):

- 真 OAuth token 一旦泄漏（commit 进 git、log 打印）无法回收，风险不可承受。
- 真云 droplet 有账单，需要用户明确授权 vendor + 预算。
- 跨物理机 tunnel 是"每次跑现场调"的动作，不适合无人值守。
- 需要一个"harness+config ready"检查点，让用户先审 config、再决定何时接硬件。

**Deliverable of this worktree = 可接真设备烟测的完整脚手架**：

- Per-device config schema (`multidevice/{laptop,headless,windows,cloud}.yaml.template`)
- 启动 wrapper（调 Phase 2 `deploy.sh --mode stub` 用于 smoke；`--mode prod`
  分支 gated on `ALLOW_PROD_DEPLOY=1` — this worktree never sets that env）
- `build_prod_vs_stub.py` — 对比脚本 CLI 完备（本 worktree 只跑 fake input smoke）
- `analysis_template.md` 模板（首段带 scope 声明；4 根因候选段）
- `teardown.sh` 关闭清单（双 gate；本 worktree 只跑 `--dry-run`）
- `E2E_RUNBOOK.md` Multi-device 追加段（描述性；不改既有 host-mode 段）
- `dry_run_all.sh` 本机模拟 4 端 daemon（loopback fake OAuth stub；不真设备）

`multi-agent/tests/eval/results/prod/dry_run_smoke/` **只含 dry-run smoke 输出**
（fake numbers or stub-loopback fake OAuth）；**不含**真设备数据。真设备真跑
产物（`run-*/`、`prod_vs_stub.csv` 无 `_sample` 后缀、`analysis.md` 无
`_template` 后缀）属后续 worktree — 本 worktree 越界即 P0.

---

## 1. Task boundary & file scope

### Files this worktree owns (all NEW unless noted)

| Path | Action |
|---|---|
| `multi-agent/tests/prod_test/multidevice/laptop.yaml.template` | **NEW**. Per-device config template for Linux laptop (driver + `.codex/config.toml` path (a) local proxy). |
| `multi-agent/tests/prod_test/multidevice/headless.yaml.template` | **NEW**. Linux headless server (observer + slave-A). |
| `multi-agent/tests/prod_test/multidevice/windows.yaml.template` | **NEW**. Windows desktop (slave-B PowerShell executor + Windows-only artifact generation). |
| `multi-agent/tests/prod_test/multidevice/cloud.yaml.template` | **NEW**. Cloud sandbox (slave-C; default `vendor: digitalocean`, enum ∈ `{digitalocean, e2b, vps}`). |
| `multi-agent/tests/prod_test/multidevice/topology.schema.json` | **NEW**. JSON schema for topology enum + per-device config validation. |
| `multi-agent/tests/prod_test/multidevice/topologies/4-device.yaml.sample` | **NEW**. Full 4-device sample. |
| `multi-agent/tests/prod_test/multidevice/topologies/3-device-nowin.yaml.sample` | **NEW**. 3-device (no Windows). |
| `multi-agent/tests/prod_test/multidevice/topologies/3-device-nocloud.yaml.sample` | **NEW**. 3-device (no cloud). |
| `multi-agent/tests/prod_test/multidevice/topologies/2-device-min.yaml.sample` | **NEW**. Minimum 2-device (Linux laptop + headless server). |
| `multi-agent/tests/prod_test/multidevice/wrappers/laptop_up.sh` | **NEW**. Startup wrapper (calls Phase 2 `deploy.sh --mode stub` for smoke; `--mode prod` branch gated on `ALLOW_PROD_DEPLOY=1`). |
| `multi-agent/tests/prod_test/multidevice/wrappers/headless_up.sh` | **NEW**. Same shape as laptop_up.sh. |
| `multi-agent/tests/prod_test/multidevice/wrappers/windows_up.ps1` | **NEW**. Windows PowerShell wrapper (`Set-ExecutionPolicy -Scope Process Bypass` only; no `LocalMachine`/`CurrentUser`). |
| `multi-agent/tests/prod_test/multidevice/wrappers/cloud_up.sh` | **NEW**. Cloud sandbox wrapper (calls `deploy.sh --mode stub`; provisions vendor via `--dry-run` printing only). |
| `multi-agent/tests/prod_test/multidevice/dry_run_all.sh` | **NEW**. Local-loopback fake-daemon simulator (4-device smoke; no real OAuth). |
| `multi-agent/tests/prod_test/multidevice/build_prod_vs_stub.py` | **NEW**. Prod-vs-stub comparison CLI; reads stub CSV + prod dir, emits `prod_vs_stub.csv` with `verdict` column + stub SHA meta. |
| `multi-agent/tests/prod_test/multidevice/teardown.sh` | **NEW**. Print-only shutdown checklist (4 items); `--execute` requires `ALLOW_TEARDOWN=1` env. |
| `multi-agent/tests/prod_test/multidevice/lint.sh` | **NEW**. Static-check driver (secret-scan, ExecutionPolicy scope, bind endpoint parser, .gitignore invariants). |
| `multi-agent/tests/prod_test/multidevice/README.md` | **NEW**. Per-device README + scope statement + points to `paper/v3/p3-prod-multidevice-run` for real-device work. |
| `multi-agent/tests/prod_test/multidevice/tests/` | **NEW**. Pytest module (`test_multidevice.py` etc.) covering all security items (a)–(k). |
| `multi-agent/tests/eval/results/prod/dry_run_smoke/analysis_template.md` | **NEW**. Analysis template with scope-declaration first paragraph + 4 root-cause candidate sections. |
| `multi-agent/tests/eval/results/prod/dry_run_smoke/prod_vs_stub_sample.csv` | **NEW**. Fake-input output of `build_prod_vs_stub.py --sample-mode`. |
| `multi-agent/tests/eval/results/prod/dry_run_smoke/topology_used.json` | **NEW**. hostname → sha256[:8] redaction sample. |
| `multi-agent/tests/eval/results/prod/dry_run_smoke/.gitkeep` | **NEW**. Directory marker. |
| `multi-agent/tests/prod_test/E2E_RUNBOOK.md` | **APPEND-ONLY**. Add `## Multi-device deployment (§C5 smoke)` section at the end; do NOT touch existing host-mode content. |
| `multi-agent/.gitignore` | **EDIT**. Add `!/tests/prod_test/multidevice/` + `!/tests/prod_test/multidevice/**` whitelist, then re-ignore `/tests/prod_test/multidevice/tokens/`, `/tests/prod_test/multidevice/**/*.token`, `/tests/prod_test/multidevice/**/*.pem`, `/tests/prod_test/multidevice/**/tokens.yaml`. |

### Files this worktree MUST NOT modify

- `multi-agent/deploy/**` — Phase 0/2 territory. Wrappers only **call** these
  scripts as subprocesses; they never edit them. Stage-3 review gates on
  `git diff origin/paper/v3-integration -- multi-agent/deploy/` being empty.
- `multi-agent/internal/**` — Phase 0/1/2 territory (`secretscrub` is
  consumed as a library only).
- `multi-agent/tools/eval/**` — Phase 0/1/2 territory.
- `multi-agent/tests/prod_test/E2E_RUNBOOK.md` existing content
  (host-mode section) — only allowed change is appending the Multi-device
  section at EOF.
- `multi-agent/tests/prod_test/{README.md,run_e2e.sh,driver_mcp_e2e.py}`
  — pre-existing; do not touch.
- `multi-agent/.gitignore` outside the multidevice whitelist / re-ignore
  block (see §7(a)); every other existing entry stays byte-for-byte.

### Non-goals (explicitly out of scope; belong to `p3-prod-multidevice-run`)

- Real physical devices (Linux laptop / headless server / Windows desktop /
  cloud droplet). All smoke is loopback fake-daemon.
- Real OAuth device flow. Placeholders only (`<OAUTH_TOKEN_HERE_DO_NOT_COMMIT>`).
- Real cloud droplet creation (`doctl compute droplet create`) or destruction
  (`teardown.sh --execute` with `ALLOW_TEARDOWN=1`).
- Real workspace_id allocation on `agent.cs.ac.cn`.
- Producing `tests/eval/results/prod/run-*/`, `prod_vs_stub.csv` (without
  `_sample` suffix), `analysis.md` (without `_template` suffix) — those are
  real-device products.
- Extending the workload set beyond `cross-device-code-mod` +
  `windows-only-artifact` — §6.6 external validity is a two-workload
  narrow-scope claim; the follow-up worktree also must not expand to five
  workloads (that would collide with §6.3 唯一数据源 = `p3-stub-fulltable-run`).

---

## 2. Target 4-device topology (config schema supports 4 / 3 / 2 devices)

### 2.1 Devices

| # | Device | Role | Notes |
|---|---|---|---|
| 1 | Linux laptop | driver + `.codex/config.toml` (path (a) local proxy) | User's daily machine; runs `codex` CLI. |
| 2 | Linux headless server | observer + slave-A | Same as prod runbook host-mode observer + slave-A. |
| 3 | Windows desktop | slave-B (PowerShell executor) | Windows-only artifact generation (e.g. `.ps1`/`.msi`/`.exe`). |
| 4 | Cloud sandbox (default = DigitalOcean single droplet) | slave-C | Cross-internet tunnel validation; vendor ∈ `{digitalocean, e2b, vps}`. |

**Rationale for the 4 devices** (per todo_list §C5 + 12号 §J.2):

- Device 1 vs Device 2 = separates driver from observer (single-host runbook
  collapses these) — surfaces cross-machine `daemon-link` behavior.
- Device 3 = the OS-heterogeneity axis (WT-0-windows-slave `install.ps1`
  landed but has never been exercised across a real cross-OS tunnel).
- Device 4 = the "not-my-network" axis (cloud droplet forces public tunnel,
  reveals `agentserver` real tunnel round-trip cost missing from stub).

### 2.2 Topology enum (fallback / degradation paths)

Config schema `topology_enum` ∈:

| Enum | Devices | Trigger |
|---|---|---|
| `4-device` | 1 + 2 + 3 + 4 | Full topology. |
| `3-device-nowin` | 1 + 2 + 4 (Windows dropped; a second Linux takes slave-B role labeled "降级 3 设备-noWin") | Windows machine unavailable. |
| `3-device-nocloud` | 1 + 2 + 3 (cloud dropped; tailscale/wireguard 家用 Linux 顶 as "降级 3 设备-noCloud 伪云") | Cloud vendor unavailable. |
| `2-device-min` | 1 + 2 only (Linux laptop + headless server) — todo_list §"如时间紧" 允许 | Both Windows and cloud unavailable; the "最低 2 设备" fallback. |

`analysis_template.md` MUST ship (unconditionally, so real-run worktrees
only fill numbers) a dedicated `## 降级 2 设备 影响段` section flagging:
no cross-OS coverage (Windows-only-artifact workload becomes stub-only
in `2-device-min`), and no cross-internet tunnel coverage (cloud sandbox
effect is unmeasured). The section is checked by
`test_analysis_template_2device_impact_section`.

### 2.3 Sample topology files

Each of the four `topologies/*.yaml.sample` files:

- Sets `topology_enum: <one of the four values>`.
- Includes the per-device role assignment table.
- Includes an ASCII topology diagram + a port/tunnel table (mirror of §7
  in the RUNBOOK append).
- Includes a `notes:` field describing which coverage axes are lost in
  the degraded case (empty for `4-device`).

---

## 3. Workload coverage (harness layer)

### 3.1 In-scope workloads

**Only two workloads** get config + startup wrapper wired in this worktree,
matching §6.6 exactly. Excerpt from `paper_outputs/evaluation_v3.md` §6.6
(quoted verbatim for spec-review anchoring):

> 从 5 workload 里挑 `cross-device-code-mod` + `windows-only-artifact`
> （覆盖"跨机文件同步"与"Windows-only artifact"两个 stub 最容易失真的维度），
> Full Loom + `manual_ssh` baseline 各 N = 3 rep。**不重跑 5 workload × 12
> config 主表**——那会与 §6.3 唯一数据源约定冲突。

**Why only these two**:

- `cross-device-code-mod` = the "cross-machine file sync" axis stub
  abstracts away (stub runs both sides on the same host).
- `windows-only-artifact` = the "OS-heterogeneous producer/consumer" axis
  stub abstracts by pretending Windows-only tools exist in the Linux
  fake-daemon.

### 3.2 N=3 repetition convention

`N=3` per workload is written into the config schema (`per_workload_reps: 3`)
so the real-run worktree consumes it directly. This worktree does not
sample — dry-run smoke emits fake `run_count: 3` rows to prove the schema
plumbing.

### 3.3 Hard cap: never expand to 5 workloads

Config schema MUST reject `workloads: [...]` lists whose length > 2, OR
which name any workload not in `{cross-device-code-mod, windows-only-artifact}`.
This is a **hard cap** — even the follow-up real-run worktree must not
extend it (would collide with §6.3 唯一数据源). Test
`test_workload_hard_cap` enforces this in this worktree so the guardrail
is baked into config validation, not just documentation.

---

## 4. RUNBOOK append (Multi-device section)

### 4.1 Location & rule

**Append-only** at the end of `multi-agent/tests/prod_test/E2E_RUNBOOK.md`.
Add a single new H2 section:

```
## Multi-device deployment (§C5 smoke)
```

The existing host-mode Topology / Prereqs / step-by-step / commander k8s
sections stay byte-for-byte identical. Stage-3 review gates on this via
`diff <before-append> <after-append>` up to the append point.

### 4.2 Content

The Multi-device section must include, in order:

1. **Scope banner** (matches §0 of this spec): "本节描述 4 设备真实部署路径。
   本 worktree (paper/v3/p3-prod-multidevice) 仅落 harness+config；真设备
   烟测由 paper/v3/p3-prod-multidevice-run 触发。"
2. **Cross-reference**: link to `tests/prod_test/multidevice/README.md`
   for per-device wrapper usage.
3. **Multi-device topology diagram** (ASCII; distinct from host-mode) —
   4 boxes + inter-device tunnels labeled with port ranges.
4. **Port / tunnel table** — for each device, which local ports bind, and
   which cross-device tunnels are established (agentserver-signed).
5. **OAuth device flow steps** (**documentation only** — this worktree
   does not execute) — one section per device, 5 numbered steps:
   `codex auth login` on device → visit `https://...` → paste code →
   token stored in `<user-config-dir>/codex/tokens.yaml` (NEVER under
   `multidevice/tokens/` — the placeholder path in this worktree is
   gitignored; real tokens live outside repo entirely).
6. **Handoff pointer**: "真设备真跑步骤由 `paper/v3/p3-prod-multidevice-run`
   补齐；本 section 是那份 runbook 的 config anchor。"

---

## 5. `build_prod_vs_stub.py` CLI

### 5.1 Shape

```
build_prod_vs_stub.py \
  --prod-dir <path-to-prod-run-dir> \
  --stub-table <path-to-stub-csv> \
  --out <path-to-output-csv> \
  [--sample-mode]           # this worktree: reads fake input, emits _sample
```

### 5.2 Input contracts

- `--stub-table` MUST be one of:
  - `multi-agent/tests/eval/results/smoke/paper/table2_sample.csv` (this
    worktree — sample stub table produced by WT-3-stub-fulltable smoke)
  - `multi-agent/tests/eval/results/paper/table2.csv` (real stub table
    from `p3-stub-fulltable-run` — for the follow-up real-run worktree)
- Any other stub-table path → exit 2 with "stub source path not in
  allow-list". Prevents `prod_vs_stub.csv` from silently comparing against
  a stale side-loaded CSV. See §7(f).
- `--prod-dir` MUST contain per-workload sub-directories with metric JSONs
  in the schema WT-3-stub-fulltable's runner already writes.

### 5.3 Output CSV columns

```
workload,metric,stub_value,prod_value,abs_diff,rel_diff_pct,verdict
```

Plus a meta header (comment lines prefixed `#`) containing:

- `# stub_source_path=<path>` — literal path used
- `# stub_source_sha256=<64-hex>` — SHA256 of the stub CSV file bytes
- `# prod_dir=<path>` — prod run directory
- `# generated_at=<value from --generated-at flag>` — passed in by the
  caller; the script itself does not call `datetime.now()` in `--sample-mode`
  (test determinism).
- `# script_version=v1` — hard-coded, bumped on schema changes.

### 5.4 `verdict` values

- `consistent` — `abs_diff` within tolerance AND `rel_diff_pct` within
  tolerance for the metric.
- `divergent-explained` — outside tolerance BUT annotated in
  `analysis.md` (the real-run worktree cross-refs verdict rows to
  `analysis.md` sections).
- `divergent-unexplained` — outside tolerance AND no annotation. §6.6
  hard gate: `divergent-unexplained` count must be 0 in the real run.

Tolerance per metric (matches §6.6):

| Metric | abs_diff threshold | rel_diff_pct threshold |
|---|---|---|
| `TaskSuccessRate` | 5.0 (pp) | — |
| `WrongContextFailureRate` | 5.0 (pp) | — |
| `RoutingAccuracy` | 5.0 (pp) | — |
| `TimeToCompletion` | — | 100.0 (%) |
| Any other metric | rejected — see §3.3 hard cap | — |

> **Note on `ModelProxyOverhead`**: `todo_list.md:130` (WT-3-prod-multidevice
> row) also mentions `ModelProxyOverhead` as a divergence-tolerated metric.
> `evaluation_v3.md` §6.6 (line 285) supersedes that row and narrows the
> core-metric set to the four above (`TaskSuccessRate`,
> `WrongContextFailureRate`, `RoutingAccuracy`, `TimeToCompletion`).
> `ModelProxyOverhead` is therefore **explicitly deferred** in this
> worktree's harness — if the real-run worktree needs to compare it, it
> must first amend §6.6.

### 5.5 Sample-mode behavior (this worktree)

`--sample-mode` reads a fake prod dir bundled in
`tests/prod_test/multidevice/fixtures/fake_prod/` (2 workloads × 4
metrics × N=3 fake rows) and the stub sample CSV, writing
`prod_vs_stub_sample.csv` (filename hard-suffixed `_sample`). Exists so
the harness can verify column structure + verdict logic + stub-SHA meta
recording without any real data.

---

## 6. `analysis_template.md`

### 6.1 First paragraph (scope declaration)

**MUST literally contain the following four plain-text lines** (no markdown
emphasis; grep -qF friendly) so the scope declaration is machine-checkable:

```
本文件为 paper_outputs/evaluation_v3.md §6.6 外部效度提供依据。
不产 §6.3 主实验数字。
不产 §6.5 消融数字。
核心 metric 仅抽 TaskSuccessRate / TimeToCompletion / WrongContextFailureRate / RoutingAccuracy 四个上表；divergent-unexplained 行数 = 0 是通过外部效度的硬门槛。
```

Prevents the real-run worktree from re-purposing this file into a §6.3
main-table dumping ground.

### 6.2 Required strong-divergence sections

Empty `##` headings for each "strong divergence" pattern the real-run
worktree may need to fill:

```
## TaskSuccessRate abs_diff > 5pp: <workload>
## TimeToCompletion rel_diff > 100%: <workload>
## WrongContextFailureRate abs_diff > 5pp: <workload>
## RoutingAccuracy abs_diff > 5pp: <workload>
```

### 6.3 Root-cause candidates (required — all 4 must appear)

Each strong-divergence section is followed by a "根因候选" checklist. The
canonical four root-cause candidates (matching §6.6 wording) MUST all
appear at least once in the file (grep -qF checked):

- `OAuth round-trip`
- `真 tunnel`
- `跨机 RTT`
- `云 sandbox 冷启动`

### 6.4 "This is a template, no real data" disclaimer

Last line: "本文件是模板（`analysis_template.md`），未含真数据；真数据由
`p3-prod-multidevice-run` 产 `analysis.md`。"

---

## 7. `teardown.sh`

### 7.1 Four operations (all print-only in this worktree)

1. **OAuth token local deletion** — `rm -f "$LOOM_HOME/tokens/*.token"`
   (per-device, one line per device role)
2. **agentserver workspace_id revoke** — `curl -X DELETE
   https://agent.cs.ac.cn/api/v1/workspaces/<workspace_id> -H
   "Authorization: Bearer <OAUTH_TOKEN_HERE_DO_NOT_COMMIT>"` (**print
   only** — never executed here)
3. **Cloud droplet destroy** — `doctl compute droplet delete <droplet_id>
   --force` (vendor-conditional: `doctl` for digitalocean, `e2b sandbox
   delete` for e2b, `ssh <vps> systemctl stop loom-slave` for vps) —
   **print only**
4. **Windows executor uninstall** — `pwsh -Command "&
   $env:LOOM_HOME/uninstall.ps1 -Scope Process"` — **print only**

### 7.2 Dual-gate

- `--dry-run` (default) — prints planned commands, exits 0.
- `--execute` — checks `[[ "$ALLOW_TEARDOWN" == "1" ]]`; if unset, falls
  back to `--dry-run` behavior + emits stderr "ALLOW_TEARDOWN not set;
  refusing to execute; running dry-run instead". Only when the env is
  set does it actually run each command.

This worktree's tests only exercise `--dry-run` (and `--execute` without
env → falls back to dry-run); the actual `--execute` with env is the
real-run worktree's responsibility.

---

## 8. Acceptance (harness layer)

- [ ] `multidevice/{laptop,headless,windows,cloud}.yaml.template` present;
      each conforms to `topology.schema.json`.
- [ ] `topology.schema.json` accepts all 4 topology enum values; each
      topology has a `topologies/<enum>.yaml.sample` file that
      round-trip-validates.
- [ ] `dry_run_all.sh` runs on this host: emulates 4 daemons on
      loopback with fake OAuth stub (no real device / no real OAuth); writes
      `dry_run_smoke/{prod_vs_stub_sample.csv,topology_used.json}`.
- [ ] `build_prod_vs_stub.py --sample-mode` produces valid CSV: correct
      column header, at least one row per metric, stub SHA present in meta
      comment lines.
- [ ] `analysis_template.md` has scope declaration first paragraph, 4
      metric sections, 4 root-cause candidates, disclaimer.
- [ ] `teardown.sh --dry-run` prints ≥4 planned commands (OAuth rm, curl
      revoke, doctl destroy, pwsh uninstall).
- [ ] `E2E_RUNBOOK.md` Multi-device section appended; existing content
      byte-for-byte identical up to append point (verified via diff).
- [ ] `.gitignore` whitelists `multidevice/` while re-ignoring `tokens/`,
      `*.token`, `*.pem`, `tokens.yaml`.
- [ ] `pytest tests/prod_test/multidevice/tests/` all pass, covering
      every security item (a)–(k) in §9.

### Not accepted here (belongs to `p3-prod-multidevice-run`)

- Any real device / real OAuth / real tunnel / real cloud droplet action.
- `tests/eval/results/prod/run-*/`, `prod_vs_stub.csv` (no `_sample`
  suffix), `analysis.md` (no `_template` suffix).
- Actual `divergent-unexplained = 0` gate result.

---

## 9. Security

### (a) OAuth token never on disk / never in git

- Every file under `multi-agent/tests/prod_test/multidevice/` must use
  the literal placeholder `<OAUTH_TOKEN_HERE_DO_NOT_COMMIT>` where an
  OAuth token would otherwise appear. Real token literals (`sk-…`,
  `ghp_…`, `AKIA…`, `xoxb…`, `Bearer <hex>`, `refresh_token …`) are
  banned.
- **`.gitignore` amendment** — the current
  `multi-agent/.gitignore` block `/tests/prod_test/*` +
  four negation entries (README, RUNBOOK, run_e2e.sh, driver_mcp_e2e.py)
  does NOT whitelist `multidevice/`. This worktree adds:
  ```
  !/tests/prod_test/multidevice/
  !/tests/prod_test/multidevice/**
  /tests/prod_test/multidevice/tokens/
  /tests/prod_test/multidevice/**/*.token
  /tests/prod_test/multidevice/**/*.pem
  /tests/prod_test/multidevice/**/tokens.yaml
  ```
  (the ignore lines come **after** the whitelist so re-ignore takes
  precedence for token-bearing paths).
- **Secret-scan** command (used in `test_multidevice_dir_no_secrets`):
  ```
  grep -REn 'sk-|ghp_|AKIA|xoxb|Bearer[[:space:]]+[A-Za-z0-9]|refresh_token' multidevice/
  ```
  This is ERE (`grep -E`); alternation uses `|`, not `\|`
  (`\|` in ERE is a literal). The negative-scan test injects a fake
  `sk-abc123` into a scratch file and asserts the scanner catches it.
- **git check-ignore assertions** — the test suite asserts:
  - `git check-ignore multi-agent/tests/prod_test/multidevice/laptop.yaml.template`
    returns non-zero (tracked, not ignored).
  - `git check-ignore multi-agent/tests/prod_test/multidevice/tokens/anything.yaml`
    returns zero (ignored).
  - `git check-ignore multi-agent/tests/prod_test/multidevice/x.token`
    returns zero (ignored).

### (b) Cross-machine tunnel loopback convergence

- Every startup wrapper's local bind endpoint must parse to one of
  `127.0.0.1`, `::1`, or `localhost`. Wildcard equivalents forbidden:
  `0.0.0.0:<port>`, bare `:<port>`, `[::]:<port>`, external IPs.
- Enforcement is via a **parser** (not a naive substring grep) in
  `tests/test_bind_endpoints.py`: parse each `listen:` / `bind:` /
  `endpoint:` value with `urllib.parse` (`http://<value>` normalized)
  and reject the disallowed forms. Test includes explicit positive
  cases (valid) and negative cases (all four disallowed forms) with
  expected reject reason strings.
- Cross-device tunnel URLs are returned by `agentserver`; they are
  never hard-coded in template YAMLs.

### (c) Windows slave execution policy

- `wrappers/windows_up.ps1` uses **only** `Set-ExecutionPolicy -Scope
  Process Bypass`. No `LocalMachine` / `CurrentUser` scope changes.
- Test `test_windows_execpolicy_scope_process` greps the ps1 files and
  asserts:
  - `Set-ExecutionPolicy` appears at least once.
  - Every occurrence's `-Scope` argument literally equals `Process`.
  - No occurrence of `-Scope LocalMachine` or `-Scope CurrentUser`.

### (d) Cloud sandbox upload secret scrub

- The cloud-upload code path in `cloud_up.sh` (and its embedded
  Python helper for reading the yaml) must call
  `internal/secretscrub.Sanitize` (via a Go shim compiled at repo
  root) OR use a direct Python port of the same regex set — the port
  lives in `tests/prod_test/multidevice/secretscrub_python.py` (**not**
  a new Go dependency).
- Test `test_cloud_upload_secretscrub_wired` injects a fake `sk-abc123`
  into a scratch YAML file, runs the cloud-upload dry-run against
  it, and asserts the scanned output is redacted (or the upload
  aborts with a "secret detected" error).

### (e) workspace_id lifecycle documented

- `README.md` includes a `## workspace_id lifecycle` section stating:
  "one workspace per paper experiment session; destroyed via
  `teardown.sh --execute` after the run." Includes the 4-item shutdown
  checklist.
- No workspace is created in this worktree.

### (f) `prod-vs-stub` data source pinning

- `build_prod_vs_stub.py` asserts `--stub-table` path is in the
  allow-list from §5.2. On accept, records SHA256 of the file bytes in
  the output CSV meta header.
- Test `test_stub_source_pinned` verifies the SHA in the output
  matches `sha256sum` of the input file.

### (g) Degradation path audit — hostname redaction

- `dry_run_all.sh` writes `dry_run_smoke/topology_used.json` where
  each device's `hostname` field is `sha256(hostname)[:8]` (not
  plaintext). Prevents leaking real machine identities in the tracked
  smoke output.
- Test `test_topology_used_hostname_redacted` injects a known
  `HOSTNAME=alice-laptop` env, runs the smoke, checks the JSON hash
  matches `sha256("alice-laptop")[:8]`.

### (h) Cloud vendor single key

- `cloud.yaml.template` has a single line `vendor: <one of
  digitalocean|e2b|vps>`. Schema validates enum. README + spec say
  "change one line to switch vendor."

### (i) `analysis_template.md` required sections

- All 4 root-cause candidates (OAuth round-trip / 真 tunnel / 跨机
  RTT / 云 sandbox 冷启动) present at least once (grep -qF).
- Scope declaration paragraph present (grep -qF for each of the 4
  literal lines from §6.1).
- 4 metric-header sections present.

### (j) `teardown.sh` dual gate

- `--execute` without `ALLOW_TEARDOWN=1` env → falls back to
  `--dry-run`, prints warning on stderr.
- `--execute` with env → runs commands.
- This worktree's test only exercises the without-env case (asserts
  fall-back).

### (k) No-real-prod-deploy hard cap

- Every wrapper (`{laptop,headless,windows,cloud}_up.*`) that would
  otherwise call Phase 2 `deploy.sh --mode prod` or `deploy.ps1
  -Mode prod` MUST wrap it in:
  ```bash
  if [[ "${ALLOW_PROD_DEPLOY:-0}" != "1" ]]; then
      echo "ALLOW_PROD_DEPLOY not set; using --mode stub for smoke" >&2
      MODE=stub
  fi
  ```
- Test `test_no_prod_deploy_without_env` shell-parses each wrapper and
  asserts every `--mode prod` / `-Mode prod` invocation is inside such
  a gate.
- `dry_run_all.sh` never sets `ALLOW_PROD_DEPLOY=1` — smoke is 100%
  stub loopback.

---

## 10. Handoff to `paper/v3/p3-prod-multidevice-run`

- `README.md` explicitly names the follow-up worktree.
- RUNBOOK Multi-device section ends with a handoff pointer (§4.2 item 6).
- `analysis_template.md` disclaimer names the follow-up worktree.
- No real-device product filepath (`run-*/`, `prod_vs_stub.csv` w/o
  `_sample`, `analysis.md` w/o `_template`) is created here.
