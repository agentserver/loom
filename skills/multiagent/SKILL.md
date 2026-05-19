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
2. Clarify missing intent: goal, success criteria, inputs, outputs, allowed targets, and whether generated MCP code is allowed.
3. Draft a `TaskContract` with `draft_task_contract`, then adjust it if needed.
4. Call `dry_run_contract`. If blocked, ask for missing permission/capability or build an MCP server only when `allow_build_mcp` is true.
5. Submit with `submit_contract_task` unless a simple direct helper is more appropriate.
6. Monitor with `get_task`, `wait_task`, and `tail_subtasks`.

## Route Choice

- `direct_slave`: one slave already satisfies the task and no DAG is needed.
- `driver_fanout`: driver should orchestrate multiple slaves, staged MCP calls, generated MCP, or clarification-friendly DAGs.
- `blocked`: do not submit; fix missing skills, tools, resources, targets, or build policy first.

## Interface References

Read only the reference needed for the task:

- Driver MCP tools: `references/driver-tools.md`
- Slave skills and task prompt formats: `references/slave-skills.md`
- Task contract format and envelope: `references/task-contract.md`
- Common orchestration patterns and failure handling: `references/orchestration-patterns.md`

## Common Mistakes

- Skipping `dry_run_contract` and submitting an under-specified contract.
- Setting `allow_build_mcp: true` without requiring generated code to persist as artifacts.
- Calling `skill:"mcp"` with natural language instead of JSON `{server, tool, args}`.
- Asking slave Claude Code to edit its own permissions; permission changes go through native `skill:"claude_permissions"` for now.
- Using `127.0.0.1` or local file paths as if they were reachable from other machines.
