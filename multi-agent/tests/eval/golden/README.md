# tests/eval/golden — §F2 E4 task-family golden fixtures

Five user-promoted-capability task families used by E4 (three-stage
experiment per `docs/intermediate/11_loom_user_promoted_capability_lifecycle.md`
§4) and by B3 `skills/mcp-acceptance --cases`. Each family ships:

- `first-task/` — Stage A trigger (one-shot script, no MCP yet);
- `reuse-1/`, `reuse-2/`, `reuse-3/` — Stage C reuse instances of the same
  capability under different inputs;
- `acceptance/cases.jsonl` — Stage B固化 semantic gate (≥5 cases per family,
  covering happy / edge / error / boundary / negative);
- `README.md` — family-level prose + invocation template.

## Cross-family conventions (WT-1 / B3 contract)

The following hold for every family in this directory. The schema test
(`golden_schema_test.go`) enforces what it can; the rest is contract that
B3's `--cases` runner is expected to honor.

1. **One tool per family.** `acceptance/cases.jsonl` only ever names one
   `tool` value, and every `spec.yaml` lists that tool under
   `capability_requirements.tools`. Drift on either side fails
   `TestAcceptanceToolMatchesSpec`.

2. **Path resolution.** Path strings inside `cases.jsonl` (`input.path`,
   `input.policy_path`, …) are resolved relative to the `multi-agent/`
   module root — i.e., the CWD `go test` runs in, and the CWD the
   B3 `mcp-acceptance --cases` driver is expected to invoke MCPs from.
   `TestAcceptanceCaseInputsAreSelfContained` verifies in-repo paths
   resolve; `/tmp/...` paths in negative cases are deliberately absent.

3. **Tool input surface is closed.** Each tool accepts only the field
   names registered in `toolAllowedFields` in the schema test. Adding a
   new optional argument requires updating both the tool's spec/README
   (so B3 knows about it) and the test allowlist (so drift is detected).
   This is the guard that catches "case adds `base_url` but the MCP
   wrapper has no `base_url` parameter" silent inconsistencies.

4. **`expected_error` matching semantics.** B3 SHOULD treat the
   `expected_error` string as a **case-sensitive substring** match
   against the MCP's error message. Exact / regex matching is out of
   scope for the v3 paper; substring is permissive enough that small
   wording differences across runtimes (Pillow vs PIL, pandas vs polars)
   don't break the gate. Cases written here keep the substring short and
   distinctive (e.g. `"file not found"`, `"invalid PNG header"`).

5. **`expected` deep-equals.** When a case carries `expected` (not
   `expected_error`), B3 SHOULD do a deep-equal JSON comparison against
   the tool's structured response, with two carve-outs that families
   document in their own READMEs: float tolerance (csv-profiler `mean`)
   and `required_keys`-only comparison (api-wrapper `/healthz`).

6. **Per-task oracle.** Reuse / first-task runs are scored against
   `expected/<artifact>.json` in the task directory, not against
   `cases.jsonl`. The two oracles overlap on intent (does the tool work?)
   but only the per-task oracle gates Stage A vs Stage C task success.

## Schema test

`golden_schema_test.go` is hermetic — no network, no MCP, no compose. It
walks the tree and enforces the bullets above plus the layout / spec
shape documented in §F1. Run with:

```
go test ./tests/eval/golden/... -v
```

Adding a sixth family means: (a) creating the directory, (b) extending
`expectedFamilies` in the test, (c) extending `toolAllowedFields` with
the new tool's input surface, (d) writing the family README.
