"""replace_intro.py tests (spec §5 + §7 (f)/(h)/(j)/(k))."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

import pytest


RI = "tests/eval/motivation/replace_intro.py"


def _run(args, cwd: Path, env: dict | None = None):
    e = os.environ.copy()
    if env:
        e.update(env)
    return subprocess.run(
        [sys.executable, *args], cwd=str(cwd),
        capture_output=True, text=True, env=e,
    )


def _mirror_paper(tmp_path: Path, paper_outputs: Path) -> Path:
    """Create a scratch paper_outputs layout under tmp_path with the two
    target files copied in."""
    root = tmp_path / "paper_writing_scratch"
    po = root / "paper_outputs"
    po.mkdir(parents=True)
    shutil.copy(paper_outputs / "introduction_v3.md", po / "introduction_v3.md")
    shutil.copy(paper_outputs / "motivation_v3.md", po / "motivation_v3.md")
    return root


# ---------------------------------------------------------------------------
# gate1 — --execute + --allow-scaffold-smoke-input mutex
# ---------------------------------------------------------------------------

def test_execute_with_smoke_input_rejected(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--execute",
        "--allow-scaffold-smoke-input",
    ], cwd=ma_root, env={"ALLOW_INTRO_REWRITE": "1"})
    assert r.returncode == 2
    assert "ErrExecuteSmokeConflict" in r.stderr


# ---------------------------------------------------------------------------
# gate2 — --numbers source-of-truth
# ---------------------------------------------------------------------------

def test_numbers_dir_missing_main_signature(
    ma_root: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    numdir = tmp_path / "numbers"
    numdir.mkdir()
    (numdir / "runs.csv").write_text("x", encoding="utf-8")
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(numdir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrNumbersDirNotFromMainExperiment" in r.stderr


def test_numbers_dir_smoke_input_passes(
    ma_root: Path, smoke_results_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    """Smoke mode + 4 canonical JSON present → passes gate2."""
    # Only meaningful when dry_run_smoke.sh has produced smoke_results_dir.
    if not smoke_results_dir.exists() or not any(smoke_results_dir.iterdir()):
        pytest.skip("dry_run_smoke has not populated results/dry_run_smoke/ yet")
    scratch = _mirror_paper(tmp_path, paper_outputs)
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(smoke_results_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--allow-scaffold-smoke-input",
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr


def test_execute_without_env_downgrades(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--execute",
    ], cwd=ma_root, env={"ALLOW_INTRO_REWRITE": ""})  # explicitly clear env
    assert r.returncode == 0, r.stderr
    assert "dry-run mode" in r.stderr
    intro_after = (scratch / "paper_outputs" / "introduction_v3.md").read_text()
    motiv_after = (scratch / "paper_outputs" / "motivation_v3.md").read_text()
    intro_orig = (paper_outputs / "introduction_v3.md").read_text()
    motiv_orig = (paper_outputs / "motivation_v3.md").read_text()
    assert intro_after == intro_orig
    assert motiv_after == motiv_orig


# ---------------------------------------------------------------------------
# gate3 — path whitelist
# ---------------------------------------------------------------------------

def test_target_missing_only_one(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrTargetMissing" in r.stderr


def test_target_duplicate(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrDuplicateTarget" in r.stderr


def test_target_basename_not_whitelisted(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    other = scratch / "paper_outputs" / "other.md"
    other.write_text("x", encoding="utf-8")
    r = _run([
        RI,
        "--target", str(other),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrTargetPathRejected" in r.stderr


def test_target_parent_not_paper_outputs(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    (scratch / "elsewhere").mkdir()
    off = scratch / "elsewhere" / "introduction_v3.md"
    off.write_text("x", encoding="utf-8")
    r = _run([
        RI,
        "--target", str(off),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrTargetPathRejected" in r.stderr


# ---------------------------------------------------------------------------
# gate4 — range + IQR ordering
# ---------------------------------------------------------------------------

@pytest.mark.parametrize("key,median,iqr_low,iqr_high", [
    ("contexts_count", 0, 0, 0),
    ("manual_steps_ssh", 0, 0, 0),
    ("wrong_context_failure_manual_baseline", -0.1, -0.1, -0.1),
    ("wrong_context_failure_manual_baseline", 1.5, 1.5, 1.5),
    ("reuse_time_savings", -101.0, -101.0, -101.0),
    ("reuse_time_savings", 200.0, 200.0, 200.0),
])
def test_number_range_sanity(
    ma_root: Path, paper_outputs: Path, tmp_path: Path,
    key: str, median: float, iqr_low: float, iqr_high: float,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    numdir = tmp_path / "bad_numbers"
    numdir.mkdir()
    # Fill 4 canonical files; only `key` gets bad values.
    good = {
        "contexts_count":                        {"median": 3, "iqr_low": 3, "iqr_high": 3},
        "wrong_context_failure_manual_baseline": {"median": 0.5, "iqr_low": 0.4, "iqr_high": 0.6},
        "manual_steps_ssh":                      {"median": 10, "iqr_low": 10, "iqr_high": 10},
        "reuse_time_savings":                    {"median": 50.0, "iqr_low": 40.0, "iqr_high": 60.0},
    }
    good[key] = {"median": median, "iqr_low": iqr_low, "iqr_high": iqr_high}
    for k, r in good.items():
        r.update({"canonical_key": k, "n_samples": 3})
        (numdir / f"{k}.json").write_text(json.dumps(r), encoding="utf-8")
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(numdir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--allow-scaffold-smoke-input",
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrNumberOutOfRange" in r.stderr


def test_iqr_order_invariant(
    ma_root: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    numdir = tmp_path / "iqr_bad"
    numdir.mkdir()
    good = {
        "contexts_count":                        {"median": 3, "iqr_low": 3, "iqr_high": 3},
        "wrong_context_failure_manual_baseline": {"median": 0.5, "iqr_low": 0.4, "iqr_high": 0.6},
        "manual_steps_ssh":                      {"median": 10, "iqr_low": 10, "iqr_high": 10},
        # Inverted (all still in range 0..100 but median outside [low, high]).
        "reuse_time_savings":                    {"median": 5.0, "iqr_low": 10.0, "iqr_high": 1.0},
    }
    for k, r in good.items():
        r.update({"canonical_key": k, "n_samples": 3})
        (numdir / f"{k}.json").write_text(json.dumps(r), encoding="utf-8")
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(numdir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--allow-scaffold-smoke-input",
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrIqrOrderInvalid" in r.stderr


# ---------------------------------------------------------------------------
# gate6a — intro pattern coverage + outside-anchor
# ---------------------------------------------------------------------------

def test_intro_pattern_coverage(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    # Delete one of the 4 sentence patterns from the intro paragraph.
    intro = scratch / "paper_outputs" / "introduction_v3.md"
    text = intro.read_text(encoding="utf-8")
    # remove the wrong_context_failure sentence pattern.
    text2 = text.replace("系统缺少一种机制来回答：哪个 context",
                         "系统缺少一种机制来回答：什么设备")
    intro.write_text(text2, encoding="utf-8")
    r = _run([
        RI,
        "--target", str(intro),
        "--target", str(scratch / "paper_outputs" / "motivation_v3.md"),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrIntroPatternMismatch" in r.stderr


# ---------------------------------------------------------------------------
# gate6b — motivation placeholder coverage + outside-placeholder
# ---------------------------------------------------------------------------

def test_motivation_placeholder_coverage(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    motiv = scratch / "paper_outputs" / "motivation_v3.md"
    text = motiv.read_text(encoding="utf-8")
    # Remove one of the 8 placeholders.
    text2 = text.replace("{{contexts_count_median}}", "REMOVED")
    motiv.write_text(text2, encoding="utf-8")
    r = _run([
        RI,
        "--target", str(scratch / "paper_outputs" / "introduction_v3.md"),
        "--target", str(motiv),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 2
    assert "ErrMotivationPlaceholderMissing" in r.stderr


# ---------------------------------------------------------------------------
# happy path — dry-run outputs diff for both targets
# ---------------------------------------------------------------------------

def test_dry_run_produces_dual_diff(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    scratch = _mirror_paper(tmp_path, paper_outputs)
    intro = scratch / "paper_outputs" / "introduction_v3.md"
    motiv = scratch / "paper_outputs" / "motivation_v3.md"
    intro_orig = intro.read_text(encoding="utf-8")
    motiv_orig = motiv.read_text(encoding="utf-8")
    r = _run([
        RI,
        "--target", str(intro),
        "--target", str(motiv),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--dry-run",
    ], cwd=ma_root)
    assert r.returncode == 0, r.stderr
    assert "introduction_v3.md" in r.stdout
    assert "motivation_v3.md" in r.stdout
    # No writes.
    assert intro.read_text(encoding="utf-8") == intro_orig
    assert motiv.read_text(encoding="utf-8") == motiv_orig


def test_atomic_rollback_on_intro_failure(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    """Intro pattern mismatch → both targets untouched, even under --execute."""
    scratch = _mirror_paper(tmp_path, paper_outputs)
    intro = scratch / "paper_outputs" / "introduction_v3.md"
    text = intro.read_text(encoding="utf-8")
    intro.write_text(
        text.replace("系统缺少一种机制来回答：哪个 context",
                     "系统缺少一种机制来回答：什么设备"),
        encoding="utf-8",
    )
    orig_intro = intro.read_text(encoding="utf-8")
    motiv = scratch / "paper_outputs" / "motivation_v3.md"
    orig_motiv = motiv.read_text(encoding="utf-8")

    r = _run([
        RI,
        "--target", str(intro),
        "--target", str(motiv),
        "--numbers", str(fake_main_dir),
        "--provenance-path", "/tmp/pv.md",
        "--paper-worktree", str(scratch),
        "--execute",
    ], cwd=ma_root, env={"ALLOW_INTRO_REWRITE": "1"})
    assert r.returncode == 2
    assert intro.read_text(encoding="utf-8") == orig_intro
    assert motiv.read_text(encoding="utf-8") == orig_motiv
