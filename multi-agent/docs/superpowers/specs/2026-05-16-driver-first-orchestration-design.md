# Driver-First Orchestration Design

## Context

The current system has three roles:

- `driver`: talks to the user, exposes MCP tools, discovers agents, drafts and submits task contracts.
- `master`: runs `fanout` orchestration through planner, DAG scheduler, MCP validation, `build_mcp` replans, reducer, and observer writes.
- `slave`: executes concrete work through chat, MCP tools, or `build_mcp`.

This works, but the master receives a mostly finalized task after the user conversation has already left the driver. When planning fails, a remote master can only replan from task output and tool errors. It cannot naturally return to the user for business-level clarification. That makes the master feel nondeterministic and harder to control.

## Decision

Move the default control plane to the driver. The driver should clarify requirements, inspect capabilities, build a structured contract, choose an execution route, and directly coordinate slaves when the task benefits from user-facing clarification or deterministic routing.

The master should remain available as an optional remote orchestration executor, not as the mandatory path for every multi-agent task.

## Goals

- Let the driver remain close to the user throughout planning, execution, failure handling, and follow-up clarification.
- Prefer direct driver-to-slave execution for simple or single-slave tasks.
- Support driver-managed multi-slave DAG execution for tasks that need multiple tools, generated MCP services, reducer behavior, or staged replans.
- Keep master compatibility for existing `fanout` workflows and for deployments that want a shared remote orchestrator.
- Keep all distributed file and result exchange through observer artifacts instead of shared local filesystem assumptions.
- Record contracts, resource snapshots, subtask state, generated MCP metadata, and final artifacts in observer for audit and recovery.

## Non-Goals

- Do not remove master in the first implementation.
- Do not fork planner, scheduler, MCP validation, and reducer logic into unrelated driver-only copies.
- Do not rely on local shared directories for distributed driver, master, and slave communication.
- Do not make `dry_run_contract` submit work or create MCP servers.

## Architecture

### Driver Control Plane

The driver owns these responsibilities:

- `inspect_capabilities`: collect visible masters, slaves, skills, resources, and structured `mcp_tools`.
- Requirement clarification: ask the user for missing input files, output expectations, success criteria, build permission, and target constraints.
- `draft_task_contract`: convert clarified business intent into a structured `TaskContract`.
- `dry_run_contract`: validate whether the contract matches visible skills, tools, resources, and build policy without side effects.
- Route selection:
  - `direct_slave` for one slave that satisfies required skills and tools.
  - `driver_fanout` for multi-slave, staged MCP, generated tools, or clarification-friendly orchestration.
  - `master_fanout` when the contract explicitly requests master routing or when a remote shared orchestrator is operationally preferred.
- Execution supervision: track DAG nodes, child task IDs, outputs, failures, cancellations, and observer events.
- Clarification loop: pause before or during execution when the contract is insufficient, `build_mcp` is blocked, MCP schema mismatches occur, or reducer output lacks required evidence.

### Slave Execution Plane

Slaves remain focused executors:

- `chat` for natural-language work.
- `mcp` for deterministic tool calls using structured JSON prompts.
- `build_mcp` for generating reusable MCP servers from validated build specs.

Slaves should advertise skills, resources, and structured `mcp_tools` through their agent card so the driver can reason about them before dispatch.

### Master Optional Executor

The master remains useful when:

- Existing clients depend on `fanout`.
- A deployment wants a shared remote orchestrator rather than long-running driver state.
- The task is batch-like and does not need interactive clarification after submission.
- The driver chooses `master_fanout` explicitly.

Master should eventually become an implementation option behind the same contract and observer abstractions used by driver orchestration.

## Route Selection Rules

`dry_run_contract` should return a route recommendation in addition to the current target and skill fields:

- `direct_slave`: exactly one allowed available slave satisfies required skills and required MCP tools, no missing tools, no reducer, no multi-node dependency.
- `driver_fanout`: multiple slaves are needed, generated MCP is allowed and useful, existing MCP tools live on different slaves, or the driver may need to ask the user follow-up questions.
- `master_fanout`: contract routing is `master_only`, an operator policy requires master, or the driver is configured not to hold orchestration state.
- `blocked`: required skills/tools/resources are unavailable and cannot be built under policy.

