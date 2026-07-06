"""Collector-level tests (spec §5)."""

from __future__ import annotations

import json
import shutil
import sqlite3
import subprocess
import sys
from pathlib import Path

import pytest


COLLECT_CTX = "tests/eval/motivation/collect_contexts_count.py"
COLLECT_WCF = "tests/eval/motivation/collect_wrong_context_failure.py"
COLLECT_MS = "tests/eval/motivation/collect_manual_steps.py"
COLLECT_RTS = "tests/eval/motivation/collect_reuse_time_savings.py"


def _run(args, cwd: Path):
    return subprocess.run(
        [sys.executable, *args],
        cwd=str(cwd),
        capture_output=True,
        text=True,
    )


# ---------------------------------------------------------------------------
# spec §5 test_expected_numbers_not_hardcoded
# ---------------------------------------------------------------------------

def test_expected_numbers_not_hardcoded(motivation_pkg: Path):
    """No source file may hard-code an expected paper-visible number."""
    forbidden = [
        r"should be \d",
        r"expected: \d",
        r"assert .*== [0-9]",   # very rough — allow fixtures but not scripts
    ]
    scripts = [
        motivation_pkg / "collect_contexts_count.py",
        motivation_pkg / "collect_wrong_context_failure.py",
        motivation_pkg / "collect_manual_steps.py",
        motivation_pkg / "collect_reuse_time_savings.py",
        motivation_pkg / "aggregate.py",
        motivation_pkg / "gen_provenance.py",
        motivation_pkg / "replace_intro.py",
    ]
    for script in scripts:
        text = script.read_text(encoding="utf-8")
        for pat in forbidden:
            import re
            assert not re.search(pat, text), \
                f"{script} contains forbidden pattern {pat!r}"


# ---------------------------------------------------------------------------
# contexts_count collector
# ---------------------------------------------------------------------------

