#!/usr/bin/env python3
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parent
SCRIPT = ROOT / "run_csv_to_parquet_smoke.sh"


def test_script_declares_three_container_agents() -> None:
    text = SCRIPT.read_text()
    for agent in ("agent-inspector", "agent-converter", "agent-verifier"):
        assert agent in text


def test_script_uses_container_isolation() -> None:
    text = SCRIPT.read_text()
    assert "--network none" in text
    assert re.search(r"docker\s+run", text)
    assert "CODEX_HOME" not in text


def test_script_writes_expected_report_files() -> None:
    text = SCRIPT.read_text()
    for name in (
        "multi_agent_container_report_cn.md",
        "schema_report.json",
        "conversion_report.json",
        "verification.json",
        "artifact_manifest.json",
    ):
        assert name in text
