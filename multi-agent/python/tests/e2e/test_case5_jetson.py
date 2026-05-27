"""e2e case 5: cross-node — chat with ask_user on the arm64 jetson.

Auto-skipped (not failed) when slave-jetson-prod is not in the workspace.
"""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case5_jetson():
    with loom.workflow(goal="cross-node ask_user on jetson") as wf:
        names = wf.list_slaves()
        if "slave-jetson-prod" not in names:
            pytest.skip(
                f"slave-jetson-prod not in current workspace; visible: {names}"
            )
        def handler(q):
            return "blue"
        res = (
            wf.chat(
                'Call ask_user with question="pick a color" '
                'options=["red","blue"]. When you receive the answer, '
                "reply with exactly that color word and stop.",
                target="slave-jetson-prod",
                timeout_sec=480,
            )
            .expect_or_ask(handler)
            .wait()
        )
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert "blue" in res.output.lower(), f"output: {res.output!r}"
