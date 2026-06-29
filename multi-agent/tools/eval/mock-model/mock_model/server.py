"""FastAPI app + uvicorn entrypoint for the deterministic mock model.

⚠️ For deterministic eval only. Not a real model.
"""

from __future__ import annotations

import argparse
import json
import os
from typing import AsyncIterator, Optional

from fastapi import FastAPI, Header, HTTPException
from fastapi.responses import StreamingResponse

from .deterministic import DEFAULT_CONTENT_PREFIX, deterministic_digest
from .models import (
    MOCK_MODEL_IDS,
    ChatCompletionRequest,
    ChatCompletionResponse,
    Choice,
    ChoiceMessage,
    ModelInfo,
    ModelList,
    Usage,
)

# `--content-prefix` flows in through this env var so it's picked up by the
# FastAPI handlers without threading state through every closure.
_CONTENT_PREFIX_ENV = "MOCK_MODEL_CONTENT_PREFIX"


def _prefix() -> str:
    return os.environ.get(_CONTENT_PREFIX_ENV) or DEFAULT_CONTENT_PREFIX


def _require_bearer(authorization: Optional[str]) -> None:
    """Both `experimental_bearer_token` and `env_key` send `Authorization: Bearer ...`.

    We accept any non-empty bearer — this server is local-only and the eval
    harness wants determinism, not access control.
    """
    if not authorization or not authorization.lower().startswith("bearer "):
        raise HTTPException(status_code=401, detail="missing bearer token")
    token = authorization.split(" ", 1)[1].strip()
    if not token:
        raise HTTPException(status_code=401, detail="empty bearer token")


def _count_tokens(text: str) -> int:
    """Cheap, stable token approximation — `len(text.split())`.

    Not OpenAI-tokenizer-accurate; §D6a explicitly does not require that.
    """
    return len(text.split())


def _build_app() -> FastAPI:
    app = FastAPI(title="mock-model", version="0.1.0")

    @app.get("/healthz")
    async def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.get("/v1/models", response_model=ModelList)
    async def list_models(
        authorization: Optional[str] = Header(default=None),
    ) -> ModelList:
        _require_bearer(authorization)
        return ModelList(data=[ModelInfo(id=mid) for mid in MOCK_MODEL_IDS])

    @app.post("/v1/chat/completions")
    async def chat_completions(
        req: ChatCompletionRequest,
        authorization: Optional[str] = Header(default=None),
    ):
        _require_bearer(authorization)
        messages_payload = [m.model_dump() for m in req.messages]
        # Compute the digest once and derive both content and id from it.
        # Avoids re-reading the prefix env var (which would race if changed
        # between the two reads) and the prior `content[len(prefix)+1:-1]`
        # slice, which was fragile if the prefix happened to contain `[`.
        hash_slice = deterministic_digest(req.model, messages_payload)
        content = f"{_prefix()}[{hash_slice}]"
        prompt_text = "\n".join(m.content for m in req.messages)
        prompt_tokens = _count_tokens(prompt_text)
        completion_tokens = _count_tokens(content)

        if req.stream:
            # Codex CLI with `wire_api = "chat"` hard-codes `stream: true`
            # (openai/codex#3513). We emit a minimal but spec-shaped SSE
            # stream: role chunk, content chunk, finish chunk, `[DONE]`.
            # The concatenated content equals the non-stream `content` body
            # so reproducibility holds across stream modes.
            return StreamingResponse(
                _sse_chunks(
                    chat_id=f"chatcmpl-mock-{hash_slice}",
                    model=req.model,
                    content=content,
                ),
                media_type="text/event-stream",
            )

        return ChatCompletionResponse(
            id=f"chatcmpl-mock-{hash_slice}",
            model=req.model,
            choices=[
                Choice(
                    index=0,
                    message=ChoiceMessage(content=content),
                    finish_reason="stop",
                )
            ],
            usage=Usage(
                prompt_tokens=prompt_tokens,
                completion_tokens=completion_tokens,
                total_tokens=prompt_tokens + completion_tokens,
            ),
        )

    return app


def _sse_chunk(payload: dict) -> bytes:
    """Format one OpenAI-style SSE event: `data: <compact-json>\\n\\n`.

    Compact JSON (sort_keys + no whitespace) so the byte stream itself is
    deterministic — concatenating two reproducibility-run streams should
    diff cleanly.
    """
    line = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    return f"data: {line}\n\n".encode("utf-8")


async def _sse_chunks(
    *, chat_id: str, model: str, content: str
) -> AsyncIterator[bytes]:
    """Emit role → content → finish → `[DONE]` chunks.

    Each chunk has `object: "chat.completion.chunk"` and `created: 0` for the
    same no-wall-clock-leak reason the non-stream path uses.
    """
    base = {
        "id": chat_id,
        "object": "chat.completion.chunk",
        "created": 0,
        "model": model,
    }
    # 1. Role delta
    yield _sse_chunk(
        {**base, "choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}]}
    )
    # 2. Content delta (single chunk — keeps the stream deterministic and tiny)
    yield _sse_chunk(
        {**base, "choices": [{"index": 0, "delta": {"content": content}, "finish_reason": None}]}
    )
    # 3. Finish delta
    yield _sse_chunk(
        {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]}
    )
    # 4. Sentinel
    yield b"data: [DONE]\n\n"


app = _build_app()


def _cli(argv: Optional[list[str]] = None) -> None:
    parser = argparse.ArgumentParser(
        prog="mock-model",
        description="Deterministic OpenAI-compatible mock model server.",
    )
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=53453)
    parser.add_argument(
        "--seed",
        type=int,
        default=None,
        help="Accepted for forward-compat; no randomness to seed (content is pure).",
    )
    parser.add_argument(
        "--content-prefix",
        default=DEFAULT_CONTENT_PREFIX,
        help=f"Prefix wrapper around the hash slice (default: {DEFAULT_CONTENT_PREFIX}).",
    )
    args = parser.parse_args(argv)

    os.environ[_CONTENT_PREFIX_ENV] = args.content_prefix

    import uvicorn  # local import — keeps `pytest` fast

    uvicorn.run(app, host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":  # `python -m mock_model.server`
    _cli()
