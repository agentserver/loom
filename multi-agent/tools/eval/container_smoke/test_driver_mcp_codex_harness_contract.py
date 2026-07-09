#!/usr/bin/env python3
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parent
SCRIPT = ROOT / "run_heterogeneous_dates_driver_mcp_smoke.sh"


def test_harness_uses_codex_driver_with_driver_mcp() -> None:
    text = SCRIPT.read_text()

    assert "codex exec" in text
    assert "driver-agent serve-mcp" in text
    assert "[mcp_servers.driver]" in text
    assert re.search(r"driver-agent[^\n]+serve-mcp", text)
    assert "driver_prompt.md" in text


def test_harness_starts_real_workspace_infrastructure() -> None:
    text = SCRIPT.read_text()

    assert "agentserver-stub" in text
    assert "observer-server" in text
    assert "docker run" in text
    assert "slave-agent" in text
    assert "agent:" in text
    assert "kind: codex" in text
    assert "observer:" in text
    assert "telemetry_enabled: true" in text


def test_driver_prompt_coordinates_slaves_via_mcp_tools() -> None:
    text = SCRIPT.read_text()

    for tool in (
        "list_agents",
        "write_slave_file",
        "run_slave_bash",
        "read_slave_file",
    ):
        assert tool in text

    assert "smoke-data-slave" in text
    assert "smoke-compute-slave" in text
    assert "data-agent" not in text
    assert "solver-agent" not in text
    assert "verifier-agent" not in text


def test_harness_records_observer_usage_and_report_artifacts() -> None:
    text = SCRIPT.read_text()

    assert "agent_usage_summary.json" in text
    assert "observer_summary.json" in text
    assert "driver_mcp_tool_summary.json" in text
    assert "container_isolation.json" in text
    assert "oracle_result.json" in text
    assert "multi_agent_driver_mcp_report_cn.md" in text
    assert "model_input_tokens" in text
    assert "model_output_tokens" in text


def test_harness_does_not_bake_public_oracle_answer_into_driver_prompt() -> None:
    text = SCRIPT.read_text()
    prompt_match = re.search(
        r"""cat >"\$\{PROMPT_DIR\}/driver_prompt\.md" <<'PROMPT'(?P<prompt>.*?)^PROMPT$""",
        text,
        re.S | re.M,
    )
    assert prompt_match, "driver prompt heredoc missing"
    prompt = prompt_match.group("prompt")

    assert "11.428571" not in prompt
    assert "avg_temp.txt must contain only" in prompt
