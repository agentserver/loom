"""RED tests for mock_model.server — OpenAI-compatible surface.

Uses httpx.ASGITransport against the FastAPI app in-process (no real uvicorn).
"""

from __future__ import annotations

import httpx
import pytest

from mock_model.server import app


@pytest.fixture
def client() -> httpx.AsyncClient:
    transport = httpx.ASGITransport(app=app)
    return httpx.AsyncClient(transport=transport, base_url="http://mock")


@pytest.mark.anyio
async def test_chat_completions_returns_openai_shape(client: httpx.AsyncClient) -> None:
    """Response must look like an OpenAI chat.completion object."""
    async with client:
        resp = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": "Bearer anything"},
            json={
                "model": "mock-glm-5.2",
                "messages": [{"role": "user", "content": "hello"}],
                "temperature": 0,
                "stream": False,
            },
        )

    assert resp.status_code == 200
    body = resp.json()

    # Top-level OpenAI shape
    assert body["object"] == "chat.completion"
    assert body["model"] == "mock-glm-5.2"
    assert body["created"] == 0, "created must be fixed 0; no wall-clock leak"
    assert body["id"].startswith("chatcmpl-mock-")

    # choices[0]
    assert isinstance(body["choices"], list) and len(body["choices"]) == 1
    choice = body["choices"][0]
    assert choice["index"] == 0
    assert choice["finish_reason"] == "stop"
    assert choice["message"]["role"] == "assistant"
    content = choice["message"]["content"]
    assert content.startswith("MOCK[") and content.endswith("]")

    # usage
    usage = body["usage"]
    assert usage["prompt_tokens"] >= 0
    assert usage["completion_tokens"] >= 0
    assert usage["total_tokens"] == usage["prompt_tokens"] + usage["completion_tokens"]


@pytest.mark.anyio
async def test_chat_completions_is_deterministic_across_calls(
    client: httpx.AsyncClient,
) -> None:
    """Two identical requests must return byte-identical content."""
    payload = {
        "model": "mock-glm-5.2",
        "messages": [{"role": "user", "content": "ping"}],
        "temperature": 0,
        "stream": False,
    }
    async with client:
        a = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": "Bearer x"},
            json=payload,
        )
        b = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": "Bearer y"},  # auth value must not affect content
            json=payload,
        )
    assert a.json()["choices"][0]["message"]["content"] == b.json()["choices"][0]["message"]["content"]
    assert a.json()["id"] == b.json()["id"]


@pytest.mark.anyio
async def test_chat_completions_with_bearer_token_accepts(
    client: httpx.AsyncClient,
) -> None:
    """Any non-empty Bearer token accepted (covers both experimental_bearer_token + env_key)."""
    async with client:
        resp = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": "Bearer mock-secret-abc"},
            json={
                "model": "mock-gpt-5.5",
                "messages": [{"role": "user", "content": "hi"}],
            },
        )
    assert resp.status_code == 200


@pytest.mark.anyio
async def test_chat_completions_missing_auth_rejected(
    client: httpx.AsyncClient,
) -> None:
    """No Authorization header → 401."""
    async with client:
        resp = await client.post(
            "/v1/chat/completions",
            json={
                "model": "mock-glm-5.2",
                "messages": [{"role": "user", "content": "hi"}],
            },
        )
    assert resp.status_code == 401


@pytest.mark.anyio
async def test_models_endpoint_returns_three(client: httpx.AsyncClient) -> None:
    """/v1/models must list the three fixed mock models."""
    async with client:
        resp = await client.get(
            "/v1/models",
            headers={"Authorization": "Bearer x"},
        )
    assert resp.status_code == 200
    body = resp.json()
    assert body["object"] == "list"
    ids = sorted(m["id"] for m in body["data"])
    assert ids == sorted(["mock-glm-5.2", "mock-gpt-5.5", "mock-claude-opus-4-8"])
    for m in body["data"]:
        assert m["object"] == "model"


@pytest.mark.anyio
async def test_healthz(client: httpx.AsyncClient) -> None:
    """/healthz returns 200 with a small status body and no auth required."""
    async with client:
        resp = await client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


@pytest.mark.anyio
async def test_chat_completions_stream_true_returns_sse(
    client: httpx.AsyncClient,
) -> None:
    """`stream: true` must return text/event-stream chunks ending with `data: [DONE]`.

    Codex CLI with `wire_api = "chat"` hard-codes `stream: true` (see
    openai/codex#3513) and will not parse a non-stream JSON response. Without
    this, the README's "wire into codex" example silently breaks.
    """
    import json

    async with client:
        async with client.stream(
            "POST",
            "/v1/chat/completions",
            headers={"Authorization": "Bearer x"},
            json={
                "model": "mock-glm-5.2",
                "messages": [{"role": "user", "content": "hello"}],
                "stream": True,
            },
        ) as resp:
            assert resp.status_code == 200
            ctype = resp.headers.get("content-type", "")
            assert "text/event-stream" in ctype, ctype
            body = b""
            async for chunk in resp.aiter_bytes():
                body += chunk

    text = body.decode("utf-8")
    # Final sentinel
    assert text.rstrip().endswith("data: [DONE]")
    # At least one delta chunk with shape {"object":"chat.completion.chunk", ...}
    data_lines = [
        line[len("data: "):]
        for line in text.splitlines()
        if line.startswith("data: ") and line != "data: [DONE]"
    ]
    assert data_lines, "expected at least one streamed chunk"
    chunks = [json.loads(d) for d in data_lines]
    # All chunks share the same id and model
    assert all(c["id"].startswith("chatcmpl-mock-") for c in chunks)
    assert all(c["object"] == "chat.completion.chunk" for c in chunks)
    assert all(c["model"] == "mock-glm-5.2" for c in chunks)
    # The concatenated content delta must equal the same deterministic body the
    # non-stream path returns — so reproducibility holds across stream modes.
    content = "".join(
        c["choices"][0]["delta"].get("content", "") for c in chunks
    )
    assert content.startswith("MOCK[") and content.endswith("]")
    # And one chunk should carry a finish_reason
    assert any(c["choices"][0].get("finish_reason") == "stop" for c in chunks)


@pytest.mark.anyio
async def test_chat_completions_stream_matches_nonstream_content(
    client: httpx.AsyncClient,
) -> None:
    """Stream and non-stream paths must produce byte-identical content for the same input.

    Otherwise a reproducibility re-run that flips stream-mode would diverge.
    """
    import json

    payload_nonstream = {
        "model": "mock-glm-5.2",
        "messages": [{"role": "user", "content": "ping"}],
        "stream": False,
    }
    payload_stream = {**payload_nonstream, "stream": True}

    async with client:
        a = await client.post(
            "/v1/chat/completions",
            headers={"Authorization": "Bearer x"},
            json=payload_nonstream,
        )
        async with client.stream(
            "POST",
            "/v1/chat/completions",
            headers={"Authorization": "Bearer x"},
            json=payload_stream,
        ) as resp:
            body = b""
            async for chunk in resp.aiter_bytes():
                body += chunk

    non_stream_content = a.json()["choices"][0]["message"]["content"]
    data_lines = [
        line[len("data: "):]
        for line in body.decode("utf-8").splitlines()
        if line.startswith("data: ") and line != "data: [DONE]"
    ]
    streamed_content = "".join(
        json.loads(d)["choices"][0]["delta"].get("content", "")
        for d in data_lines
    )
    assert streamed_content == non_stream_content


@pytest.fixture
def anyio_backend() -> str:
    return "asyncio"
