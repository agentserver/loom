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


@pytest.fixture
def anyio_backend() -> str:
    return "asyncio"
