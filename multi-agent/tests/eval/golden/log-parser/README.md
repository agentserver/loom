# log-parser — E4 task family

> Tool produced by user-promoted固化: `parse_access_log`

Parse a web-server access log into structured records: timestamp, method,
path, status, bytes, optional referrer/user-agent. Stage A handles
nginx-style combined logs via a one-shot script; Stage B固化 wraps the
parsing into a registered MCP tool keyed on a `format` discriminator so
reuse-N can swap between nginx-combined, apache-common, and JSON-lines
without re-prompting.

## Layout

| Path | Role |
|---|---|
| `first-task/`              | First nginx access log; triggers ad-hoc parsing |
| `reuse-1/`                 | nginx-combined again, different file |
| `reuse-2/`                 | apache-common (different format, same family) |
| `reuse-3/`                 | JSON-lines structured log (different format again) |
| `acceptance/cases.jsonl`   | Stage B固化 gate |

## Running once the MCP exists

```bash
skills/mcp-acceptance --tool parse_access_log \
  --cases tests/eval/golden/log-parser/acceptance/cases.jsonl

tools/eval/runner --spec tests/eval/golden/log-parser/first-task/spec.yaml
```

Oracle compares the parsed record list against `expected/records.json`
(order-significant).