The recommendation is advisory but should be included in the submitted contract or task metadata so later events can be audited against the original routing decision.

## Shared Orchestration Library

To avoid two independent orchestrators, extract reusable master logic into shared internal packages:

- DAG scheduler and node state transitions.
- planner prompt construction and plan validation.
- MCP tool schema validation and JSON path rendering.
- `build_mcp` preflight validation.
- build-blocked and post-build replan helpers.
- reducer prompt construction and fallback summary behavior.
- observer artifact read/write helpers.

The master and driver can then use the same primitives with different control loops. The driver loop adds user clarification checkpoints; the master loop stays fully autonomous.

## Data Flow

1. User gives an initial request to the driver.
2. Driver calls `inspect_capabilities`.
3. Driver asks clarification questions until the request includes inputs, outputs, success criteria, and build policy.
4. Driver creates a `TaskContract`.
5. Driver calls `dry_run_contract`.
6. If route is `direct_slave`, driver delegates directly to that slave.
7. If route is `driver_fanout`, driver creates and runs a DAG against slaves, using observer artifacts for file transfer and output writes.
8. If route is `master_fanout`, driver submits the contract envelope to master.
9. Driver records snapshots, contracts, route decisions, subtask events, generated MCP metadata, and final outputs in observer.
10. On blocked build, invalid MCP args, missing evidence, or ambiguous final output, driver can pause and clarify with the user before continuing.

## Error Handling

- Capability mismatch: `dry_run_contract` returns `blocked` with missing skills, missing tools, missing resources, and candidate build targets.
- `build_mcp` blocked: driver records the block reason and either asks the user for permission to expand allowed packages or replans with a narrower spec.
- MCP schema mismatch: driver validates before dispatch and asks for clarification or builds/evolves a tool if the schema cannot represent the required input.
- Slave failure: required node failure fails the parent unless the contract marks the node optional or policy allows best-effort completion.
- Reducer uncertainty: driver can ask the user for final disambiguation instead of forcing a remote reducer answer.
- Observer write failure: parent task fails with the artifact/write URL error and preserves subtask outputs in observer.

## Testing Strategy

- Unit tests for route recommendation in `dry_run_contract`: `direct_slave`, `driver_fanout`, `master_fanout`, and `blocked`.
- Unit tests for direct driver-to-slave submission under `direct_first`.
- Scenario fixture for driver clarification: vague prompt, capability inspection, clarified contract, dry-run recommendation, and expected route.
- Container E2E with separate driver, master, and slave containers:
  - direct slave path without master.
  - driver-managed multi-slave path with existing MCP tools.
  - driver-managed `build_mcp` path using observer artifacts.
  - explicit `master_fanout` compatibility path.
- Regression checks that generated MCP code is real, persisted, advertised through `mcp_tools`, and usable by a later task.

## Migration Plan

1. Extend `dry_run_contract` with `recommended_route` and route reasons.
2. Add driver direct-submit fast path for `direct_slave`.
3. Extract shared orchestration primitives from `internal/orchestrator` without changing master behavior.
4. Add driver-managed fanout using the shared primitives.
5. Add clarification checkpoints around blocked build, invalid MCP args, and uncertain reducer output.
6. Keep master as explicit `master_fanout` route and preserve existing examples.
7. Update E2E tests to cover distributed containers and observer artifact transport.

## Open Policy Choices

- Default route should be `direct_first` for user-facing driver sessions.
- `master_only` should remain available for compatibility and operator-controlled batch execution.
- `allow_build_mcp` should default to false, but the driver should preserve enough real build spec/code context for later modification when build is allowed.
- Generated MCP servers should be treated as durable capabilities once registered and advertised through agent cards.

## Future Agentserver Work

This project should continue recording the need for agentserver-native support:

- capability/resource inspection as a first-class API;
- observer-backed artifact transport;
- durable generated MCP registration;
- structured task contracts;
- route recommendation and dry-run validation;
- optional remote orchestration with resumable state.

Until those are native, the driver should implement them explicitly and store the evidence in observer.
