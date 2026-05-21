# Orchestration Patterns

## Clarify Before Submit

Ask the user for missing business facts before invoking tools when any of these are unclear:

- desired output format or write target;
- which files are inputs;
- whether generated MCP/code is allowed;
- whether repo writes are allowed;
- target constraints such as specific slave, GPU, Python, network, or package permissions.

## Driver-Managed Fanout

Use `driver_fanout` when the driver should retain control over clarification and DAG execution.

Pattern:

1. Inspect capabilities.
2. Draft and dry-run the contract.
3. If runnable with `driver_fanout`, submit without target override.
4. Driver planner creates DAG nodes for slaves.
5. Driver validates DAG shape, target skill, and `mcp` server/tool/args before dispatch.
6. On validation failure, driver feeds `PLAN_VALIDATION_ERROR` back to planner up to 5 attempts.

## Dynamic MCP

Use when the task needs a reusable deterministic tool that is not currently advertised. Use `bash` to generate and validate the source, then `register_mcp` (or `register_slave_mcp`) to install it.

Rules:

- Do not emit dependent use nodes until the generated server/tool is visible in refreshed capabilities.
- Prefer narrow MCP servers with one coherent tool contract.
- Keep `allowed_packages` minimal. If blocked by imports, replan with explicit user or policy approval.

## File Transfer

Driver files are local to the driver machine. Remote agents receive artifact URLs, not local paths. Two transports are available; pick by payload size and lifecycle.

### Option A: PUT-manifest via observer (large / archival)

- `submit_task.read_paths` registers driver-local files; the slave fetches via observer URL.
- `submit_task.write_paths` creates PUT targets; outputs sync back after the task completes.
- With `artifact_transport: observer_lazy`, observer stores lazy artifact/write records.
- Best for: large artifacts, anything that should survive in the observer artifact store, asynchronous output collection.
- Do not use `127.0.0.1` as a cross-machine URL.

### Option B: In-band file tools (small / synchronous / driver-mediated)

`read_slave_file` / `write_slave_file` / `stat_slave_file` (see `driver-tools.md`) move bytes synchronously through the driver's `FileRegistry`. Bytes never enter LLM context — `read_slave_file` returns a `blob_handle` and a driver-local `cache_path`; `write_slave_file` accepts `content`, `source_blob`, or `source_path`.

- Best for: shipping a generated MCP server source up to a slave, pulling a small log back for review, cross-slave copy (`read` from A → `write source_blob=...` to B), `stat`-then-write probing.
- Slave-cap is 8 MiB per read call; chunk larger files via `offset`/`length` or fall back to Option A.
- Requires the target slave to advertise `file`.

### Decision rule

| Situation | Use |
|---|---|
| Need observer artifact lineage / PR review | PUT-manifest |
| Shipping a scaffolded MCP source to a slave | `write_slave_file` |
| Pulling back a server file or log for inspection | `read_slave_file` |
| Copying file between two slaves | `read_slave_file` → `write_slave_file source_blob=...` |
| File > a few MiB and one-shot | PUT-manifest |
| Need to check "does this already exist?" cheaply | `stat_slave_file` |

Anti-pattern: `run_slave_bash` with a `cat <<EOF` or base64-decode heredoc to ship a file. Use `write_slave_file` instead.

## Permissions

If a slave says a command is blocked by Claude Code permissions:

1. Confirm the target advertises `claude_permissions`.
2. Call `get_slave_claude_permissions`.
3. Patch only the required permissions with `update_slave_claude_permissions`.
4. Retry the original task.

Do not ask slave Claude Code to edit its own permissions.

## Failure Handling

- `dry_run_contract` blocked: change the contract or ask for missing authorization before submit.
- `register_mcp` rejected: validate source syntax and imports before submitting; do not send natural language to `skill:"register_mcp"`.
- `mcp` schema mismatch: pass only fields in `input_schema`; use rendered JSON paths like `{{node.output.rows}}` for nested outputs.
- Required DAG node failed: report which node failed and which downstream nodes were skipped.
- Reducer uncertainty: preserve evidence from subtask outputs and ask a follow-up instead of inventing conclusions.
