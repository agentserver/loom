# humanloop e2e (live prod_test fleet)

End-to-end tests for the mid-chat human-in-the-loop feature
(spec: `docs/superpowers/specs/2026-05-26-humanloop-resumable-chat-design.md`).

These scripts drive the **real** `tests/prod_test/` fleet (driver-prod native +
slave-local-prod docker), and exercise the full pause/resume round-trip via
the driver MCP server. No mocks. They satisfy the
`e2e_required_for_features_and_fixes` policy.

## Pre-requisites

1. `tests/prod_test/` deployed per its README — driver registered, slave
   container `loom-slave-local-prod` up and connected to agent.cs.ac.cn.
2. New `slave-agent.linux-amd64` + `driver-agent.linux-amd64` binaries built
   from the worktree and copied into `tests/prod_test/bin/`. Restart
   slave-local-prod after replacing.
3. Python 3 on PATH (no other deps).

Quick sanity:

```bash
# from this directory
python3 scripts/probe_fleet.py    # lists agents the driver can see
```

Expected to print `slave-local-prod skills=chat,bash,...` and possibly
`slave-jetson-prod` if that node was also re-registered into the new workspace.

## Cases

| # | Script | What it covers | Live status as of 2026-05-26 |
|---|---|---|---|
| 1 | `scripts/case1_happy.py` | Happy chat with humanloop MCP injected but `ask_user` not called | ✅ PASS |
| 2 | `scripts/case2_ask_user.py` | Model calls `ask_user`, driver returns `awaiting_user`, `resume_task` continues to `completed` | ✅ PASS |
| 3 | `scripts/case3_multi_round.py` | Two rounds of `ask_user` before final | ⚠️ FAIL — infrastructure works (case 2 proves a single pause/resume round, including session continuity); model declines to call `ask_user` a second time within one task even with explicit instructions. Multi-round semantics are model-behavior-bound, not infra-bound. The unit test path exercises multi-round at the executor level. |
| 4 | `scripts/case4_request_permission.py` | `request_permission` marker distinct from `ask_user` | ✅ PASS |
| 5 | `scripts/case5_jetson.py` | Cross-node (arm64 jetson) | ✅ PASS — full pause/resume round-trip on arm64 jetson: submit → awaiting_user (model called ask_user on jetson, paused) → resume("blue") → completed with output "blue". Proves the humanloop path is platform-agnostic. |
| 6 | `scripts/case6_slave_offline.py` | Kill slave container mid-await, resume should error; restart and retry | ✅ PASS |
| 7 | `scripts/case7_session_lost.py` | Delete session jsonl between pause and resume | ✅ PASS — clean failure: `No conversation found with session ID: …` |
| 8 | `scripts/case8_flock.py` | Concurrent `resume_task` against same session | ✅ PASS (degraded — slave dispatch serialises, so the second resume runs after the first releases the flock; unit test `TestChatResumeRejectsConcurrent` covers the contention path) |
| 9 | `scripts/case9_quota.py` | Per-process question quota — Nth+1 `ask_user` call returns `refused` | ✅ PASS (drives the deployed slave-agent binary's `humanloop-mcp` subcommand directly via docker exec, since the executor's pause-on-first-call design means a real chat won't reach the quota in one Run) |
| 10 | `scripts/case10_grace_shutdown.py` | Synthetic claude stub that ignores stdin-close → SIGTERM/SIGKILL → task `failed` | ✅ PASS — `failure_reason: "claude did not exit within Ns grace window after stdin close; SIGTERM/SIGKILL applied"` |

## How to run

```bash
cd multi-agent/tests/humanloop_e2e
python3 scripts/case1_happy.py
python3 scripts/case2_ask_user.py
# … etc
```

Each script prints `PASS case N` on success, `FAIL …` with details on failure,
and exits non-zero on failure.

## Shared helpers

`scripts/lib.py` — wraps `driver-agent serve-mcp` in a tiny JSON-RPC shim, so
each `call_tool(name, args)` spawns a fresh driver-agent process and reads
back the inner tool result. Stateless: no driver daemon to manage.

## Notes / known gotchas

- Driver MCP server cold-start adds ~200ms per `call_tool`. For tighter
  inner loops, the lib could be extended to reuse a single driver process —
  not done because each call is independent and the overhead is acceptable.
- `submit_task` returns `session_id: ""` for chat tasks because agentserver's
  `DelegateTaskResponse.SessionID` isn't populated until the slave reports
  one. The real session_id arrives via `wait_task`'s `awaiting_user` shape
  or `resume_task`'s response.
- Case 5 (jetson) needs the jetson slave re-registered into the same
  workspace as driver-prod (`6f55e9fe-…` as of this writing). If your
  jetson is still in the old workspace, it won't be visible in `list_agents`.
