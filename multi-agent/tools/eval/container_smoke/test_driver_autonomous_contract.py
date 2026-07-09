#!/usr/bin/env python3
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parent
BASE_SCRIPT = ROOT / "run_heterogeneous_dates_driver_mcp_smoke.sh"
WRAPPER = ROOT / "run_heterogeneous_dates_driver_autonomous_smoke.sh"


def test_autonomous_wrapper_selects_autonomous_mode_and_output_root() -> None:
    text = WRAPPER.read_text()

    assert "DRIVER_PROMPT_MODE=autonomous" in text
    assert "multi_agent_driver_autonomous_smoke" in text
    assert "run_heterogeneous_dates_driver_mcp_smoke.sh" in text


def test_base_harness_has_autonomous_prompt_mode() -> None:
    text = BASE_SCRIPT.read_text()

    assert 'DRIVER_PROMPT_MODE="${DRIVER_PROMPT_MODE:-guided}"' in text
    assert 'case "$DRIVER_PROMPT_MODE" in' in text
    assert "autonomous)" in text
    assert "guided)" in text


def test_autonomous_prompt_does_not_prescribe_slave_skill_sequence() -> None:
    text = BASE_SCRIPT.read_text()
    match = re.search(
        r"""autonomous\)\s*cat >"\$\{PROMPT_DIR\}/driver_prompt\.md" <<'PROMPT'(?P<prompt>.*?)^PROMPT$""",
        text,
        re.S | re.M,
    )
    assert match, "autonomous prompt heredoc missing"
    prompt = match.group("prompt")

    assert "Decide which available agents, tools, and slave skills to use" in prompt
    assert "Do not ask the user" in prompt
    for prescribed in (
        "First call list_agents",
        "Use write_slave_file",
        "Use run_slave_bash",
        "Use read_slave_file",
        "bash skill",
        "file skill",
    ):
        assert prescribed not in prompt


def test_autonomous_report_records_no_human_intervention_and_actual_choices() -> None:
    text = BASE_SCRIPT.read_text()

    assert "human_intervention_count" in text
    assert "autonomy_gate" in text
    assert "driver_decision_summary.json" in text
    assert "实际选择" in text
    assert "multi_agent_driver_autonomous_report_cn.md" in text
