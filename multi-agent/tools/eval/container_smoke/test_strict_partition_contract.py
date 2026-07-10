#!/usr/bin/env python3
import re
from pathlib import Path


ROOT = Path(__file__).resolve().parent
SCRIPT = ROOT / "run_heterogeneous_dates_strict_partition_experiment.sh"
BASE_SCRIPT = ROOT / "run_heterogeneous_dates_driver_mcp_smoke.sh"


def test_strict_partition_runner_declares_main_configurations() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    for name in (
        "manual_ssh_cross_context",
        "single_machine_codex_ssh",
        "cloud_sandbox_context_injection",
        "FullPCS_strict_partition",
        "FullPCS_reuse_stage",
    ):
        assert name in text


def test_strict_partition_runner_keeps_driver_workspace_without_raw_csv() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "driver workspace must not contain raw CSV" in text
    assert "data_context" in text
    assert "compute_context" in text
    assert "daily_temp_sf_high.csv" in text
    assert "daily_temp_sf_low.csv" in text
    assert not re.search(
        r"cp\s+[^\\n]*daily_temp_sf_(high|low)\.csv[^\\n]*driver_workspace",
        text,
        re.I,
    )


def test_strict_partition_runner_writes_report_json_and_all_agent_tokens() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "strict_partition_report_cn.md" in text
    assert "strict_partition_results.json" in text
    assert "per_agent_tokens" in text
    assert "all_agent_total_tokens" in text


def test_strict_partition_runner_uses_shared_prompt_for_agent_configurations() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "shared_task_prompt.md" in text
    assert "SHARED_PROMPT_PATH" in text
    assert "prompt_sha256" in text
    assert "prompt_based_agent" in text
    assert "PROMPT_PARITY_CONFIGS" in text


def test_prompt_based_cloud_and_reuse_invoke_codex_and_collect_usage() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "cloud-codex" in text
    assert "reuse-stage-codex" in text
    assert re.search(
        r"cloud_sandbox_context_injection\(\).*?codex exec --json",
        text,
        re.S,
    )
    assert re.search(
        r"FullPCS_reuse_stage\(\).*?codex exec --json",
        text,
        re.S,
    )
    assert re.search(
        r"collect_codex_usage \"\$\{root\}/logs/cloud-codex\.jsonl\"",
        text,
    )
    assert re.search(
        r"collect_codex_usage \"\$\{root\}/logs/reuse-codex\.jsonl\"",
        text,
    )


def test_strict_partition_report_generator_imports_prompt_hash_dependency() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    match = re.search(r"generate_report\(\).*?<<'PY'\n(?P<body>.*?)\nPY", text, re.S)
    assert match, "generate_report Python block is missing"
    body = match.group("body")
    assert "hashlib.sha256" in body
    assert "import hashlib" in body


def test_strict_partition_runner_reports_final_avg_temp_values() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "final_avg_temp" in text
    assert "expected_avg_temp" in text
    assert "11.428571428571429" in text


def test_strict_partition_runner_records_redesign_gap_fields() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "context_injection_manifest.json" in text
    assert "wrong_context_probe.json" in text
    assert "wrong_context_failure" in text
    assert "redesign_alignment" in text
    assert "FullPCS_reuse_stage" in text


def test_strict_partition_runner_records_reuse_stage_artifacts() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "reuse_stage_report_cn.md" in text
    assert "capability_registry.json" in text
    assert "registry_hit" in text
    assert "repeated_generation" in text


def test_strict_partition_runner_records_fullpcs_wrong_target_probe() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert "fullpcs_wrong_target_probe.json" in text
    assert "FullPCS_wrong_target_recovery" in text
    assert "wrong_target_recovery" in text


def test_single_machine_codex_executes_from_driver_workspace() -> None:
    assert SCRIPT.exists(), "strict partition experiment runner is missing"
    text = SCRIPT.read_text()

    assert re.search(r"\(\s*\n\s*cd \"\$driver_workspace\"", text)
    assert "codex exec --json" in text


def test_driver_mcp_runner_has_strict_partition_mode() -> None:
    text = BASE_SCRIPT.read_text()

    assert "STRICT_PARTITION" in text
    assert "smoke-data-slave/workspace/input" in text
    assert "raw CSVs are not in the driver workspace" in text
    assert "FullPCS_strict_partition" in text
