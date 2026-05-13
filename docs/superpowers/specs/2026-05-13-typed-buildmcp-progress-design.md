# Typed BuildMCP and Progress Events Design

## Goal

Turn `build_mcp` from a prompt convention into a typed system protocol, and add progress events for long-running master planning and slave build work so drivers can distinguish interim progress from final results.

## Problem

The current planner prompt tells the model that `build_mcp` nodes must contain JSON specs, but the enforcement happens too late. A malformed natural-language `build_mcp` prompt reaches the slave and fails inside `BuildMCPExecutor` with a JSON parsing error. This makes task recovery harder because the error crosses the master/slave boundary before the system has validated the plan.

Long-running planning and build stages also do not emit structured progress. The driver sees a task as running until a terminal event appears. That makes it hard to tell whether a planner/build is actively working, stalled, or close to timeout.

## Design Summary

Add a typed `BuildSpec` contract shared by planner, master, and slave. Master validates and normalizes every `build_mcp` node before dispatch. Slave continues to accept only valid JSON specs, but those specs are now produced by a system-level preflight path rather than by prompt discipline alone.

Add progress events as first-class observer events. Progress events update a separate progress timeline and latest progress summary, but do not mark tasks completed or failed. Final events remain the only source of terminal task state.

Add idle timeout handling for long-running operations. A hard timeout limits total runtime, while an idle timeout trips when no meaningful progress has been emitted for too long.

## Scope

In scope:

- Typed `BuildSpec` model, validation, normalization, and JSON serialization.
- Master-side `build_mcp` preflight before slave dispatch.
- Repair/replan path when a `build_mcp` node is malformed.
- Progress events for master planning and slave `build_mcp`.
- Observer persistence and API/UI exposure for progress.
- Driver status/wait responses that distinguish progress from final output.

Out of scope:

- Changing generated MCP server runtime protocol.
- Supporting arbitrary free-text directly inside `BuildMCPExecutor`.
- Replacing Claude CLI execution.
- Implementing a full distributed cancellation protocol beyond existing task context cancellation.

## Typed BuildMCP Protocol

Create a shared package, tentatively `internal/buildspec`, with:

- `Spec`
- `ToolSpec`
- `Validate(spec Spec) error`
- `Normalize(spec Spec) Spec`
- `ParseJSON(raw string) (Spec, error)`
- `MarshalCanonical(spec Spec) string`

The fields mirror the existing executor contract:

- `name`
- `description`
- `tools`
- `hints`
- `allowed_packages`
- `compose_servers`
- `version`
- `iteration`
- `max_iterations`
- optional `prior_path`
- optional `patch_instructions`

Validation rules:

- `name` must match `^[a-z][a-z0-9_]{0,31}$`.
- `version >= 1`.
- `iteration >= 1`.
- `max_iterations >= 1`.
- `tools` must contain at least one entry.
- every tool must have `name`, `description`, `args_schema`, and `result_description`.
- `allowed_packages` defaults to an empty list.
- `compose_servers` defaults to an empty list.
- `prior_path` is required for `version >= 2`.

Normalization rules:

- trim whitespace from names and descriptions.
- sort and deduplicate `allowed_packages`.
- sort and deduplicate `compose_servers`.
- fill default `version=1`, `iteration=1`, `max_iterations=3` when omitted.
- marshal with stable JSON field order for hashing, testing, and observer output.

## Planner Node Model

Extend `planner.Node` with a structured field:

```go
BuildSpec json.RawMessage `json:"build_spec,omitempty"`
```

For backwards compatibility, the master accepts both forms:

- preferred: `{"kind":"build_mcp","skill":"build_mcp","build_spec":{...}}`
- legacy: `{"kind":"build_mcp","skill":"build_mcp","prompt":"{...json...}"}`

Before dispatch, master calls a preflight function:

```go
PrepareBuildMCPNode(node planner.Node) (planner.Node, error)
```

This function:

1. extracts `build_spec` if present, otherwise parses `prompt`.
2. validates and normalizes the spec.
3. writes canonical JSON back into `node.Prompt`.
4. clears or preserves `BuildSpec` only as needed for observer/debug output.

The slave still receives `skill=build_mcp` with `Prompt` equal to canonical JSON.

## Malformed Spec Handling

If preflight fails, master must not dispatch the node to slave. Instead it emits a validation progress/error event and asks the planner for a repair plan using structured context:

```text
BUILD_MCP_SPEC_INVALID:
node_id: <id>
error: <validation error>
bad_prompt_or_spec: <truncated raw value>
required_contract: <short schema summary>
```

The replan may return:

- a corrected `build_mcp` node.
- a chat node that explains why the MCP server cannot be built.

The existing max build iteration limit still applies. A malformed spec repair counts toward the same global build/replan budget so the system cannot loop forever.

## Progress Events

Add observer event types:

- `master_planning_progress`
- `master_planning_completed`
- `slave_task_progress`
- `slave_build_mcp_progress`

Progress event payload:

```json
{
  "phase": "planning|build_mcp|validate|smoke_launch|register|republish",
  "message": "short human-readable update",
  "elapsed_ms": 15000,
  "attempt": 1,
  "is_final": false
}
```

