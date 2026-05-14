# Distributed Driver/Master Contract Design

**Date:** 2026-05-14
**Status:** Draft (awaiting user review)
**Scope:** Redesign the driver/master boundary for a distributed deployment where driver, master, and slaves run on different machines or containers. The driver owns user-facing requirement clarification and compiles a structured task contract. The master consumes only constrained contracts and resource snapshots. Slaves execute concrete work. Observer stores durable artifacts, code outputs, execution state, and audit records. Development and E2E tests run agents in long-lived containers with shared source mounts for binary development, while task data moves only through observer artifacts.

## 1. Problem

The current master-centered shape gives the master too much freedom: it can receive raw natural language, infer business intent, choose resources, decide whether to build tools, plan a DAG, route data, and summarize results. This makes it hard to control and hard to test.

The deployment model also invalidates local filesystem assumptions. In the real system, driver, master, and slaves may run on different machines. A path written by one process is not automatically readable by another. Local task directories can be used as scratch space, but they cannot be the system of record for task artifacts or follow-up conversation context.

The desired design separates these concerns:

- Driver handles ambiguity with the user.
- Master handles bounded orchestration.
- Slaves handle execution.
- Observer handles durable artifacts and state.
- Agentserver handles workspace membership, registration, discovery, tunnel connectivity, and authorization.

## 2. Goals

1. Make the driver the user-facing "intent compiler": it talks with the user, inspects available resources, clarifies business intent, and emits a structured `TaskContract`.
2. Make the master deterministic enough to test: it receives structured inputs, validates hard constraints, and either returns a bounded plan or rejects the request.
3. Preserve real generated code and other task outputs as durable artifacts without requiring the user to see them by default.
4. Keep `allow_build_mcp=false` as the default, while still allowing ordinary code artifacts to be generated and saved.
5. Support distributed execution where no business data is passed through shared local directories.
6. Add a realistic development and E2E model with long-lived containers for driver, master, and slaves.

## 3. Non-Goals

- Do not replace agentserver with LangGraph, CrewAI, AutoGen, or another external orchestrator.
- Do not make master a general-purpose conversational agent.
- Do not require users to inspect generated code artifacts unless they ask for them or an approval gate requires it.
- Do not use a shared filesystem as the task artifact transport in distributed tests.
- Do not automatically create or register new MCP tools unless the contract explicitly allows `build_mcp`.

## 4. Reference Patterns

This design borrows patterns from existing agent systems without taking a runtime dependency on them:

- Anthropic workflow guidance: prefer predictable workflows where possible, reserve autonomous agents for open-ended work.
- OpenAI Agents SDK: use manager-as-tools and handoff patterns, but keep the user-facing driver in control of the conversation.
- LangGraph and similar graph runtimes: model interrupts and human approvals as first-class state transitions.
- MCP elicitation: ask the user for missing information before execution rather than making low-confidence assumptions.
- Structured output systems: require schema validation after LLM output; valid JSON alone is not enough.

The local adaptation is: driver compiles intent, master plans within constraints, observer persists state and artifacts.

## 5. Roles

### Driver

Driver is the user-facing control plane. It has an LLM, but its job is not free-form execution. Its job is to compile user intent into a validated task contract.

Driver responsibilities:

- Discover current resources from agentserver/master.
- Ask business-level clarification questions when the request is ambiguous.
- Produce and validate `TaskContract`.
- Choose direct slave execution when one target can satisfy the contract.
- Submit to master only when orchestration is needed.
- Track conversation continuity through observer artifact metadata, not local paths.
- Ask the user for approval when policy requires it.

### Master

Master is a constrained planner and DAG executor.

Master responsibilities:

- Accept `TaskContract`, `ResourceSnapshot`, and planner constraints.
- Reject invalid or over-budget contracts.
- Generate route or fanout plans inside hard limits.
- Validate plan target IDs, skills, MCP tool schemas, artifact handles, DAG shape, and budget.
- Delegate concrete nodes to slaves.
- Store plan and execution events through observer.

Master must not infer missing business requirements from raw user prose. If the contract is incomplete, master should return a structured rejection asking driver to clarify.

### Slave

Slave is a capability executor.

Slave responsibilities:

- Execute delegated work in local scratch space.
- Fetch inputs from observer artifact handles or approved proxied URLs.
- Upload outputs, code, patches, logs, and structured results to observer.
- Return artifact references rather than relying on local paths.

Slave local directories are temporary. They may be deleted after the task without losing durable state.

### Observer

Observer is the durable state and artifact layer.

Observer responsibilities:

- Store task contracts, resource snapshots, plans, events, and audit records.
- Store code artifacts, patches, generated files, logs, and result files.
- Support lazy artifact loading by ID.
- Support future approval gates for plans, writes, repo commits, and capability expansion.
- Let driver resolve follow-up references such as "the code from last time" by conversation or task metadata.

## 6. Core Data Model

### TaskContract

Driver emits a `TaskContract` after clarifying the user's intent.

