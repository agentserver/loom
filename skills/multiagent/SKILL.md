---
name: multiagent
description: Use when operating this multi-agent workspace through driver and slave agents; drafting contracts; choosing routes; invoking slave skills; or diagnosing capability mismatches.
---

# Multiagent

## Core Rule

Treat the driver as the user-facing orchestrator. Clarify the business goal first, inspect current capabilities, draft a contract, dry-run it, then submit only when the route and required capabilities are explicit.

Prefer driver-to-slave orchestration. Avoid routing through an intermediate coordinator when the driver can inspect capabilities, clarify requirements, build a DAG, and talk to slaves directly.

Do not assume driver and slaves share a local filesystem. Move user files through driver manifests and observer artifacts. Use target IDs or display names from live discovery, not stale examples.

When the user asks which agents are online, call `list_agents`. It returns only
currently `available` agents unless `include_unavailable:true` is explicitly
passed, and includes each agent's `role` (`driver`, `master`, or `slave`) plus
`status`; do not infer role from display names.

Do not assume every slave has Bash. Inspect each target's `platform` and
`command_interfaces` from `list_agents` or `inspect_capabilities` before
choosing a shell helper. Use `run_slave_shell` when the shell is unspecified,
`run_slave_powershell` for Windows / PowerShell targets, and
`run_slave_bash` only when the target advertises a real Bash command
interface.

Shell helpers are asynchronous by default and return a `task_id`. Pass
`wait:true` only for short commands where blocking the MCP call is intentional;
otherwise monitor with `get_task`, `wait_task`, and `tail_subtasks`.

If an outer MCP client times out, a stdio session is interrupted, or a long
`wait:true` helper response is lost, call `list_driver_tasks` before assuming
the task was lost. The driver records each delegated `task_id` locally as soon
as agentserver returns it.

## Initialization (REQUIRED before any submit_task)

You MUST bind the driver to its parent codex thread once per session,
BEFORE calling any parent-link skill (`submit_task` / `resume_task` /
`submit_contract_task` with skill ∈ {`chat`, `chat_resume`, `fanout`,
`fanout_strict`, `route`} — i.e. anything that spawns a nested codex on a
slave). Bash-only / powershell-only submissions don't need this.

**Step 1** — run this discovery script as a shell tool call. **Pipe the
script body into bash via a quoted heredoc** (`bash <<'DISCOVER_EOF' ...
DISCOVER_EOF`) rather than `bash -c '...'`. The script contains
single-quoted regex literals (`UUID_RE='^[0-9a-fA-F]{8}-...$'`) that would
conflict with single quotes around a `-c` argument. The quoted heredoc
form (`<<'DISCOVER_EOF'`) disables variable / command expansion AND
preserves the script byte-for-byte, satisfying the CI drift check.

**Critical**: do NOT wrap the invocation in `THREAD_ID=$(...)` or any other
shell-variable assignment. Shell-tool calls do not persist environment
between invocations — a variable set in one tool call vanishes before the
next. The script must print the thread_id to stdout so the model sees it
directly in the tool-call response. The model then copies that value
verbatim into the `bind_thread` MCP call.