def test_contexts_count_happy_path(ma_root: Path, trace_dir: Path, tmp_path: Path):
    out = tmp_path / "ctx.json"
    r = _run([
        COLLECT_CTX,
        "--route-reasons", str(trace_dir / "route_reasons.sqlite"),
        "--workload-id", "motivation-e2e",
        "--out", str(out),
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads(out.read_text(encoding="utf-8"))
    assert rec["canonical_key"] == "contexts_count"
    assert rec["raw_value"] == 3
    assert rec["unit"] == "int"


def test_contexts_count_empty_result(ma_root: Path, trace_dir: Path, tmp_path: Path):
    r = _run([
        COLLECT_CTX,
        "--route-reasons", str(trace_dir / "route_reasons.sqlite"),
        "--workload-id", "unknown-workload",
        "--out", str(tmp_path / "ctx.json"),
    ], cwd=ma_root)
    assert r.returncode == 2, r.stderr


def test_contexts_count_missing_file(ma_root: Path, tmp_path: Path):
    r = _run([
        COLLECT_CTX,
        "--route-reasons", str(tmp_path / "does-not-exist.sqlite"),
        "--workload-id", "motivation-e2e",
    ], cwd=ma_root)
    assert r.returncode == 2


# ---------------------------------------------------------------------------
# wrong_context_failure collector — seed source
# ---------------------------------------------------------------------------

def test_wcf_happy_path(ma_root: Path, trace_dir: Path, workload_dir: Path, tmp_path: Path):
    out = tmp_path / "wcf.json"
    r = _run([
        COLLECT_WCF,
        "--runs-csv", str(trace_dir / "runs.csv"),
        "--workload-id", "motivation-e2e",
        "--baseline", "manual_ssh",
        "--spec-yaml", str(workload_dir / "spec.yaml"),
        "--out", str(out),
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads(out.read_text(encoding="utf-8"))
    # 10 rows for (motivation-e2e, manual_ssh), 3 True → 0.3
    assert rec["canonical_key"] == "wrong_context_failure_manual_baseline"
    assert abs(rec["raw_value"] - 0.3) < 1e-9
    assert rec["seed"] == 20260706  # from spec.yaml


def test_wcf_no_matching_rows(ma_root: Path, trace_dir: Path, workload_dir: Path, tmp_path: Path):
    r = _run([
        COLLECT_WCF,
        "--runs-csv", str(trace_dir / "runs.csv"),
        "--workload-id", "no-such-workload",
        "--baseline", "manual_ssh",
        "--spec-yaml", str(workload_dir / "spec.yaml"),
    ], cwd=ma_root)
    assert r.returncode == 2


def test_seed_published_in_spec_yaml(workload_dir: Path):
    """spec §5 test_seed_published_in_spec_yaml."""
    import re
    text = (workload_dir / "spec.yaml").read_text(encoding="utf-8")
    m = re.search(r"^\s*wrong_ctx_seed\s*:\s*(-?\d+)\s*$", text, re.MULTILINE)
    assert m, "wrong_ctx_seed: <int> not published in motivation-e2e/spec.yaml"
    assert int(m.group(1)) != 0


def test_seed_not_from_env(motivation_pkg: Path):
    """spec §5 test_seed_not_from_env — grep-audit collector source.

    Strip docstrings + comments before grepping so a prose mention of
    `os.environ` in the module docstring cannot spuriously trip this check.
    """
    import ast
    src = (motivation_pkg / "collect_wrong_context_failure.py").read_text(encoding="utf-8")
    tree = ast.parse(src)
    # Remove module + function docstrings (they're prose, not code).
    def _strip_docstrings(node):
        if isinstance(node, (ast.Module, ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            if (node.body and isinstance(node.body[0], ast.Expr)
                    and isinstance(node.body[0].value, ast.Constant)
                    and isinstance(node.body[0].value.value, str)):
                node.body[0] = ast.Expr(value=ast.Constant(value=""))
        for child in ast.iter_child_nodes(node):
            _strip_docstrings(child)
    _strip_docstrings(tree)
    code_only = ast.unparse(tree)
    # Also strip inline comments (ast.unparse never emits them, so we're good).
    assert "os.environ" not in code_only and "getenv" not in code_only, (
        "collect_wrong_context_failure.py must not read seed from environment"
    )
    assert "import os" not in code_only, (
        "collect_wrong_context_failure.py must not import os for env access"
    )


# ---------------------------------------------------------------------------
# manual_steps collector — primary + cross-check + fail-loud
# ---------------------------------------------------------------------------

def test_manual_steps_primary_source_enforced_no_sources(ma_root: Path):
    """spec §5 test_manual_steps_primary_source_enforced (a)."""
    r = _run([
        COLLECT_MS,
        "--workload-id", "motivation-e2e",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrManualStepsMissingSource" in r.stderr


def test_manual_steps_primary_source_enforced_transcript_only(
    ma_root: Path, trace_dir: Path,
):
    """spec §5 (b): --steps-log without --runs-csv is banned."""
    r = _run([
        COLLECT_MS,
        "--workload-id", "motivation-e2e",
        "--steps-log", str(trace_dir / "steps.log"),
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrManualStepsMissingRunsCSV" in r.stderr


def test_manual_steps_mismatch_fail_loud(
    ma_root: Path, trace_dir: Path, tmp_path: Path,
):
    """spec §5 (c): mismatch → ErrManualStepsMismatch."""
    # Author a mutated steps.log that drops one matching line.
    orig = (trace_dir / "steps.log").read_text(encoding="utf-8")
    mutated = tmp_path / "steps.log"
    lines = orig.splitlines(keepends=True)
    # Drop the first `^ssh ` line.
    for i, ln in enumerate(lines):
        if ln.startswith("ssh "):
            del lines[i]
            break
    mutated.write_text("".join(lines), encoding="utf-8")

    r = _run([
        COLLECT_MS,
        "--runs-csv", str(trace_dir / "runs.csv"),
        "--workload-id", "motivation-e2e",
        "--steps-log", str(mutated),
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrManualStepsMismatch" in r.stderr


def test_manual_steps_primary_source_enforced_match(
    ma_root: Path, trace_dir: Path, tmp_path: Path,
):
    """spec §5 (d): well-formed inputs → exit 0 + cross_check populated."""
    out = tmp_path / "ms.json"
    r = _run([
        COLLECT_MS,
        "--runs-csv", str(trace_dir / "runs.csv"),
        "--workload-id", "motivation-e2e",
        "--steps-log", str(trace_dir / "steps.log"),
        "--out", str(out),
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads(out.read_text(encoding="utf-8"))
    assert rec["raw_value"] == 10
    assert rec["cross_check"]["transcript_count"] == 10
    assert rec["cross_check"]["transcript_source_sha256"]


def test_manual_steps_transcript_format_positive(
    ma_root: Path, trace_dir: Path, tmp_path: Path,
):
    """spec §5 test_manual_steps_transcript_format positive path."""
    r = _run([
        COLLECT_MS,
        "--runs-csv", str(trace_dir / "runs.csv"),
        "--workload-id", "motivation-e2e",
        "--steps-log", str(trace_dir / "steps.log"),
        "--out", str(tmp_path / "ms.json"),
    ], cwd=ma_root)
    assert r.returncode == 0


def test_manual_ssh_baseline_never_shells_out(ma_root: Path):
    """spec §5 test_manual_ssh_baseline_never_shells_out."""
    # The existing manual_ssh baseline binary source MUST NOT contain
    # ^ssh|^scp|^rsync anywhere in its workloadScripts heredocs.
    for name in ("impl.go", "workloads.go"):
        text = (ma_root / "tests" / "eval" / "baselines" / "manual_ssh" / name).read_text(
            encoding="utf-8"
        )
        for pat in (r"\bssh ", r"\bscp ", r"\brsync "):
            import re
            # Ignore comment lines that mention these words as prose.
            hits = [
                line for line in text.splitlines()
                if re.search(pat, line) and not line.lstrip().startswith("//")
            ]
            assert not hits, \
                f"{name} contains executable {pat!r}: {hits[:3]}"


# ---------------------------------------------------------------------------
# reuse_time_savings collector
# ---------------------------------------------------------------------------

def test_reuse_time_savings_happy_path(ma_root: Path, trace_dir: Path, tmp_path: Path):
    r = _run([
        COLLECT_RTS,
        "--e4-stages-csv", str(trace_dir / "e4_stages.csv"),
        "--workload-id", "motivation-e2e",
        "--out-dir", str(tmp_path),
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    files = sorted(tmp_path.glob("reuse_time_savings.*.json"))
    assert len(files) == 3
    for f in files:
        rec = json.loads(f.read_text(encoding="utf-8"))
        assert rec["canonical_key"] == "reuse_time_savings"
        assert rec["unit"] == "percent"


def test_reuse_time_savings_zero_denominator(ma_root: Path, tmp_path: Path):
    """spec §5 test_reuse_denominator_zero_null."""
    e4 = tmp_path / "e4.csv"
    e4.write_text(
        "workload_id,family,stage,stage_start,stage_end\n"
        "wl,famZ,A,0.0,0.0\n"
        "wl,famZ,C,0.0,5.0\n",
        encoding="utf-8",
    )
    outdir = tmp_path / "out"
    r = _run([
        COLLECT_RTS,
        "--e4-stages-csv", str(e4),
        "--workload-id", "wl",
        "--out-dir", str(outdir),
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    rec = json.loads((outdir / "reuse_time_savings.famZ.json").read_text())
    assert rec["raw_value"] is None
    assert "instantaneous" in rec["note"].lower()


def test_reuse_time_savings_missing_file(ma_root: Path, tmp_path: Path):
    r = _run([
        COLLECT_RTS,
        "--e4-stages-csv", str(tmp_path / "nope.csv"),
        "--workload-id", "wl",
    ], cwd=ma_root)
    assert r.returncode == 2
