"""e2e case 1: happy chat with humanloop MCP injected but ask_user not called.

Equivalent to multi-agent/tests/humanloop_e2e/scripts/case1_happy.py but written
via loom-py to prove the library covers the same shape in <=15 lines.
"""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case1_happy(slave_local_prod):
    with loom.workflow(goal="say HELLO") as wf:
        res = wf.chat(
            "Reply with the single word HELLO and stop. Do not call any tool.",
            target=slave_local_prod,
            timeout_sec=180,
        ).wait()
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert "HELLO" in res.output.upper(), f"output: {res.output!r}"
