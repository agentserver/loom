"""e2e case 2: single ask_user → resume → final.

Auto-answers "blue" via a non-interactive handler so the test doesn't hang
waiting for stdin.
"""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case2_ask_user(slave_local_prod):
    def handler(q: loom.Question) -> str:
        assert q.kind == "ask_user"
        assert "blue" in q.options or len(q.options) == 0
        return "blue"

    with loom.workflow(goal="ask_user round-trip") as wf:
        res = (
            wf.chat(
                'Call the ask_user tool with question="pick a color" '
                'options=["red","blue"]. When you receive the user\'s '
                "answer in your next user turn, reply with exactly that "
                "color word and stop.",
                target=slave_local_prod,
                timeout_sec=180,
            )
            .expect_or_ask(handler)
            .wait()
        )
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert "blue" in res.output.lower(), f"output: {res.output!r}"
