"""Spec §7 (e) — failure scrub mirrors internal/secretscrub."""
from __future__ import annotations

import pytest

from lib.failure_scrub import (
    REDACTED,
    failure_record,
    sanitize,
    scrub_and_tail,
    tail_lines,
)


def test_redact_sk_token():
    dirty = "ERROR: token=sk-abcdefghij0123456789xxxx bad"
    out = sanitize(dirty)
    assert "sk-abcdefghij0123456789xxxx" not in out
    assert REDACTED in out


def test_redact_sk_ant():
    dirty = "leak sk-ant-abcdef1234567890XYZ trailing"
    out = sanitize(dirty)
    assert "sk-ant" not in out
    assert REDACTED in out


def test_redact_bearer_header():
    dirty = "authorization: Bearer eyJabcdefghij0123456789 next"
    out = sanitize(dirty)
    # eyJ jwt AND bearer both fire — both should be scrubbed.
    assert "eyJabc" not in out
    assert REDACTED in out


def test_redact_github_pat():
    dirty = "ghp_ABCDEFGHIJKLMNOPQRSTUVWX secret"
    out = sanitize(dirty)
    assert "ghp_A" not in out


def test_tail_lines_default_cap():
    text = "\n".join(f"line-{i}" for i in range(500))
    tail = tail_lines(text)
    assert len(tail) == 200
    assert tail[0] == "line-300"
    assert tail[-1] == "line-499"


def test_tail_short_input_unchanged():
    text = "one\ntwo\nthree"
    assert tail_lines(text) == ["one", "two", "three"]


def test_scrub_and_tail_combined():
    lines = [f"line-{i}" for i in range(250)]
    lines[240] = "leak sk-abcdefghij0123456789xxxx here"
    text = "\n".join(lines)
    tail = scrub_and_tail(text)
    assert len(tail) == 200
    joined = "\n".join(tail)
    assert "sk-abc" not in joined
    assert REDACTED in joined


def test_failure_record_shape():
    rec = failure_record(
        run_id="r1", configuration="full_loom",
        workload_id="cross-device-code-mod",
        exit_code=137,
        stderr_text="oops sk-abcdefghij0123456789xxxx\n",
    )
    assert rec["run_id"] == "r1"
    assert rec["exit_code"] == 137
    assert isinstance(rec["tail_stderr"], list)
    assert len(rec["tail_stderr"]) <= 200
    assert not any("sk-abc" in ln for ln in rec["tail_stderr"])
