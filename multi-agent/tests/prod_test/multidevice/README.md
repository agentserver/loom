# WT-3-prod-multidevice — Multi-device deployment harness

**Scope (read first)**: this directory is the **harness + config +
dry-run smoke** for §C5 multi-device prod deployment. It does NOT
touch real physical devices, does NOT execute real OAuth device flow,
and does NOT create / use / destroy real cloud droplets.

**Real-device work is deferred to the follow-up worktree**
`paper/v3/p3-prod-multidevice-run`. That worktree consumes the
templates + wrappers + teardown script here, adds the real-device
provisioning steps, and produces `tests/eval/results/prod/run-*/`
with the actual `prod_vs_stub.csv` + `analysis.md`.

---

## Contents

| Path | Purpose |
|---|---|
| `topology.schema.json` | JSON schema (draft-07) for per-device configs + topology samples. |
| `laptop.yaml.template` | Linux laptop (driver) config template. |
| `headless.yaml.template` | Linux headless server (observer + slave-A). |
| `windows.yaml.template` | Windows desktop (slave-B). |
| `cloud.yaml.template` | Cloud sandbox (slave-C); single `vendor:` line switch. |
| `topologies/*.yaml.sample` | Sample topology docs for the 4 enum values. |
| `wrappers/{laptop,headless,cloud}_up.sh` | Bash startup wrappers (call Phase 2 `deploy.sh --mode stub` for smoke; `--mode prod` gated on `ALLOW_PROD_DEPLOY=1`). |
| `wrappers/windows_up.ps1` | PowerShell wrapper (same shape; `Set-ExecutionPolicy -Scope Process Bypass` only). |
| `wrappers/cloud_upload.py` | Pre-upload secret scanner (spec §7(d)). |
| `secretscrub_python.py` | Port of `internal/secretscrub` regex list. |
| `dry_run_all.sh` | Loopback fake-daemon 4-device smoke driver. |
| `build_prod_vs_stub.py` | Prod-vs-stub comparison CLI. |
| `teardown.sh` | 4-item shutdown checklist; dual-gate `--dry-run` / `--execute` (with `ALLOW_TEARDOWN=1` env). |
| `lint.sh` | Static-check runner (secret-scan, ExecutionPolicy scope, bind-endpoint substrings). |
| `fixtures/fake_prod/` | Sample metric JSONs (2 workloads × N=3). |
| `fixtures/fake_stub_table.csv` | Deterministic stub CSV used by `build_prod_vs_stub.py --sample-mode`. |
| `tests/` | Pytest module covering all security items (a)–(k). |

---

## Usage — harness smoke (this worktree)

```bash
# 1. Static checks
bash tests/prod_test/multidevice/lint.sh

# 2. Schema + wrapper unit tests
python -m pytest tests/prod_test/multidevice/tests/

# 3. Loopback fake-daemon smoke (produces dry_run_smoke/*)
bash tests/prod_test/multidevice/dry_run_all.sh

# 4. Print-only teardown checklist
bash tests/prod_test/multidevice/teardown.sh --dry-run
```

Nothing above ever contacts `agent.cs.ac.cn`, spawns a real Windows
process, or provisions any cloud droplet.

## Usage — real-device smoke (paper/v3/p3-prod-multidevice-run)

Not this worktree. See the follow-up worktree's runbook once created.
The E2E_RUNBOOK.md `## Multi-device deployment (§C5 smoke)` section
provides the concrete step-by-step device flow that worktree will
implement.

---

## workspace_id lifecycle

**One workspace per paper experiment session.** Each session:

1. Creates a fresh `workspace_id` on `agent.cs.ac.cn` (see §J.2 of
   `12_loom_development_tasks_for_v3.md`).
2. Uses it for the duration of the multi-device run (all 4 agents
   share it — a wrong-workspace slave shows up in the agentserver's
   list but is invisible to the driver's `list_agents`).
3. Destroys it after the run via `teardown.sh --execute` with
   `ALLOW_TEARDOWN=1` env set — 4 shutdown checklist items:
   1. Remove local OAuth token files.
   2. Revoke the `workspace_id` on the agentserver.
   3. Destroy the cloud droplet.
   4. Uninstall the Windows executor.

**This worktree never creates a workspace** — the lifecycle only runs
in `paper/v3/p3-prod-multidevice-run`. Documented here so operators
of the real-run worktree know the shutdown expectations up front.

---

## Tunnel coordination

Cross-device traffic goes through agentserver-signed tunnels; the
tunnel URLs are **returned by the agentserver at workspace creation
time** and are never hardcoded in any file under this directory.
Local bind endpoints are always loopback (`127.0.0.1`, `::1`,
`localhost`) — no wildcard binds (`0.0.0.0:`, bare `:PORT`,
`[::]:PORT`, external IPs) are allowed (spec §7(b);
`test_bind_endpoints.py` enforces).

See `E2E_RUNBOOK.md` `## Multi-device deployment (§C5 smoke)` for the
full port/tunnel table and the OAuth device flow steps (documentation
only in this worktree).

---

## Handoff

- **Real physical devices**, **real OAuth**, **real cloud droplets**,
  **real workspace_id** → `paper/v3/p3-prod-multidevice-run`.
- **Real-device product filepaths** (`tests/eval/results/prod/run-*`,
  `prod_vs_stub.csv`, `analysis.md` without `_template` suffix) →
  `paper/v3/p3-prod-multidevice-run`.
- **Workload set expansion beyond `cross-device-code-mod` +
  `windows-only-artifact`** → forbidden even in the follow-up worktree
  (blocked by `topology.schema.json` hard cap; would collide with §6.3
  唯一数据源 = `p3-stub-fulltable-run`).