Terminal events keep their current types:

- `master_task_completed`
- `master_task_failed`
- `slave_task_completed`
- `slave_task_failed`

Progress events never update task status to completed or failed. They update only latest progress fields and the event timeline.

## Progress Timing

Default timings:

- master planning progress interval: 15 seconds.
- master planning idle timeout: 90 seconds.
- master planning hard timeout: 300 seconds for fanout planning.
- slave `build_mcp` progress interval: 20 seconds.
- slave `build_mcp` idle timeout: 120 seconds.
- slave `build_mcp` hard timeout: 900 seconds unless task timeout overrides it.

Rationale:

- 15 seconds is frequent enough for the driver/UI to show life during planning without producing noisy logs.
- 20 seconds is appropriate for build work because code generation, validation, and smoke launch can take longer than planning steps.
- idle timeout must be longer than several progress intervals to avoid false positives.
- hard timeout must stay finite so a stuck CLI process cannot hold the scheduler forever.

## Progress Reporter

Add a small internal abstraction:

```go
type ProgressReporter interface {
    Progress(ctx context.Context, eventType string, phase string, message string, fields map[string]any)
}
```

Use a ticker helper for long-running opaque subprocesses:

```go
RunWithProgress(ctx, interval, idleTimeout, hardTimeout, emit, fn)
```

The helper tracks:

- start time.
- last progress time.
- hard deadline.
- idle deadline.

For opaque Claude CLI calls, ticker progress is phase-level rather than token-level:

- planning: "planner still running"
- build generation: "generating MCP server source"

For explicit build steps, emit step progress:

- parsed spec
- reused existing server
- invoking Claude
- validating syntax
- validating imports
- writing generated file
- smoke launching server
- registering MCP server
- persisting dynamic config
- republishing capability card

## Observer Storage and API

Persist progress events in the existing `events` table like all other events.

Add aggregate fields to task-facing API responses:

- `latest_progress`
- `latest_progress_at`
- `latest_progress_phase`
- `final_output`
- `is_final`

`is_final` is true only for completed/failed terminal task states.

The web UI should render progress as timeline entries with a distinct label. It must not display a progress event as the task result.

## Driver Behavior

Driver status and wait commands should surface:

- current status.
- latest progress summary.
- final output only when `is_final=true`.

When waiting, progress events should be streamable or poll-visible so the caller can see activity before completion.

Driver timeout decisions should use both:

- terminal task status.
- latest progress age.

A task with recent progress remains active until the hard timeout. A task with no progress beyond idle timeout can be reported as stalled.

## Error Handling

If build spec validation fails:

- master emits progress/error context.
- master replans or repairs before dispatch.
- slave is not called with invalid `build_mcp` input.

If slave build generation blocks:

- existing `build_mcp_blocked` handle remains supported.
- master receives the handle and replans as today.

If observer progress post fails:

- execution continues.
- the local process logs the post failure.
- terminal task state must still be emitted.

If progress ticker cannot prove meaningful sub-step progress:

- it emits heartbeat progress with phase and elapsed time.
- idle timeout uses reporter activity, not token output, because Claude stream-json may be quiet during long thinking.

## Testing

Unit tests:

- `buildspec` validation accepts canonical valid specs.
- `buildspec` rejects natural language, missing tools, invalid names, and missing `prior_path` for version upgrades.
- planner node preflight rewrites `build_spec` into canonical JSON prompt.
- planner node preflight rejects invalid specs without dispatching to slave.

Orchestrator tests:

- malformed `build_mcp` node triggers replan instead of slave dispatch.
- valid `build_mcp` node dispatches canonical JSON.
- progress events are emitted during planning timeout windows.
- terminal task completion is not changed by progress events.

Executor tests:

- `BuildMCPExecutor` emits progress for each explicit phase.
- build timeout returns `timeout` while preserving prior progress chunks/events.
- existing JSON-only behavior remains intact for the executor.

Observer tests:

- progress events persist in event history.
- progress events update latest progress fields.
- progress events do not overwrite final output/status.

Driver tests:

- wait/status responses distinguish `latest_progress` from `final_output`.
- `is_final=false` while only progress exists.
- `is_final=true` after terminal status.

## Migration

This is backwards compatible:

- existing planner output using JSON in `prompt` still works.
- existing slave `build_mcp` JSON contract remains unchanged.
- existing terminal event types remain unchanged.
- existing observer views continue to work if they ignore unknown progress fields.

The new preferred planner format is `build_spec`, but prompt JSON remains supported until all planning prompts and tests are migrated.

## Acceptance Criteria

- A planner output with `kind=build_mcp` and natural-language `prompt` is rejected or repaired at master preflight, never delivered directly to slave `BuildMCPExecutor`.
- A valid `build_spec` is canonicalized and delivered to slave as JSON prompt.
- Driver can observe planning/build progress before final completion.
- Progress and final output are visibly and programmatically distinguishable.
- Idle timeout and hard timeout are both enforced for long-running planning/build operations.
- Existing dynamic MCP build tests still pass.

