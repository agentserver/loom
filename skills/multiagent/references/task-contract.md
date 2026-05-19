# Task Contract

Contracts make driver-side clarification and routing deterministic. Always apply defaults before validation.

## JSON Shape

```json
{
  "version": 1,
  "conversation_id": "conv-2026-05-19-001",
  "intent": {
    "goal": "Business-level task goal",
    "business_context": "Why this matters",
    "success_criteria": ["Concrete completion condition"],
    "non_goals": ["Optional explicit exclusions"]
  },
  "data_contract": {
    "read_artifacts": [
      {
        "artifact_id": "art_123",
        "kind": "csv",
        "name": "input.csv",
        "sha256": "optional"
      }
    ],
    "write_targets": [
      {
        "type": "artifact",
        "kind": "document",
        "name": "result.md"
      }
    ]
  },
  "execution_policy": {
    "routing": "direct_first",
    "allow_code_artifacts": true,
    "code_persistence": "observer_artifact_store",
    "expose_code_to_user": "on_request",
    "write_mode": "artifact_only",
    "max_dag_nodes": 6,
    "max_depth": 3,
    "max_concurrency": 3,
    "require_plan_approval": false,
    "require_user_approval_for_repo_writes": false,
    "allowed_targets": []
  },
  "capability_requirements": {
    "skills": ["chat"],
    "tools": ["server/tool_name"],
    "resources": {"tags": ["python3"]}
  }
}
```

## Defaults

- `version`: `1`
- `execution_policy.routing`: `direct_first`
- `allow_code_artifacts`: `true`
- `code_persistence`: `observer_artifact_store`
- `expose_code_to_user`: `on_request`
- `write_mode`: `artifact_only`
- `max_dag_nodes`: `6`
- `max_depth`: `3`
- `max_concurrency`: `3`

## Validation Rules

- `conversation_id`, `intent.goal`, and `intent.success_criteria` are required.
- At least one `data_contract.write_targets` entry is required.
- `write_targets[].type` currently supports `artifact`.
- `write_targets[].kind:"code"` requires `allow_code_artifacts:true`.
- Use `routing: "direct_first"` for driver-first operation.
- `code_persistence` must be `observer_artifact_store`.
- `expose_code_to_user` must be `on_request`.
- `write_mode` supports `artifact_only`, `patch`, and `repo_commit`.
- `repo_commit` requires `require_user_approval_for_repo_writes:true`.
- Numeric DAG limits must be at least 1.

## Envelope Format

`submit_contract_task` wraps the contract around the executable prompt:

```text
<TASK_CONTRACT version=1>
{ ...contract JSON... }
</TASK_CONTRACT>

Prompt body for the selected agent.
```

Agents decode the envelope before execution. The body defaults to `intent.goal` when no explicit prompt is supplied.
