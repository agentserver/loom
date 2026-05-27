"""Human-in-the-loop helpers.

default_terminal_handler is the no-arg fallback for FutureTask.expect_or_ask:
reads stdin if it's a TTY, else raises NoInteractiveHandler to force the user
to supply a non-default handler when running unattended.
"""
from __future__ import annotations

import sys

from .errors import NoInteractiveHandler
from .tasks import Question


def _ttyish() -> bool:
    """True iff stdin AND stdout are both TTYs (so input/print make sense)."""
    return (
        hasattr(sys.stdin, "isatty") and sys.stdin.isatty()
        and hasattr(sys.stdout, "isatty") and sys.stdout.isatty()
    )


def default_terminal_handler(q: Question) -> str:
    """Default handler for FutureTask.expect_or_ask() when no handler is passed.

    Reads stdin if both stdin/stdout are TTYs; raises NoInteractiveHandler otherwise.
    """
    if not _ttyish():
        raise NoInteractiveHandler(
            "expect_or_ask was called without a handler but stdin is not a TTY. "
            "Pass a handler explicitly: .expect_or_ask(my_handler) where my_handler "
            "takes a Question and returns a string."
        )
    if q.kind == "ask_user":
        if q.options:
            prompt = f"{q.question} ({'/'.join(q.options)}): "
        else:
            prompt = f"{q.question}: "
        return input(prompt).strip()
    if q.kind == "request_permission":
        reason = f" ({q.reason})" if q.reason else ""
        prompt = f"Approve {q.intent} on {q.target}{reason}? (y/N): "
        return "approve" if input(prompt).strip().lower() == "y" else "deny"
    raise NoInteractiveHandler(f"unknown question kind: {q.kind}")