```
bash <<'DISCOVER_EOF'
#!/usr/bin/env bash
# discover-thread.sh — print the current codex thread_id to stdout, or
# exit non-zero with an explanation on stderr. Deterministic; no heuristics.
#
# Signals allowed:
#   1. $CODEX_THREAD_ID (codex itself wrote it, or a forwarder did)
#   2. /proc/<ppid>/fd open rollout file under $CODEX_HOME/sessions/
#      with exact UUID suffix (Linux)
#
# Cwd-based or "newest thread" sqlite lookup is INTENTIONALLY OMITTED —
# the same cwd routinely hosts multiple codex threads, and "newest" can
# silently mis-attribute to the wrong one.
#
# /proc/<ppid>/fd matches MUST be:
#   - under $CODEX_HOME/sessions/ (canonicalized prefix)
#   - basename ends with -<UUID>.jsonl
# Across the parent-chain walk we collect UNIQUE thread_id candidates;
# exactly 1 → emit it; 0 → fall to manual fallback; >1 → fail loud and
# print the candidates so operators can see the invariant violation.

set -euo pipefail

UUID_RE='^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
valid() { [[ "$1" =~ $UUID_RE ]]; }

# Source 1: env
if [[ -n "${CODEX_THREAD_ID:-}" ]] && valid "$CODEX_THREAD_ID"; then
    echo "$CODEX_THREAD_ID"
    exit 0
fi

# Source 2: Linux /proc/<pid>/fd walk
CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
# Canonicalize so symlinked CODEX_HOME doesn't false-negative the prefix
# check (gopsutil-side fd readlinks return realpath).
SESSIONS_ROOT=$(cd "$CODEX_HOME/sessions" 2>/dev/null && pwd -P || true)

if [[ -n "$SESSIONS_ROOT" && -d "/proc/$PPID/fd" ]]; then
    declare -A seen=()
    pid=$PPID
    for _ in 1 2 3 4 5; do
        [[ "$pid" -gt 1 ]] || break
        if [[ -d "/proc/$pid/fd" ]]; then
            for fd in /proc/$pid/fd/*; do
                target=$(readlink -f "$fd" 2>/dev/null) || continue
                # Reject anything not under sessions root
                [[ "$target" == "$SESSIONS_ROOT"/* ]] || continue
                # Extract trailing UUID (validate strict shape)
                cand=$(printf '%s\n' "$target" | sed -nE \
                    's|.*-([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$|\1|p')
                if [[ -n "$cand" ]] && valid "$cand"; then
                    seen["$cand"]=1
                fi
            done
        fi
        pid=$(awk '/^PPid:/{print $2}' "/proc/$pid/status" 2>/dev/null || echo 1)
    done

    case "${#seen[@]}" in
        1)
            for k in "${!seen[@]}"; do echo "$k"; done
            exit 0
            ;;
        0)
            : # fall through to manual-fallback message below
            ;;
        *)
            {
                echo "discover-thread: parent codex appears to hold MORE THAN ONE"
                echo "open rollout fd under $SESSIONS_ROOT. Refusing to guess."
                echo "Candidate thread_ids:"
                for k in "${!seen[@]}"; do echo "  - $k"; done
                echo "Next: use /status in codex to identify the correct id"
                echo "and call driver.bind_thread(thread_id=...) directly."
            } >&2
            exit 2
            ;;
    esac
fi

# All sources failed — explicit fail with operator-actionable message.
{
    echo "discover-thread: could not determine the parent codex thread_id."
    echo ""
    echo "Tried:"
    echo "  1. \$CODEX_THREAD_ID env (unset or malformed)"
    if [[ -d "/proc/$PPID/fd" ]]; then
        if [[ -z "${SESSIONS_ROOT:-}" ]]; then
            echo "  2. /proc/<ppid>/fd scan (skipped — \$CODEX_HOME/sessions missing)"
        else
            echo "  2. /proc/<ppid>/fd scan under $SESSIONS_ROOT (no match)"
        fi
    else
        echo "  2. /proc/<ppid>/fd scan (skipped — not on Linux)"
    fi
    echo ""
    echo "Next steps:"
    echo "  - In your codex session, type /status. Find the line"
    echo "    starting with 'thread_id:' and copy the UUID."
    echo "  - Then call driver.bind_thread(thread_id=<that uuid>) manually."
} >&2
exit 1
DISCOVER_EOF
```

It prints the thread_id to stdout, or exits non-zero with an actionable
message on stderr. Exit codes: 0 = success, 1 = no source resolved,
2 = multi-candidate invariant violation.

**Step 2** — read the UUID from Step 1's stdout, then call `bind_thread`
with that value as a literal argument. Expect `{ "bound": true,
"thread_id": "..." }` in the response:

```
driver.bind_thread(thread_id="<paste the UUID from Step 1's stdout here>")
```

If Step 1 exits non-zero (typical on macOS / Windows without
`CODEX_THREAD_ID` set, or in containers without /proc access), or if
running on native Windows (cmd / PowerShell, no bash — skip Step 1
entirely), ask the user:

> "I need your current Codex thread id to set up parent linkage.
>  In your codex session, run `/status` and paste the UUID after
>  `thread_id:` here."

Then call `bind_thread` with that value.

If you skip this step entirely, every parent-link submission will fail
with: "driver not bound to a codex thread; call `bind_thread` first ..."

**Re-binding mid-session** (e.g. after `/resume` switches to a different
codex thread) is supported: just re-run discovery and call `bind_thread`
again — the new value replaces the old one.

## Default Workflow

