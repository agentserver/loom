# Compute Packaging Repair Design

## Goal

Fix the current master/slave task decomposition failures in a generic way that moves `multi-agent` toward the compute packaging roadmap: capabilities, plans, and execution facts must become structured, validated, and auditable objects instead of prompt-only conventions.

This design specifically addresses:

- Master inventing invalid MCP call arguments after a dynamic MCP is built.
- Master treating required child failures as successful parent completion under `best_effort`.
- Weak DAG decomposition for reusable capability requests.
- Observer data that is insufficient to explain why a task was planned, replanned, dispatched, skipped, or failed.

The design is intentionally not specific to any single generated pipeline. It applies to all dynamic MCP tools and future reusable compute capabilities.

## Context

The roadmap documents define the target architecture as a three-timing compute packaging platform:

- Cold path: an LLM reads capability cards, intent, and constraints, then compiles a deterministic plan.
- Hot path: the runtime executes the compiled plan without LLM decisions.
- Adaptation path: capability, price, SLA, health, or external changes invalidate and replace affected plans.

Current `multi-agent` already has the early pieces:

- `driver-agent` submits user tasks.
- `master-agent` discovers agents and asks the planner to produce a DAG.
- `slave-agent` executes chat, MCP, and `build_mcp` tasks.
- `build_mcp` can synthesize a Python MCP server, register it, and republish an updated agent card.
- The observer service stores task, subtask, and MCP creation events.

The current failure mode is structural. `build_mcp` specs include `args_schema`, but the generated MCP server, dynamic MCP config, agent card, and planner context mostly expose only flat tool names. The master therefore lacks a machine-readable contract for valid MCP calls. The planner can invent arguments, and the orchestrator dispatches those calls without validation.

## Non-Goals

- Do not implement full Plan Cache in this repair.
- Do not implement a complete external capability registry service.
- Do not build a compute market, price negotiation, or SLA contract layer.
- Do not make dynamic MCP multi-tenant safe beyond the existing import allow-list and smoke launch.
- Do not force every task into multiple MCP servers. A single server with multiple tools is valid when it is the cohesive reusable API.

## Recommended Approach

Use structured capability contracts and deterministic validation as the Phase 0/1 bridge toward compute packaging.

Prompt improvements are still useful, but they are not sufficient. The system must make invalid calls impossible to dispatch when the invalidity is known from capability metadata.

The repair has four parts:

1. Publish structured MCP tool descriptors from slave capability cards.
2. Validate MCP call nodes in master before dispatch.
3. Add required/optional failure semantics to fanout plans.
4. Persist planning, replan, capability, validation, and failure evidence in observer storage.

## Capability Contract

Introduce a shared tool descriptor shape:

```go
type MCPToolDescriptor struct {
    Server            string          `json:"server"`
    Name              string          `json:"name"`
    Description       string          `json:"description,omitempty"`
    InputSchema       json.RawMessage `json:"input_schema,omitempty"`
    ResultDescription string          `json:"result_description,omitempty"`
}
```

The descriptor is the minimal CapabilityCard v0 operation contract. It should be published in the agent card as `mcp_tools` while preserving the old flat `tools` list for compatibility.

Example agent card fragment:

```json
{
  "skills": ["chat", "mcp", "build_mcp"],
  "tools": ["render_digit"],
  "mcp_tools": [
    {
      "server": "digit_image_pipeline",
      "name": "render_digit",
      "description": "Render digit images at supported sizes.",
      "input_schema": {
        "type": "object",
        "properties": {
          "n": {"type": "integer"},
          "output_dir": {"type": "string"}
        },
        "required": ["n"],
        "additionalProperties": false
      },
      "result_description": "JSON object with generated image paths."
    }
  ],
  "resources": {
    "cpu": "available",
    "memory_gb": 8,
    "tags": ["local"]
  }
}
```

### Descriptor Sources

Dynamic MCP descriptors come from the `build_mcp` spec:

- `buildToolSpec.Name` becomes `name`.
- `buildToolSpec.Description` becomes `description`.
- `buildToolSpec.ArgsSchema` becomes `input_schema`.
- `buildToolSpec.ResultDescription` becomes `result_description`.
- `buildSpec.Name` becomes `server`.

Static MCP descriptors can initially be best-effort:

- If `tools/list` returns descriptor fields, use them.
- If it only returns names, publish descriptors with `server` and `name` only.
- The planner may use name-only tools, but master-side validation can only enforce existence for those tools.

## Dynamic MCP Changes

`build_mcp` must preserve schema information through the entire tool publication path.

### Generated Server Contract

The Python generation system prompt should require `tools/list` to return full descriptors:

