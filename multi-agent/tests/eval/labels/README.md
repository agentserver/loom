# `tests/eval/labels/` — §F4 ground-truth annotations

Per §F4 of [`docs/intermediate/12_loom_development_tasks_for_v3.md`][12], this
directory holds the **ground-truth labels** that feed three v3 evaluation
metrics:

| Metric | Label field consumed |
|---|---|
| `RoutingAccuracy` | `ground_truth_context` (singular — one correct target) |
| `CapabilityRecall` | `context_ground_truth.required_capabilities` |
| `CapabilityPrecision` | `context_ground_truth.required_capabilities` ∪ `forbidden_capabilities` |

> 13 号文档（`docs/intermediate/13_workload_spec.md`，待补）会把本目录的
> `schema/` 与字段语义当作权威引用：workload spec 的 `required_contexts[].role`
> 必须出现在某个 task 的 `ground_truth_context.context_id`，否则该 task
> 无法被打分。

## Layout

```
tests/eval/labels/
├── README.md
├── labels_schema_test.go            ← `go test ./tests/eval/labels/...`
├── json_strict.go
├── schema/
│   ├── labels_file.schema.json          ← wrapper: {task_id, gtc, cgt}
│   ├── ground_truth_context.schema.json
│   └── context_ground_truth.schema.json
├── workloads/                       ← §F1 5 main workloads
│   ├── cross-device-code-mod.labels.json
│   ├── remote-data-processing.labels.json
│   ├── windows-only-artifact.labels.json
│   ├── missing-parser-converter.labels.json
│   └── credential-bound-model.labels.json
└── families/                        ← §F2 5 task families × 4 tasks
    ├── csv-profiler/{first-task,reuse-1,reuse-2,reuse-3}.labels.json
    ├── log-parser/{first-task,reuse-1,reuse-2,reuse-3}.labels.json
    ├── refund-policy-checker/{first-task,reuse-1,reuse-2,reuse-3}.labels.json
    ├── image-metadata-extractor/{first-task,reuse-1,reuse-2,reuse-3}.labels.json
    └── api-wrapper/{first-task,reuse-1,reuse-2,reuse-3}.labels.json
```

## Per-task labels file

Each `*.labels.json` follows this shape; the test enforces it via the two
schemas under `schema/`.

```json
{
  "task_id": "csv-profiler-first-task",

  "ground_truth_context": {
    "agent_role": "driver | slave | sandbox",
    "context_id": "<kebab-case>",
    "rationale": "Why this is the unique correct context — read by reviewers."
  },

  "context_ground_truth": {
    "required_capabilities": [ /* see schema */ ],
    "forbidden_capabilities": [ /* optional */ ],
    "credential_aliases":   [ /* optional; opaque placeholder names */ ]
  }
}
```

Rules the test enforces (see `labels_schema_test.go`):

- Exactly **5** files under `workloads/` and **20** under `families/`.
- `task_id` is **globally unique** across both directories and a
  kebab-case slug (enforced by `schema/labels_file.schema.json`).
- The **wrapper** `{task_id, ground_truth_context, context_ground_truth}`
  and both sub-objects are schema-validated with
  `additionalProperties: false` at every level — stray keys at any
  level (including the outer wrapper) fail the build.
- `ground_truth_context.(agent_role, context_id)` must appear in the
  closed `knownContexts` allowlist in `labels_schema_test.go`,
  mirroring the §`context_id ↔ spec coupling` table below. Extending
  the namespace means editing the table **and** the allowlist in the
  same PR.
- Every `credential`-kind capability inside `required_capabilities`
  must be mirrored in the top-level `credential_aliases`, and vice
  versa. Asymmetric drift fails the build.
- Negative cases in `TestSchemasRejectBadInput`,
  `TestKnownContextRejectsTypoAndMismatch`, and
  `TestLabelsFileSchemaRejectsBadWrapper` lock the rules above
  against regression.

## Capability vocabulary (`context_ground_truth.required_capabilities`)

| `kind` | Required props | Optional props | Used for |
|---|---|---|---|
| `tool` | `name` | `min_version` (semver-ish) | CLI / library presence |
| `platform` | `os` ∈ {linux,windows,darwin} | `arch` ∈ {amd64,arm64} | OS / arch gating |
| `file` | `kind_detail` ∈ {repo,dataset,fixture,config}, `path_pattern` | — | data locality |
| `network` | `reach` ∈ {internet,intranet,loopback-only,none} | — | reachability constraints |
| `credential` | `alias` (snake_case) | — | credential brokerage |

`credential_aliases` is a top-level mirror of any `credential` capability and
exists so credential analysis can be done without walking the capability
tree. **Never store a real token here.** Use stable placeholder names
(`openai_for_glm`, `external_api_user_token`).

## `context_id` ↔ spec coupling

`ground_truth_context.context_id` must match a `role` declared in the
corresponding workload's `spec.yaml > required_contexts[].role`. Owners
of those spec files (§F1 / §F2) are on separate worktrees, so this
worktree fixes the id namespace first:

| `context_id` used here | Expected platform |
|---|---|
| `driver-linux-laptop` | linux/amd64 driver host |
| `slave-linux-server` | linux/amd64 headless server slave |
| `slave-windows-desktop` | windows/amd64 desktop slave |
| `sandbox-cloud` | linux cloud sandbox slave |

If a downstream spec.yaml renames one of these, update **both** files in
the same PR — the schema test does not check spec.yaml because spec.yaml
is owned by a different worktree.

## Adding a new workload's labels

1. Create `workloads/<workload-id>.labels.json` (or a new family directory under
   `families/`).
2. Set `task_id` to a globally-unique kebab-case slug.
3. Fill `ground_truth_context` with a single role/context/rationale that
   *uniquely* satisfies the oracle. If two contexts could equally satisfy
   it, the workload is mis-designed for `RoutingAccuracy` and should be split.
   If your `(agent_role, context_id)` pair is not yet in the §`context_id
   ↔ spec coupling` table above, add it there **and** to `knownContexts`
   in `labels_schema_test.go` in the same PR — the closed-set check
   will otherwise reject it.
4. List **every** capability the target context must provide. Be exhaustive
   for `tool`/`platform`/`network`; for `file` use the same `path_pattern`
   the workload's oracle reads. If any capability has `kind: credential`,
   list its alias in `credential_aliases` too — the mirror check is
   bidirectional and fails the build either way.
5. Add `forbidden_capabilities` only when a *plausible wrong* context exists
   that you must rule out (e.g. internet egress for an air-gapped reuse).
6. Run `go test ./tests/eval/labels/...` — schema violations, duplicate
   `task_id`s, unknown `(agent_role, context_id)` pairs, and credential
   alias drift all fail the build.
7. If you added a new workload, also bump the expected counts in the test:
   `expectedWorkloadLabels` / `expectedFamilyLabels`.

## Cross-references

- 08 号 `docs/intermediate/08_evaluation_plan_v3.md` — §Data collection schema
  defines the `ground_truth_context` / `context_ground_truth` columns that
  the eval-runner exports (§D1).
- 12 号 `docs/intermediate/12_loom_development_tasks_for_v3.md` §F4 — the
  task this directory implements.
- 13 号 `docs/intermediate/13_workload_spec.md` (WT-0-workload-spec-doc, TBD)
  — will reference `schema/` and the field semantics above when defining
  the spec.yaml shape.

[12]: /root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md