1. Call `inspect_capabilities` before planning nontrivial work.
2. **Clarify intent before contracting** — see [Clarification](#clarification). Do not draft a contract until the goal, success criteria, inputs, outputs, and target choice are explicit.
3. Draft a `TaskContract` with `draft_task_contract`, then adjust it if needed.
4. Call `dry_run_contract`. If blocked, ask for missing permission/capability, or add a new MCP server via the `scaffold-mcp-server` + `mcp-acceptance` skills before `register_slave_mcp`.
5. Submit with `submit_contract_task` unless a simple direct helper is more appropriate.
6. Monitor with `get_task`, `wait_task`, and `tail_subtasks`.

## Clarification

Borrowed from `superpowers:brainstorming` and adapted for the driver/slave loop. Treat it as a hard gate: **no `draft_task_contract`, no `scaffold-mcp-server`, no `register_slave_mcp` until the dimensions below are pinned down.** "This task is too simple to need clarification" is the most expensive assumption in this workspace — under-specified contracts get routed to the wrong slave or build the wrong MCP.

### How to ask

- **One question per turn.** Do not stack multiple questions into a single message — the user picks the easy one and the rest get dropped.
- **Prefer multiple choice.** Concrete options ("A: jetson only / B: any slave / C: forbid jetson") get sharper answers than open-ended prompts. Use `AskUserQuestion` when you need structured answers.
- **Lead with your recommendation.** Put the option you'd pick first and label it `(recommended)`, so silent or auto-mode users default to a sensible answer.
- **Stop when ambiguous, not when uncertain.** If you can make a reasonable call and the cost of being wrong is low, just decide — clarification is for branch points the user cares about, not for every parameter.

### What to clarify

Walk these dimensions before drafting; skip any that the user has already pinned down.

| Dimension | Example question |
|---|---|
| **Goal & success criteria** | "What does 'done' look like — a one-shot answer, a registered MCP tool, a file written back?" |
| **Inputs** | "Where does the data come from — user attachment, slave-local file, external API?" |
| **Outputs** | "Where should the result land — chat reply, file in `write_targets`, a registered tool?" |
| **Target choice** | "Which slave should run this — jetson (GPU/aarch64), local (amd64), or either?" |
| **Route** | "One slave end-to-end (`direct_slave`) or driver-orchestrated DAG (`driver_fanout`)?" |
| **Constraints** | "Any hard limits — latency, network egress, allowed packages, idempotency?" |

### Stop-and-confirm gate

Before moving from clarification to `draft_task_contract`, summarize back in one short paragraph: *"I'm going to do X on slave Y, with inputs Z, producing W. OK to draft the contract?"* If the user redirects, loop back to clarification — do not bypass.

## Route Choice

- `direct_slave`: one slave already satisfies the task and no DAG is needed.
- `driver_fanout`: driver should orchestrate multiple slaves, staged MCP calls, or clarification-friendly DAGs.
- `blocked`: do not submit; fix missing skills, tools, resources, targets, or build policy first.

## Interface References

Read only the reference needed for the task:

- Driver MCP tools: `references/driver-tools.md`
- Slave skills and task prompt formats: `references/slave-skills.md`
- Task contract format and envelope: `references/task-contract.md`
- Common orchestration patterns and failure handling: `references/orchestration-patterns.md`

## Building New MCP Servers

When `dry_run_contract` reports missing tools and a slave has `register_mcp`, build the server with:

1. `scaffold-mcp-server` — generate the stdio JSON-RPC skeleton from `spec.json` (same spec feeds `register_mcp`).
2. Hand-edit handler bodies between `# @@scaffold:business:start` markers.
3. `mcp-acceptance` — drive `tools/call` against a `cases.jsonl` and gate registration on exit 0.
4. `register_slave_mcp` with the same `spec.json` and `source_path`.

Do not call `register_slave_mcp` directly from a one-shot Claude generation: `register_mcp` only does structural checks, so semantic bugs in tool implementations register silently.

## Common Mistakes

- Skipping clarification and jumping straight to `draft_task_contract` / `scaffold-mcp-server` because "the intent seems obvious".
- Stacking 3+ clarifying questions into one message; user picks the easy one and the rest get lost.
- Skipping `dry_run_contract` and submitting an under-specified contract.
- Calling `register_mcp` / `register_slave_mcp` without first running `mcp-acceptance` (structural smoke does NOT catch semantic bugs).
- Hand-writing the stdio JSON-RPC skeleton instead of using `scaffold-mcp-server` (re-introduces the `args_schema` ↔ `inputSchema` desync and "forgot `notifications/initialized` returns None" class of bugs).
- Calling `skill:"mcp"` with natural language instead of JSON `{server, tool, args}`.
- Asking slave Claude Code to edit its own permissions; permission changes go
  through native `skill:"permissions"` (legacy alias: `claude_permissions`) for
  now.
- Using `127.0.0.1` or local file paths as if they were reachable from other machines.
- Calling `run_slave_bash` on a Windows / PowerShell target. Bash means real
  Bash only; use `run_slave_powershell` or `run_slave_shell` when the target
  does not advertise Bash in `command_interfaces`.
- Assuming a timed-out driver tool created no task. Check `list_driver_tasks`
  for a locally recorded `task_id`, then inspect it with `get_task` or
  `wait_task`.
- Hand-rolling `cat <<EOF` or base64-decode payloads over `run_slave_bash` to ship a file when the slave advertises `file` — use `write_slave_file`. Same for `cat`-via-bash to pull a log back; use `read_slave_file` (returns a `blob_handle` + driver-local `cache_path`, bytes stay out of LLM context).
- Auto-answering an `awaiting_user` question without surfacing it to the
  real human (you are the proxy, not the decider).
- Treating "approve" answers to `request_permission` as if they granted new
  permissions — they don't.

## Mid-chat human-in-the-loop

Chat tasks (`skill="chat"`) can pause mid-conversation. `wait_task` may
return `status:"awaiting_user"` instead of `completed`. When that happens:

- The `question` field carries `kind` (`ask_user` | `request_permission`),
  `question` text, optional `options`, optional `context`.
- **Default behaviour: surface the question to the human via
  `AskUserQuestion`. Do not invent an answer.** Only auto-answer when the
  question text itself explicitly says e.g. *"do not ask user, decide based
  on X"*.
- Call `resume_task(last_task_id=<current_task_id>, answer=<user's reply>)`
  to continue. `resume_task` returns the same shape as `wait_task` — keep
  looping until you see `status:"completed"`.

`request_permission` is **advisory**, not enforcement. Answering "approve"
does NOT grant the slave new abilities; if the user wants to actually
elevate permissions, call `update_slave_claude_permissions` /
`register_slave_mcp` separately.
