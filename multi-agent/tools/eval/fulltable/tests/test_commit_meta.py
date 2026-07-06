"""Spec §7 (f) — commit_meta preflight + post-run verify."""
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from lib.commit_meta import (
    ErrDirtyWorktree,
    ErrLoomCommitMismatch,
    assert_clean_and_get_head,
    verify_runs_loom_commit,
)


def _init_repo(tmp_path: Path) -> Path:
    repo = tmp_path / "repo"
    repo.mkdir()
    def run(args):
        subprocess.run(args, cwd=str(repo), check=True,
                       capture_output=True)
    run(["git", "init", "-q"])
    run(["git", "config", "user.email", "a@b"])
    run(["git", "config", "user.name", "a"])
    (repo / "seed.txt").write_text("seed\n")
    run(["git", "add", "seed.txt"])
    run(["git", "commit", "-qm", "seed"])
    return repo


def _head(repo: Path) -> str:
    return subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"],
                                   text=True).strip()


def test_happy_path_clean(tmp_path):
    repo = _init_repo(tmp_path)
    head = assert_clean_and_get_head(repo)
    assert head == _head(repo)
    assert len(head) == 40


def test_staged_dirty_rejected(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / "new.txt").write_text("x\n")
    subprocess.run(["git", "-C", str(repo), "add", "new.txt"], check=True)
    with pytest.raises(ErrDirtyWorktree):
        assert_clean_and_get_head(repo)


def test_unstaged_dirty_rejected(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / "seed.txt").write_text("changed\n")
    with pytest.raises(ErrDirtyWorktree):
        assert_clean_and_get_head(repo)


def test_untracked_not_ignored_rejected(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / "loose.txt").write_text("x\n")
    with pytest.raises(ErrDirtyWorktree):
        assert_clean_and_get_head(repo)


def test_untracked_but_gitignored_passes(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / ".gitignore").write_text("ignored.txt\n")
    subprocess.run(["git", "-C", str(repo), "add", ".gitignore"], check=True)
    subprocess.run(["git", "-C", str(repo), "commit", "-qm", "ignore"],
                   check=True)
    (repo / "ignored.txt").write_text("x\n")
    head = assert_clean_and_get_head(repo)
    assert head == _head(repo)


def _write_runs_csv(path: Path, sha: str, rows: int = 2) -> None:
    header = "run_id,workload_id,loom_commit"
    lines = [header]
    for i in range(rows):
        lines.append(f"run{i},w,{sha}")
    path.write_text("\n".join(lines) + "\n")


def test_verify_runs_loom_commit_ok(tmp_path):
    csv = tmp_path / "runs.csv"
    _write_runs_csv(csv, "a" * 40)
    verify_runs_loom_commit(csv, "a" * 40)


def test_verify_runs_loom_commit_drift(tmp_path):
    csv = tmp_path / "runs.csv"
    _write_runs_csv(csv, "a" * 40)
    with pytest.raises(ErrLoomCommitMismatch):
        verify_runs_loom_commit(csv, "b" * 40)


def test_verify_runs_missing_col(tmp_path):
    csv = tmp_path / "runs.csv"
    csv.write_text("run_id,workload_id\nx,y\n")
    with pytest.raises(ErrLoomCommitMismatch):
        verify_runs_loom_commit(csv, "a" * 40)


def test_verify_runs_accepts_status_suffix(tmp_path):
    csv = tmp_path / "runs.csv"
    header = "run_id,workload_id,loom_commit"
    sha = "c" * 40
    csv.write_text(f"{header}\nrunx,w,{sha} (clean)\n")
    verify_runs_loom_commit(csv, sha)