```json
{
  "result": {
    "tools": [
      {
        "name": "render_digit",
        "description": "...",
        "inputSchema": {"type": "object", "properties": {}, "required": []}
      }
    ]
  }
}
```

The implementation should accept both `inputSchema` and `input_schema` when parsing tool descriptors to tolerate MCP naming variants.

### Dynamic YAML

`dynamic_mcp.yaml` should persist structured descriptors:

```yaml
servers:
  digit_image_pipeline:
    transport: stdio
    command: python3
    args:
      - generated_mcp/digit_image_pipeline/v1.py
    version: 1
    created_at: "2026-05-11T00:00:00Z"
    spec_hash: "..."
    tools:
      - server: digit_image_pipeline
        name: render_digit
        description: Render digit images at supported sizes.
        input_schema:
          type: object
          properties:
            n:
              type: integer
            output_dir:
              type: string
          required:
            - n
          additionalProperties: false
        result_description: JSON object with generated image paths.
```

For backwards compatibility, loader code should accept old `tools: [name]` entries and convert them to descriptor stubs.

### Smoke Launch

`SmokeLaunchPython` should return descriptors rather than names. It should still pass if a server only returns names, but generated servers should be tested to return descriptors.

## Planner Context

`internal/planner/prompts.go` should present a capability-card summary, not a flat list of tool names.

The prompt must state:

- Use `mcp_tools` when planning MCP calls.
- `skill: "mcp"` prompt JSON must match exactly:

```json
{"server":"<server>","tool":"<name>","args":{}}
```

- The planner must not invent arguments outside `input_schema`.
- If an argument needed by the user is not present in the schema, replan by evolving the MCP through `build_mcp` instead of calling the tool with extra fields.
- `build_mcp` nodes are compile/build nodes. They should not be followed by use nodes in the same plan; the orchestrator will re-discover capabilities and request a use-phase plan.
- Reusable capability requests should become MCP capability contracts. Multiple independent reusable capabilities may be modeled as one server with multiple tools or multiple servers, depending on cohesion.

The prompt is a guardrail. Correctness still depends on master-side validation.

## Master-Side MCP Validation

Before dispatching a `planner.Node` with `Skill == "mcp"`, the orchestrator must validate the node against discovered capability descriptors.

Validation steps:

1. Parse the prompt as JSON.
2. Require non-empty `server` and `tool`.
3. Require `args` to be an object. Missing `args` is treated as `{}`.
4. Find a descriptor with matching server and tool on the target agent.
5. If no descriptor exists but the old flat tool name exists, allow only existence validation and record that schema validation was unavailable.
6. If `input_schema` exists, validate:
   - `type` must be `object` for top-level args.
   - Required properties must be present.
   - Unknown properties are rejected unless `additionalProperties` is explicitly `true` or is a schema object.
   - Basic JSON types are enforced for `string`, `number`, `integer`, `boolean`, `object`, and `array`.

Validation failures should not be dispatched to the slave.

Instead, the orchestrator should trigger a bounded replan with structured context:

```text
MCP_CALL_VALIDATION_FAILED:
target_id=<agent>
server=<server>
tool=<tool>
error=<validation error>
input_schema=<schema json>
bad_prompt=<original prompt json>
```

If replan exceeds the existing bounded iteration limit, the parent task fails.

## Plan Semantics

Extend `planner.Node` with optionality:

```go
Optional bool `json:"optional,omitempty"`
```

Default behavior:

- All nodes are required unless `optional: true`.
- Nodes in a `build_mcp` to `mcp` capability chain are required by default.
- A required node failure fails the parent task.
- An optional node failure can be summarized by the reducer.

This replaces the current ambiguity where `best_effort` can make a parent task appear completed even when a required child failed.

The fanout policy remains useful, but it should not override required-node semantics. `best_effort` means optional failures can be tolerated; it does not mean required failures are successful.

## PlanIR v0 Alignment

This repair should not build the full roadmap PlanIR, but it should avoid making migration harder.

Treat the current `planner.Node` as PlanIR v0 compatibility:

```go
type Node struct {
    ID        string   `json:"id"`
    TargetID  string   `json:"target_id"`
    Prompt    string   `json:"prompt"`
    DependsOn []string `json:"depends_on,omitempty"`
    Kind      string   `json:"kind,omitempty"`
    Skill     string   `json:"skill,omitempty"`
    Optional  bool     `json:"optional,omitempty"`
}
```

The next increment can split `Prompt` into typed `Inputs`, but this repair should first make existing prompt payloads machine-checkable where they already have structure:

- `build_mcp` prompt is a JSON build spec.
- `mcp` prompt is a JSON tool call.
- ordinary chat remains free text.

This gives the system a deterministic compile/validate step without requiring a large PlanIR rewrite.

## Observer and Provenance

The observer should persist enough evidence to explain planning decisions.

