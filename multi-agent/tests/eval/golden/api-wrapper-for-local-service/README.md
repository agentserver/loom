# api-wrapper-for-local-service — E4 task family

> Tool produced by user-promoted固化: `local_echo_call`

Wrap a local HTTP service (`http://localhost:8080/...`) into an MCP tool
that exposes one method per endpoint. The fixture service is the canonical
"echo" toy (see `_shared/openapi.yaml` — the four endpoints exercised
across this family are `/echo`, `/echo/json`, `/echo/headers`, and
`/healthz`). Stage A: drive curl one-shot per request; Stage B固化: wrap
all four endpoints under one MCP with a `method` discriminator; Stage C
reuse: call the other three endpoints without re-prompting the model for
URL/header plumbing.

## Layout

| Path | Role |
|---|---|
| `first-task/`              | Call `POST /echo` (string body) |
| `reuse-1/`                 | Call `POST /echo/json` (JSON body) |
| `reuse-2/`                 | Call `GET /echo/headers` (header echo) |
| `reuse-3/`                 | Call `GET /healthz` (no body) |
| `_shared/openapi.yaml`     | OpenAPI fragment for the toy service |
| `acceptance/cases.jsonl`   | Stage B固化 gate |

## Running once the MCP exists

```bash
# Start the toy echo service first (Stage A also needs this running):
#   python tests/fixtures/echo_service.py  --port 8080   # not part of this worktree

skills/mcp-acceptance --tool local_echo_call \
  --cases tests/eval/golden/api-wrapper-for-local-service/acceptance/cases.jsonl

tools/eval/runner --spec tests/eval/golden/api-wrapper-for-local-service/first-task/spec.yaml
```

Oracle compares the MCP tool's structured response against
`expected/response.json` per task; for `/healthz` the comparison ignores
fields not in `expected.required_keys`.
