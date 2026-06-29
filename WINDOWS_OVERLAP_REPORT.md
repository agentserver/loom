# WT-0 Windows Slave — Upstream Overlap Report

**Worktree:** `paper/v3/p0-windows-slave`
**Date:** 2026-06-29
**Decision:** Path **(A) — upstream already covers everything**, plus a small
incremental gap (compose cloud sandbox slave node) added here.

## Audit method

For every commit on `origin/windows-driver-slave-support` not on master,
look up the same commit message on master. Result:

```
git log master..origin/windows-driver-slave-support  →  32 commits
mapped to master                                    →  32/32 ✓
```

All 32 upstream commits have already been merged into master under the
same commit messages (different SHAs because the branch was rebased on
merge). The merge-base between master and the upstream branch is
`3e73ff5` (multi-agent's tip from the day the windows feature branch
was opened); both sides have applied the same windows work since then.

Sample of the mapping:

| Upstream SHA | Master SHA | Subject |
|---|---|---|
| `3e136a3` | `7a383bc` | feat: add cross-platform runtime helpers |
| `4844c1c` | `35da136` | feat: make humanloop ipc cross-platform |
| `d7d36a1` | `c55f03e` | feat: add powershell task executor |
| `e894d76` | `2c79ec3` | feat: publish platform command interfaces |
| `d3c1841` | `452d4e7` | feat: advertise slave command interfaces |
| `de1efbe` | `badc13d` | feat: add windows deployment assets |
| `07b9203` | `e29f1d3` | fix: harden windows review gaps |
| `efa3127` | `0fc857a` | fix: ignore malformed humanloop preauth frames |
| _(plus 24 more all matched)_ | | |

No upstream commit is missing from master. Cherry-picking would create
duplicate-content commits, so the right move is to **only add what is
still missing on master**.

## Acceptance checklist vs master HEAD

The todo_list / plan `2026-06-08-windows-driver-slave-support.md` calls
for the following deliverables. Each is verified on master before any
edit:

| Deliverable | Status on master | Evidence |
|---|---|---|
| `internal/platform/{filelock,process,signals}_{unix,windows}.go` build-tagged helpers | ✅ already present | `multi-agent/internal/platform/{filelock,process,signals}_{unix,windows}.go` exist; tests in `filelock_test.go`, `filelock_unix_test.go`, `process_test.go` |
| `internal/humanloop/ipc_windows.go` loopback TCP listener | ✅ already present | `multi-agent/internal/humanloop/ipc.go` + `ipc_unix.go` + `ipc_windows.go`; `ipc_test.go` covers both transports |
| `internal/executor/powershell*.go` + `command_interfaces` probe | ✅ already present | `multi-agent/internal/executor/powershell.go` + `powershell_test.go`; `internal/commandiface/{detect,detect_unix,detect_windows,interfaces}.go` |
| `internal/contract/types.go` `platform` / `command_interfaces` fields | ✅ already present | `contract/types.go:95-96`, `contract/snapshot.go:26-27,40-41`; tests in `contract/contract_test.go:234` |
| Capability card emits `platform=windows` + `command_interfaces=[powershell]` and driver `list_agents` sees it | ✅ already present | `cmd/slave-agent/capabilities.go` + `capabilities_test.go:23-63` (TestNormalizeDiscoveryForRuntimeWindowsRemovesUnavailableBashAndKeepsPowerShell), `internal/driver/agent_card.go` + `agent_card_test.go:38` |
| `deploy/windows/slave/install.ps1` PowerShell installer | ✅ already present | `deploy/windows/slave/{install.ps1,slave-agent-service.ps1,config.yaml.template,README.md}` |
| `dev/compose.distributed.yaml` includes a cloud sandbox slave node | ❌ **gap on master** | only `slave-a` / `slave-b` (both generic Linux) — no cloud-sandbox tier |

## Increment added in this worktree

