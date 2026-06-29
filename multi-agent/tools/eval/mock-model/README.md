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
| `POST` | `/v1/chat/completions`  | `Bearer ...` | OpenAI subset; **no streaming**, `created: 0` |
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

## Tests

```bash
cd multi-agent/tools/eval/mock-model
PYTHONPATH=. python -m pytest -q
```

Tests run the FastAPI app in-process via `httpx.ASGITransport` — no real
uvicorn subprocess.

## Out of scope

- Streaming (`stream: true`) — §D6a does not require it; real experiments use
  the real gateway.
- Token-accurate billing — `usage.{prompt,completion}_tokens` use a plain
  `len(.split())` and are stable, not OpenAI-tokenizer-exact.
