# mock-model

⚠️ **For deterministic eval only. Not a real model.**

A tiny OpenAI-compatible HTTP server that returns hash-stable replies. Used by:

- The loom **reproducibility package** (zero-network self-check)
- **Ablation runs** where we want to remove model randomness from a measurement

Normal experiments do **not** go through mock-model — they go through the codex
local gateway (see `docs/intermediate/12_loom_development_tasks_for_v3.md` §C3 / §J).

## Install

```bash
cd multi-agent/tools/eval/mock-model
pip install -e '.[dev]'
```

## Run

```bash
# Default port matches paper §D6a
python -m mock_model.server --port 53453

# Override host / port / content prefix
python -m mock_model.server --host 0.0.0.0 --port 53453 --content-prefix MOCK
```

The `--seed` flag is accepted for forward-compatibility but has no effect: the
content is already a pure function of `(model, messages)`, so there is no
randomness to seed.

## Endpoints

| Method | Path                    | Auth         | Notes                                          |
|-------:|-------------------------|--------------|------------------------------------------------|
| `POST` | `/v1/chat/completions`  | `Bearer ...` | OpenAI subset; `stream: true` returns SSE, `created: 0` |
| `GET`  | `/v1/models`            | `Bearer ...` | Fixed list of three mock model ids             |
| `GET`  | `/healthz`              | none         | Liveness probe                                 |

### Deterministic reply

For every request, the assistant content is:

```python
content = f"MOCK[{sha256((model + canonical_json(messages))).hexdigest()[:16]}]"
```

Same `(model, messages)` ⇒ byte-identical content, byte-identical `id`. The
`Authorization` value is checked for presence but does **not** influence the
hash, so different bearers see the same reply.

### Mock model ids

`mock-glm-5.2`, `mock-gpt-5.5`, `mock-claude-opus-4-8` — these are the only
three model ids `/v1/models` returns. `chat/completions` will happily echo any
model id back, but for parity with eval harnesses prefer the three above.

## Wiring into codex

Add to `~/.codex/config.toml` (or whichever `CODEX_HOME` the driver uses — see
the `driver-codex-home-divergence` memory for the gotcha):

```toml
[model_providers.modelserver]
name        = "Mock Model"
base_url    = "http://127.0.0.1:53453/v1"
wire_api    = "chat"
experimental_bearer_token = "mock"
# env_key path also works — both accept any non-empty bearer:
# env_key   = "MOCK_MODEL_API_KEY"
```

Then point a profile at it:

```toml
[profiles.repro]
model          = "mock-glm-5.2"
model_provider = "modelserver"
```

> **codex version note.** `wire_api = "chat"` was removed from `openai/codex`
> in `rust-v0.95.0` (4 Feb 2026, PR openai/codex#10157, discussion #7782);
> the last release supporting it is `rust-v0.94.0`. Newer codex CLIs reject
> `wire_api = "chat"` at config-parse time. For ablation runs against
> post-0.94 codex, drive the mock through eval-runner (which talks raw
> `/v1/chat/completions`) rather than through codex. While codex's chat
> path still existed it hard-coded `stream: true` (openai/codex#3513),
> which this server now emits as SSE.

## Tests

```bash
cd multi-agent/tools/eval/mock-model
PYTHONPATH=. python -m pytest -q
```

Tests run the FastAPI app in-process via `httpx.ASGITransport` — no real
uvicorn subprocess.

## Out of scope

- Streaming `usage` chunks — the SSE stream we emit covers role + content +
  finish + `[DONE]` so codex-style clients parse it, but we do **not** emit a
  trailing `usage` chunk (codex's chat path doesn't send
  `stream_options.include_usage = true` anyway; openai/codex#3513). Use the
  non-stream path for accurate token counts.
- Token-accurate billing — `usage.{prompt,completion}_tokens` use a plain
  `len(.split())` and are stable, not OpenAI-tokenizer-exact.
