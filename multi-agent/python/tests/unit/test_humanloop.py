"""Unit tests for humanloop default handler + sync-loop helper."""
import io
import sys

import pytest

from loom.humanloop import default_terminal_handler, _ttyish
from loom.tasks import Question
from loom.errors import NoInteractiveHandler


def test_default_handler_ask_user_no_options(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    monkeypatch.setattr("builtins.input", lambda prompt: "blue")
    q = Question(kind="ask_user", question="pick a color")
    assert default_terminal_handler(q) == "blue"


def test_default_handler_ask_user_with_options(monkeypatch, capsys):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    captured_prompt = {}

    def fake_input(prompt):
        captured_prompt["p"] = prompt
        return "red"

    monkeypatch.setattr("builtins.input", fake_input)
    q = Question(kind="ask_user", question="pick", options=["red", "blue"])
    assert default_terminal_handler(q) == "red"
    assert "red/blue" in captured_prompt["p"]


def test_default_handler_request_permission_y(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    monkeypatch.setattr("builtins.input", lambda prompt: "y")
    q = Question(kind="request_permission", intent="run_bash", target="rm -rf /tmp/x")
    assert default_terminal_handler(q) == "approve"


def test_default_handler_request_permission_n(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    monkeypatch.setattr("builtins.input", lambda prompt: "")
    q = Question(kind="request_permission", intent="run_bash", target="rm -rf /tmp/x")
    assert default_terminal_handler(q) == "deny"


def test_default_handler_no_tty(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: False)
    q = Question(kind="ask_user", question="who")
    with pytest.raises(NoInteractiveHandler):
        default_terminal_handler(q)
