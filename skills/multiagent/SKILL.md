---
name: multiagent
description: Use when operating this multi-agent workspace through driver and slave agents; drafting contracts; choosing routes; invoking slave skills; or diagnosing capability mismatches.
---

# Multiagent

## Core Rule

Treat the driver as the user-facing orchestrator. Clarify the business goal first, inspect current capabilities, draft a contract, dry-run it, then submit only when the route and required capabilities are explicit.

Prefer driver-to-slave orchestration. Avoid routing through an intermediate coordinator when the driver can inspect capabilities, clarify requirements, build a DAG, and talk to slaves directly.

Do not assume driver and slaves share a local filesystem. Move user files through driver manifests and observer artifacts. Use target IDs or display names from live discovery, not stale examples.

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
- Asking slave Claude Code to edit its own permissions; permission changes go through native `skill:"claude_permissions"` for now.
- Using `127.0.0.1` or local file paths as if they were reachable from other machines.
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
