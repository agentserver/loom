"""Pydantic schemas for the OpenAI-compatible subset we expose."""

from __future__ import annotations

from typing import Any, List, Literal, Optional

from pydantic import BaseModel, Field


class ChatMessage(BaseModel):
    role: str
    content: str


class ChatCompletionRequest(BaseModel):
    model: str
    messages: List[ChatMessage]
    temperature: Optional[float] = 0.0
    stream: Optional[bool] = False
    # Tolerate (and ignore) other fields callers may send.
    model_config = {"extra": "allow"}


class ChoiceMessage(BaseModel):
    role: Literal["assistant"] = "assistant"
    content: str


class Choice(BaseModel):
    index: int = 0
    message: ChoiceMessage
    finish_reason: Literal["stop"] = "stop"


class Usage(BaseModel):
    prompt_tokens: int
    completion_tokens: int
    total_tokens: int


class ChatCompletionResponse(BaseModel):
    id: str
    object: Literal["chat.completion"] = "chat.completion"
    created: int = 0  # fixed; no wall-clock leak
    model: str
    choices: List[Choice]
    usage: Usage


class ModelInfo(BaseModel):
    id: str
    object: Literal["model"] = "model"
    created: int = 0
    owned_by: str = "mock-model"


class ModelList(BaseModel):
    object: Literal["list"] = "list"
    data: List[ModelInfo]


MOCK_MODEL_IDS: tuple[str, ...] = (
    "mock-glm-5.2",
    "mock-gpt-5.5",
    "mock-claude-opus-4-8",
)
