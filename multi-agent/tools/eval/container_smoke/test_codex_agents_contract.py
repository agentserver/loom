#!/usr/bin/env python3
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parent
SCRIPT = ROOT / "run_heterogeneous_dates_codex_agents.sh"


def test_driver_runs_real_codex_agents_in_containers() -> None:
    text = SCRIPT.read_text()

    assert re.search(r"docker\s+run", text)
    assert "codex exec" in text
    for agent in ("data-agent", "solver-agent", "verifier-agent"):
        assert agent in text


def test_driver_isolates_agent_state_and_records_usage() -> None:
    text = SCRIPT.read_text()

    assert "CODEX_HOME" in text
    assert "agent_usage_summary.json" in text
    assert "model_input_tokens" in text
    assert "model_output_tokens" in text
    assert "container_isolation.json" in text


def test_driver_keeps_solution_out_of_orchestration() -> None:
    text = SCRIPT.read_text()

    assert "11.428571" not in text
    assert "avg_temp.txt must contain only" in text
    assert "oracle_result.json" in text
    assert "multi_agent_codex_container_report_cn.md" in text