Add or extend events for:

- Initial plan produced by master.
- Replan request and output.
- `build_mcp` spec used by a slave.
- MCP server created with structured tool descriptors.
- MCP call validation success or failure.
- Required child failure causing parent failure.
- Optional child failure summarized by reducer.

At minimum, event payloads should include:

- `plan_json`
- `replan_reason`
- `capability_snapshot_hash`
- `mcp_tool_descriptors`
- `validation_error`
- `required`
- `optional`
- `parent_failure_reason`

This supports the roadmap's provenance direction and the current debugging need: the UI can explain why master chose a slave, why it replanned, why a slave did not receive a task, or why a parent task failed.

## Error Handling

### Schema Missing

If a tool descriptor has no schema:

- Validate server/tool existence.
- Dispatch is allowed.
- Emit an observer warning that schema validation was unavailable.

This preserves compatibility with static MCP servers.

### Schema Invalid

If a descriptor contains malformed JSON schema:

- Treat the capability as unhealthy for schema-validated planning.
- Do not dispatch schema-dependent calls.
- Emit validation failure and ask for replan or MCP rebuild.

### Replan Exhaustion

If repeated validation failures occur:

- Stop dispatching.
- Mark the parent task failed.
- Store the final validation failure as the parent failure reason.

### Build Success but Tool Unavailable

If `build_mcp` returns `mcp_tool_set` but re-discovery does not show the new descriptor:

- Re-discover once after a short delay.
- If still missing, fail the parent task with `capability_publish_failed`.

## Testing Strategy

### Unit Tests

Add tests around:

- Parsing `tools/list` descriptors with both `inputSchema` and `input_schema`.
- Backwards compatibility for name-only `tools/list`.
- `dynamic_mcp.yaml` read/write of structured descriptors.
- Agent card publication with both `tools` and `mcp_tools`.
- Planner prompt containing schema-aware MCP instructions.
- MCP call validator rejecting unknown args.
- MCP call validator rejecting missing required args.
- MCP call validator accepting valid args.
- `optional: true` child failure under `best_effort`.
- Required child failure causing parent failure.

### Integration Tests

Add a fake dynamic MCP flow:

1. Planner emits a `build_mcp` node with a tool schema.
2. Slave publishes a descriptor.
3. Planner emits a valid `mcp` call.
4. Orchestrator validates and dispatches it.

Add a negative flow:

1. Planner emits an `mcp` call with an invented arg.
2. Orchestrator rejects before dispatch.
3. Replan receives `MCP_CALL_VALIDATION_FAILED`.
4. Parent fails if replan remains invalid.

### Observer Tests

Verify observer storage can reconstruct:

- The initial plan.
- The replan reason.
- The generated MCP descriptor.
- The validation failure.
- The parent failure reason.

## File Impact

Expected files to modify:

- `internal/executor/buildmcp.go`
- `internal/executor/buildmcp_test.go`
- `internal/executor/mcp.go`
- `internal/executor/mcp_test.go`
- `internal/executor/pythonsmoke.go`
- `internal/executor/pythonsmoke_test.go`
- `internal/tunnel/tunnel.go`
- `internal/tunnel/tunnel_test.go`
- `internal/planner/planner.go`
- `internal/planner/prompts.go`
- `internal/planner/prompts_test.go`
- `internal/orchestrator/fanout.go`
- `internal/orchestrator/fanout_test.go`
- `internal/observer/event.go`
- `internal/observerstore/schema.sql`
- `internal/observerstore/store.go`
- `internal/observerstore/store_test.go`
- `internal/observerweb/server.go`

Optional new files:

- `internal/capability/types.go`
- `internal/capability/schema_validate.go`
- `internal/capability/schema_validate_test.go`

The optional `internal/capability` package is recommended to avoid spreading descriptor parsing and schema validation across executor, planner, tunnel, and orchestrator packages.

## Migration

The migration should be backwards compatible:

- Existing static MCP servers that return only names continue to work.
- Existing `dynamic_mcp.yaml` files with `tools: [name]` continue to load.
- Existing agent cards with only `tools` continue to be usable.
- New dynamic MCP builds publish structured descriptors.
- Master uses structured descriptors when available and falls back to name-only validation when necessary.

## Success Criteria

The repair is complete when:

- A dynamic MCP tool's input schema is visible to master after creation.
- Master never dispatches an MCP call with known invalid args.
- Required subtask failure fails the parent task.
- Optional subtask failure is visible but can be summarized.
- Observer UI/API can show plan, replan, schema, validation failure, and parent failure evidence.
- The implementation remains compatible with old name-only tools.

This establishes the compute packaging baseline: capability contracts are structured, cold-path planning is validated before hot-path execution, and execution facts are auditable.
