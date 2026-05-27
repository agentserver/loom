"""e2e case 4: request_permission marker distinct from ask_user."""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case4_request_permission(slave_local_prod):
    captured: dict = {}

    def handler(q: loom.Question) -> str:
        captured["kind"] = q.kind
        captured["intent"] = q.intent
        captured["target"] = q.target
        return "denied"

    with loom.workflow(goal="request_permission round-trip") as wf:
        res = (
            wf.chat(
                'Call the request_permission tool with intent="run_bash" '
                'target="rm -rf /tmp/some_dir" reason="cleanup". '
                "When you receive the user's answer in your next user turn, "
                "reply with exactly that answer verbatim and stop.",
                target=slave_local_prod,
                timeout_sec=180,
            )
            .expect_or_ask(handler)
            .wait()
        )
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert captured["kind"] == "request_permission"
    assert captured["intent"] == "run_bash"
    assert captured["target"] == "rm -rf /tmp/some_dir"