```json
{
  "version": 1,
  "conversation_id": "conv_123",
  "intent": {
    "goal": "Analyze and propose a distributed driver/master contract design",
    "business_context": "The user wants master to be more deterministic",
    "success_criteria": [
      "Separate driver clarification from master orchestration",
      "Persist generated code as observer artifacts",
      "Define multi-container E2E requirements"
    ],
    "non_goals": [
      "Do not implement code in this task",
      "Do not auto-build new MCP tools"
    ]
  },
  "data_contract": {
    "read_artifacts": [],
    "write_targets": [
      {
        "type": "artifact",
        "kind": "document",
        "name": "distributed-driver-master-contract-design.md"
      }
    ]
  },
  "execution_policy": {
    "routing": "direct_first",
    "allow_master": true,
    "allow_build_mcp": false,
    "allow_code_artifacts": true,
    "code_persistence": "observer_artifact_store",
    "expose_code_to_user": "on_request",
    "write_mode": "artifact_only",
    "max_dag_nodes": 6,
    "max_depth": 3,
    "max_concurrency": 3,
    "require_plan_approval": false,
    "require_user_approval_for_repo_writes": true
  },
  "capability_requirements": {
    "skills": ["architecture_reasoning"],
    "tools": [],
    "resources": {
      "tags": ["go", "multi-agent", "distributed"]
    }
  }
}
```

### ResourceSnapshot

Resource snapshots are read-only inputs to driver and master. They describe what is available now.

```json
{
  "generated_at": "2026-05-14T12:00:00Z",
  "agents": [
    {
      "agent_id": "agent-a",
      "short_id": "abc123",
      "display_name": "slave-code-a",
      "status": "online",
      "skills": ["chat", "mcp", "build_mcp"],
      "mcp_tools": [],
      "resources": {
        "cpu": 8,
        "memory_gb": 32,
        "gpu": false,
        "devices": [],
        "tags": ["go", "repo-edit"]
      }
    }
  ]
}
```

### ArtifactRecord

All durable outputs use observer artifact records.

```json
{
  "artifact_id": "art_code_123",
  "conversation_id": "conv_123",
  "task_id": "task_abc",
  "producer_agent_id": "agent-a",
  "kind": "code",
  "language": "go",
  "name": "generated_helper.go",
  "sha256": "...",
  "bytes": 1234,
  "content_ref": "observer://artifacts/art_code_123/content",
  "visible_to_user": false,
  "purpose": "Generated helper retained for follow-up edits"
}
```

Generated code must be real and retrievable. It does not need to be displayed to the user by default.

## 7. Build MCP Versus Code Artifacts

`allow_build_mcp=false` means the system must not automatically create or register a new MCP server, tool, or durable capability.

It does not mean code generation is forbidden.

Default policy:

```json
{
  "allow_build_mcp": false,
  "allow_code_artifacts": true,
  "code_persistence": "observer_artifact_store",
  "expose_code_to_user": "on_request"
}
```

The distinction is:

| Concept | Meaning | Default |
|---|---|---|
| Code artifact | Task output such as script, patch, test file, generated source, config | Allowed |
| build_mcp | System capability expansion by creating/registering a new MCP tool | Denied |

If a code artifact becomes repeatedly useful, driver may suggest upgrading it to MCP. That upgrade requires a new contract with `allow_build_mcp=true`.

## 8. Routing Policy

Driver should apply direct-first routing before involving master.

Use direct slave execution when:

- The contract maps to one obvious capability.
- No DAG, parallelism, reduction, or cross-agent dependency is needed.
- Required inputs are already available as artifact handles.

Use master when:

- The task requires decomposition.
- Multiple agents or tools are needed.
- The task benefits from parallel work and reduction.
- A plan approval gate is required.
- Driver cannot safely choose one target but the contract is otherwise complete.

If required fields are missing, driver should ask the user. It should not send an ambiguous contract to master.

## 9. Master Validation Rules

Master must validate before dispatch:

- Contract schema version is supported.
- `allow_master=true`.
- DAG node count <= `max_dag_nodes`.
- DAG depth <= `max_depth`.
- Concurrent running nodes <= `max_concurrency`.
- Every target is present in `ResourceSnapshot`.
- Every target is allowed by policy, if an allowlist is present.
- Every MCP call conforms to the advertised tool `input_schema`.
- `build_mcp` nodes are rejected unless `allow_build_mcp=true`.
- Artifact inputs refer to valid observer artifacts or approved lazy handles.
- Write operations match `write_targets`.
- Repo writes or commits are rejected unless explicitly approved.

Invalid plans should produce structured errors:

```json
{
  "error": "plan_rejected",
  "reason": "build_mcp_not_allowed",
  "clarification_request": null
}
```

Incomplete contracts should produce clarification requests:

```json
{
  "error": "contract_incomplete",
  "missing": ["intent.success_criteria", "data_contract.write_targets"],
  "clarification_request": "Should the result be a document artifact or a repository patch?"
}
```

## 10. Write Modes

The default write mode is `artifact_only`.