The compose file already exercises two Linux slaves (`slave-a`,
`slave-b`) plus driver/master/observer/agentserver/postgres but does
not exercise the **cloud sandbox** tier of the §C1 four-node fleet.
The real Windows node cannot ship in a Linux compose file (handled
separately by `deploy/windows/slave/install.ps1`), but the cloud
sandbox tier can — it is just another slave-agent container with a
different config and tag set. WT-0 only ships the container
placeholder; tier-aware routing (e.g. a driver matcher keyed on a
dedicated `sandbox` skill) is **out of scope** and belongs to Phase
1/2.

What this compose change does *not* prove: in Linux compose,
`slave-cloud` and `slave-b` advertise the same `[chat, mcp]` skill
set and run on the same Linux/bash runtime, so capability discovery
sees two functionally similar nodes that differ only by the
`resources.tags` `[cloud, sandbox, ephemeral]` vs the laptop tags.
That is enough to exercise the tag-keyed half of
`internal/driver/capability_tools.go` (`cardSatisfiesResources`,
`resourceJSONContains`), which is the §C1 routing surface this PR
actually unlocks locally. True OS heterogeneity — the PowerShell
vs bash split that drives `command_interfaces` divergence — only
materialises on a real Windows host via
`deploy/windows/slave/install.ps1`; that signal is covered by
Phase 3 §C5 smoke, not by this compose node.

Changes:

1. **`multi-agent/dev/compose.distributed.yaml`** — added a
   `slave-cloud` service modelled on `slave-b`, mounting
   `./configs/slave-cloud.yaml`.
2. **`multi-agent/dev/configs/slave-cloud.example.yaml`** — new
   config marking the node with `display_name: slave-cloud-dev`,
   skills `[chat, mcp]` (identical to the other Linux slaves —
   WT-0 deliberately does not introduce a `sandbox` routing skill,
   because no driver code consumes it yet), and resource tags
   `[cloud, sandbox, ephemeral]`. Comment explains the §C1 tier
   intent.
3. **`multi-agent/tests/scripts/distributed_compose_test.go`** —
   extended the existing structural smoke to (a) require the new
   compose mount for `slave-cloud.yaml`, (b) parse the compose YAML
   and assert `services.slave-cloud` exists structurally (instead
   of a bare substring on `slave-cloud:`, which would also match a
   comment or mount line), and (c) load the new example config
   cleanly via `agentconfig.Load`.
4. **`multi-agent/.gitignore`** — added `/dev/configs/slave-cloud.yaml`
   alongside the sibling `slave-a.yaml` / `slave-b.yaml` entries so
   the live (non-`.example`) copy that operators populate with
   secrets is never tracked.

## Verification (this worktree)

The Go test suite and the compose-config check below cover this
PR's diff. `GOOS=windows go build` is a master-level health check
and is included only because the surrounding plan tracks it; this
PR contains no Go production-code changes.

```
go test ./...                  → all packages pass
GOOS=windows go build ./...    → exit 0   # no Go prod-code changes in this PR;
                                          # master-level health check only
docker compose -f dev/compose.distributed.yaml config   → exit 0
   # `compose config` validates YAML schema but does NOT verify
   # bind-mount source existence. To produce that exit-0 the six
   # example files were temporarily copied to their live names
   # (cp dev/configs/<name>.example.yaml dev/configs/<name>.yaml
   # for master/driver/observer/slave-a/slave-b/slave-cloud) and
   # removed after the check; the .gitignore additions above keep
   # the live copies out of git.
```

## What is NOT in scope here

- **`deploy/windows/install-slave.ps1` top-level installer alias** —
  the prompt mentions `deploy/windows/install-slave.ps1`; on master
  the installer lives at `deploy/windows/slave/install.ps1` (and a
  driver installer at `deploy/windows/driver/install.ps1`). That is
  the path upstream review settled on and what the smoke test
  references. Not creating a duplicate alias.
- **Real Windows-host smoke** — explicitly deferred to Phase 3
  WT-3-prod-multidevice per the prompt's "不需要真 Windows 机器" note.
- **`deploy/windows/deploy.ps1` one-shot full-stack installer** —
  belongs to Phase 2 WT-2-deploy-scripts; not touched.
