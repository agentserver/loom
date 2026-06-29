# csv-profiler — E4 task family

> Tool produced by user-promoted固化: `csv_profile`

A capability family where each task asks for "basic statistics on a CSV":
row count, column count, per-column dtype, null counts, and (for numeric
columns) mean/min/max. The first task triggers an ad-hoc script
(`run_slave_bash`); on its second appearance the user/driver promotes it
into a registered MCP tool, and reuse-1/2/3 should hit that tool via
`RegistryLookupHitRate`.

## Layout

| Path | Role |
|---|---|
| `first-task/`              | Stage A trigger — the first time this family is seen |
| `reuse-1/`, `reuse-2/`, `reuse-3/` | Stage C — should hit the固化 MCP |
| `acceptance/cases.jsonl`   | Stage B固化 semantic gate fed to `skills/mcp-acceptance --cases` |

## Running once the MCP exists

```bash
# Stage B固化 gate — must pass before register_slave_mcp is accepted.
skills/mcp-acceptance --tool csv_profile \
  --cases tests/eval/golden/csv-profiler/acceptance/cases.jsonl

# Stage A / C task runs (via eval-runner once §D3 lands):
tools/eval/runner --spec tests/eval/golden/csv-profiler/first-task/spec.yaml
tools/eval/runner --spec tests/eval/golden/csv-profiler/reuse-1/spec.yaml
```

The oracle compares the tool's output against `expected/profile.json` in
each task dir (deep-equal modulo float tolerance on mean).
