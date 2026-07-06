"""Spec §7 (f) — end-to-end preflight in run.sh via WORKTREE_ROOT override.

Uses LOOM_FULLTABLE_DISPATCH_SHIM=1 to skip real runner exec and
WORKTREE_ROOT=<synth repo> to point commit_meta at a controlled tree.
"""
from __future__ import annotations

import os
import shutil
import subprocess
import textwrap
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT


def _init_repo(tmp_path: Path) -> Path:
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q"], cwd=str(repo), check=True)
    subprocess.run(["git", "config", "user.email", "a@b"], cwd=str(repo), check=True)
    subprocess.run(["git", "config", "user.name", "a"], cwd=str(repo), check=True)
    (repo / "seed.txt").write_text("seed\n")
    subprocess.run(["git", "add", "seed.txt"], cwd=str(repo), check=True)
    subprocess.run(["git", "commit", "-qm", "seed"], cwd=str(repo), check=True)
    return repo


def _shim(tmp_repo: Path):
    return {
        "LOOM_FULLTABLE_DISPATCH_SHIM": "1",
        "WORKTREE_ROOT": str(tmp_repo),
    }


def _run_sample(env):
    full_env = os.environ.copy()
    full_env.update(env)
    return subprocess.run(
        ["bash", str(FULLTABLE_DIR / "run.sh"), "--sample", "1"],
        cwd=str(MODULE_ROOT), capture_output=True, text=True, env=full_env,
    )


def test_staged_dirty_exits_2(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / "new.txt").write_text("x\n")
    subprocess.run(["git", "add", "new.txt"], cwd=str(repo), check=True)
    proc = _run_sample(_shim(repo))
    assert proc.returncode == 2
    assert "ErrDirtyWorktree" in proc.stderr


def test_unstaged_dirty_exits_2(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / "seed.txt").write_text("changed\n")
    proc = _run_sample(_shim(repo))
    assert proc.returncode == 2
    assert "ErrDirtyWorktree" in proc.stderr


def test_untracked_not_ignored_exits_2(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / "stray.txt").write_text("x\n")
    proc = _run_sample(_shim(repo))
    assert proc.returncode == 2
    assert "ErrDirtyWorktree" in proc.stderr


def test_untracked_but_gitignored_passes(tmp_path):
    repo = _init_repo(tmp_path)
    (repo / ".gitignore").write_text("ignored.txt\n")
    subprocess.run(["git", "add", ".gitignore"], cwd=str(repo), check=True)
    subprocess.run(["git", "commit", "-qm", "ignore"], cwd=str(repo), check=True)
    (repo / "ignored.txt").write_text("x\n")
    proc = _run_sample(_shim(repo))
    assert proc.returncode == 0, (proc.stdout, proc.stderr)
    assert "SHIM: would dispatch 1 rows" in proc.stdout
