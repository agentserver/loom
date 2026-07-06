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


def test_target_outside_paper_worktree(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    """gate3 (spec §4.0 rule 3): resolved --target path must be inside the
    paper_writing worktree root. Otherwise a `paper_outputs/introduction_v3.md`
    outside the sanctioned worktree could sneak past the basename/parent
    check.
    """
    scratch = _mirror_paper(tmp_path, paper_outputs)
    # Build an evil paper_outputs/introduction_v3.md OUTSIDE the scratch root.
    evil_root = tmp_path / "evil"
    (evil_root / "paper_outputs").mkdir(parents=True)
    (evil_root / "paper_outputs" / "introduction_v3.md").write_text("x", encoding="utf-8")
    r = _run([
        RI,
        "--target", str(evil_root / "paper_outputs" / "introduction_v3.md"),
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

@pytest.mark.parametrize("key,bad_value", [
    ("contexts_count", 0),
    ("manual_steps_ssh", 0),
    ("wrong_context_failure_manual_baseline", -0.1),
    ("wrong_context_failure_manual_baseline", 1.5),
    ("reuse_time_savings", -101.0),
    ("reuse_time_savings", 200.0),
])
@pytest.mark.parametrize("bad_field", ["median", "iqr_low", "iqr_high"])
def test_number_range_sanity(
    ma_root: Path, paper_outputs: Path, tmp_path: Path,
    key: str, bad_value: float, bad_field: str,
):
    """spec §5 test_number_range_sanity — parametrised over each of
    median/iqr_low/iqr_high independently. An implementation that only
    range-checked median would pass a bad iqr_low row here.
    """
    scratch = _mirror_paper(tmp_path, paper_outputs)
    numdir = tmp_path / f"bad_{key}_{bad_field}"
    numdir.mkdir()
    # Start from all-good baselines.
    good = {
        "contexts_count":                        {"median": 3, "iqr_low": 3, "iqr_high": 3},
        "wrong_context_failure_manual_baseline": {"median": 0.5, "iqr_low": 0.4, "iqr_high": 0.6},
        "manual_steps_ssh":                      {"median": 10, "iqr_low": 10, "iqr_high": 10},
        "reuse_time_savings":                    {"median": 50.0, "iqr_low": 40.0, "iqr_high": 60.0},
    }
    # Set the specific field to the bad value; leave other two in-range so
    # the ordering invariant doesn't trip (that would mask the range error).
    # For fields where out-of-order would incidentally happen, still assert
    # the failure token is ErrNumberOutOfRange (range check runs BEFORE the
    # order check per spec §4.4).
    target = dict(good[key])
    target[bad_field] = bad_value
    good[key] = target
    for k, rec in good.items():
        rec = dict(rec)
        rec.update({"canonical_key": k, "n_samples": 3})
        (numdir / f"{k}.json").write_text(json.dumps(rec), encoding="utf-8")
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
    assert r.returncode == 2, r.stderr
    assert "ErrNumberOutOfRange" in r.stderr
    # Also verify the error message names the specific field so a future
    # implementer can't collapse the three checks into one.
    assert bad_field in r.stderr, (
        f"error message must name the offending field {bad_field!r}; got: {r.stderr}"
    )
    # And no diff must have been printed (spec §4.4).
    assert r.stdout == "", f"unexpected stdout on range error: {r.stdout!r}"


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


def test_motivation_placeholder_extra_rejected(
    ma_root: Path, fake_main_dir: Path, paper_outputs: Path, tmp_path: Path,
):
    """spec §4.2 exact-8 rule: an unknown extra `{{...}}` token is P0."""
    scratch = _mirror_paper(tmp_path, paper_outputs)
    motiv = scratch / "paper_outputs" / "motivation_v3.md"
    motiv.write_text(
        motiv.read_text(encoding="utf-8") + "\n{{unexpected_token}}\n",
        encoding="utf-8",
    )
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
    assert "unexpected_token" in r.stderr


def test_smoke_numbers_dir_rejects_extras(
    ma_root: Path, paper_outputs: Path, tmp_path: Path,
):
    """spec §4.3 smoke-mode: exactly 4 canonical JSON basenames; any
    extra file in the smoke dir → ErrNumbersDirNotFromMainExperiment.
    """
    scratch = _mirror_paper(tmp_path, paper_outputs)
    numdir = tmp_path / "smoke_extras"
    numdir.mkdir()
    good = {
        "contexts_count":                        {"median": 3, "iqr_low": 3, "iqr_high": 3},
        "wrong_context_failure_manual_baseline": {"median": 0.5, "iqr_low": 0.4, "iqr_high": 0.6},
        "manual_steps_ssh":                      {"median": 10, "iqr_low": 10, "iqr_high": 10},
        "reuse_time_savings":                    {"median": 50.0, "iqr_low": 40.0, "iqr_high": 60.0},
    }
    for k, r in good.items():
        r.update({"canonical_key": k, "n_samples": 3})
        (numdir / f"{k}.json").write_text(json.dumps(r), encoding="utf-8")
    (numdir / "stray.txt").write_text("noise", encoding="utf-8")
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
    assert "ErrNumbersDirNotFromMainExperiment" in r.stderr
    assert "stray.txt" in r.stderr


# ---------------------------------------------------------------------------
# happy path — dry-run outputs diff for both targets
# ---------------------------------------------------------------------------

_GUARD_TESTER_MOTIV = r"""
import sys, types
from pathlib import Path
spec_path = Path(sys.argv[1])
src = spec_path.read_text(encoding="utf-8")
mod = types.ModuleType("_ri")
mod.__file__ = str(spec_path)
sys.modules["_ri"] = mod
# Ensure any dataclass decorator can look the class up in the module's
# __dict__ during evaluation of ClassVar annotations (Python 3.14 change).
exec(compile(src, str(spec_path), "exec"), mod.__dict__)

class R:
    median = 5
    iqr_low = 5
    iqr_high = 5
records = {k: R() for k in mod.CANONICAL_KEYS}

lines = ["Prelude line with no placeholder\n"]
for key in mod.CANONICAL_KEYS:
    lines.append(f"line {{{{{key}_median}}}} continues\n")
    lines.append(f"line {{{{{key}_iqr}}}} continues\n")
text = "".join(lines)

mod._format_scalar = lambda v, k: "X\nY"
try:
    mod._patch_motivation(text, records)
except SystemExit as e:
    print(f"__SYSEXIT__{e.code}")
    sys.exit(0)
print("__NO_EXIT__")
"""


_GUARD_TESTER_INTRO = r"""
import sys, types
from pathlib import Path
spec_path = Path(sys.argv[1])
src = spec_path.read_text(encoding="utf-8")
mod = types.ModuleType("_ri")
mod.__file__ = str(spec_path)
sys.modules["_ri"] = mod
# Ensure any dataclass decorator can look the class up in the module's
# __dict__ during evaluation of ClassVar annotations (Python 3.14 change).
exec(compile(src, str(spec_path), "exec"), mod.__dict__)

class R:
    median = 5
    iqr_low = 5
    iqr_high = 5
records = {k: R() for k in mod.CANONICAL_KEYS}

text = (
    "现代 AI agents 它们仍然是割裂的 raw contexts。系统缺少一种机制来回答：哪个 context 后续\n"
    "agent 仍可能选错机器 且 不需要复用的能力可继续以 one-off script 形态存在。\n"
    "因此，personal compute space 下段\n"
)

mod._format_scalar = lambda v, k: "X\nY"
try:
    mod._patch_intro(text, records)
except SystemExit as e:
    print(f"__SYSEXIT__{e.code}")
    sys.exit(0)
print("__NO_EXIT__")
"""


def _run_guard_tester(script_text: str, spec_path: Path):
    return subprocess.run(
        [sys.executable, "-c", script_text, str(spec_path)],
        capture_output=True, text=True,
    )


def test_motivation_outside_placeholder_guard(ma_root: Path):
    """spec §5 test_replace_intro_outside_region_rejected (motivation half).

    Defensive post-condition: an insertion whose text contains a newline
    would cross into an adjacent line. Run in a subprocess to avoid
    dataclass-import interactions with pytest's importlib.
    """
    spec_path = ma_root / "tests" / "eval" / "motivation" / "replace_intro.py"
    r = _run_guard_tester(_GUARD_TESTER_MOTIV, spec_path)
    assert r.returncode == 0, r.stderr
    assert "__SYSEXIT__2" in r.stdout, r.stdout


def test_intro_outside_anchor_guard(ma_root: Path):
    """spec §5 test_replace_intro_outside_region_rejected (intro half)."""
    spec_path = ma_root / "tests" / "eval" / "motivation" / "replace_intro.py"
    r = _run_guard_tester(_GUARD_TESTER_INTRO, spec_path)
    assert r.returncode == 0, r.stderr
    assert "__SYSEXIT__2" in r.stdout, r.stdout


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