| Mode | Behavior |
|---|---|
| `artifact_only` | Store outputs in observer only. No repo or user filesystem mutation. |
| `patch` | Generate patch/diff artifacts. Applying them requires approval. |
| `repo_commit` | Commit to an authorized repository workspace. Requires explicit policy and approval. |

In distributed mode, `write_paths` should not mean arbitrary local filesystem paths. If a repo is involved, it must be represented as an explicit repository workspace target, not as a path accidentally shared by containers.

## 11. Distributed Artifact Flow

Normal execution flow:

1. Driver discovers resources and conversation context.
2. Driver uploads or references input artifacts in observer.
3. Driver compiles and stores `TaskContract`.
4. Driver routes directly to slave or submits to master.
5. Master stores `PlanRequest` and `PlanResponse`.
6. Slaves fetch inputs from observer.
7. Slaves write outputs to local scratch space during execution.
8. Slaves upload durable outputs to observer.
9. Driver resolves final user response from task status and artifact metadata.
10. Follow-up turns refer to observer artifacts by conversation/task linkage.

No step requires master, driver, and slave to share a local directory for business data.

## 12. Container Development and E2E Environment

E2E tests should run multiple long-lived containers:

```text
agentserver
observer
master
driver
slave-a
slave-b
```

Each agent container needs Claude Code:

```bash
npm install -g @anthropic-ai/claude-code
export ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn
export ANTHROPIC_API_KEY=<your-api-key>
```

Each container should skip Claude onboarding:

```json
{
  "hasCompletedOnboarding": true
}
```

Agent containers should be long-lived so agentserver workspace authorization remains stable across process restarts. Each agent must have its own persisted config file because credentials are written back after registration:

```yaml
credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""
```

### Source Mounts

For development, all agent containers may bind-mount the same source directory:

```text
host repo -> master container
host repo -> driver container
host repo -> slave containers
```

This is only for editing, rebuilding, and restarting binaries. It must not be used as the distributed task data path.

The separation is:

| Shared source mount | Observer artifacts |
|---|---|
| Development and binary code changes | Business inputs, outputs, generated code, patches, logs |
| Visible to all development containers | Addressed by artifact IDs and metadata |
| May be modified when developing the system itself | Used for task continuity and audit |

## 13. Testing Requirements

Unit tests:

- `TaskContract` JSON schema validation.
- `ExecutionPolicy` defaulting and validation.
- Driver direct-first routing decisions.
- Master plan validation and rejection paths.
- Observer artifact metadata lookup by conversation and task.

Integration tests:

- Driver compiles a contract and routes directly to one slave.
- Driver compiles a contract and submits to master for fanout.
- Master rejects `build_mcp` when `allow_build_mcp=false`.
- Slave uploads generated code as observer artifact.
- Follow-up task retrieves the previous code artifact from observer.

Container E2E tests:

- master, driver, and slaves run in separate containers.
- No shared business-data volume exists between agent containers.
- Source directory is shared only as a development mount.
- Agent credentials persist across container restarts.
- A task can continue after a slave container restart because artifacts live in observer.
- Deleting local scratch directories does not break follow-up context.

## 14. Rollout Plan

Phase 1: Contract schema and observer persistence.

- Add Go structs for `TaskContract`, `ResourceSnapshot`, `ExecutionPolicy`, and artifact metadata.
- Store contracts and resource snapshots in observer.
- Keep existing natural-language prompt path available as compatibility mode.

Phase 2: Driver contract compiler.

- Add driver LLM prompt/schema to clarify and compile user intent.
- Add validation and direct-first routing.
- Submit contract JSON to master when orchestration is needed.

Phase 3: Master constrained planner.

- Update planner prompts to consume `TaskContract`.
- Add plan validation before dispatch.
- Reject missing fields and policy violations with structured errors.

Phase 4: Distributed artifact continuity.

- Make generated code and patches first-class observer artifacts.
- Let driver resolve follow-up references through observer metadata.
- Ensure local scratch directories are disposable.

Phase 5: Multi-container E2E.

- Add development runtime image with Claude Code installed.
- Add compose setup for long-lived master, driver, and slave containers.
- Add E2E tests that verify no business data depends on shared source mounts.

## 15. Open Questions

1. Should driver talk to master for a normalized `ResourceSnapshot`, or should it call agentserver discovery directly and normalize locally?
2. Should observer own conversation IDs, or should driver generate them and pass them through?
3. Should plan approval live entirely in observer, or should driver hold the user interaction state and observer only persist decisions?
4. Should repo mutation be implemented as patch artifacts first, before adding `repo_commit` mode?

## 16. Design Invariants

- Driver resolves ambiguity before execution.
- Master never receives raw user prose as its only source of truth.
- Master validates before dispatch.
- `allow_build_mcp=false` does not block real generated code artifacts.
- Durable task state lives in observer.
- Local files are scratch unless explicitly modeled as source-code development mounts or repository workspaces.
- Shared source mounts are allowed for developing the multi-agent binaries, not for passing task data.
