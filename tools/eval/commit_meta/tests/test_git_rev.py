"""git rev-parse wrapper tests.

Uses pytest tmp_path to spin up a real one-commit repo — we shell out to
`git` instead of mocking subprocess so the wrapper exercises the same code
path the CLI will hit in production.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from commit_meta.git_rev import find_git_root, get_commit


def _git(cwd: Path, *args: str) -> None:
    subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True)


@pytest.fixture
def tiny_repo(tmp_path: Path) -> Path:
    repo = tmp_path / "tinyrepo"
    repo.mkdir()
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "config", "user.email", "t@t")
    _git(repo, "config", "user.name", "t")
    (repo / "f.txt").write_text("hi\n")
    _git(repo, "add", "f.txt")
    _git(repo, "commit", "-q", "-m", "init")
    return repo


def test_returns_short_sha_for_real_repo(tiny_repo: Path) -> None:
    sha = get_commit(str(tiny_repo))
    # Short SHA is typically 7+ hex chars; the wrapper appends branch+dirty
    # state in parens, e.g. "abc1234 (main clean)".
    head = sha.split(" ", 1)[0]
    assert len(head) >= 7
    assert all(c in "0123456789abcdef" for c in head)
    assert "(" in sha and ")" in sha  # branch+state suffix present


def test_branch_and_clean_state_in_suffix(tiny_repo: Path) -> None:
    sha = get_commit(str(tiny_repo))
    assert "main" in sha
    assert "clean" in sha


def test_dirty_state_when_workdir_modified(tiny_repo: Path) -> None:
    (tiny_repo / "f.txt").write_text("changed\n")
    sha = get_commit(str(tiny_repo))
    assert "dirty" in sha


def test_returns_na_for_missing_path(tmp_path: Path) -> None:
    missing = tmp_path / "does_not_exist"
    result = get_commit(str(missing))
    assert result.startswith("N/A:")
    assert str(missing) in result


def test_returns_na_for_non_git_dir(tmp_path: Path) -> None:
    not_a_repo = tmp_path / "plain"
    not_a_repo.mkdir()
    result = get_commit(str(not_a_repo))
    assert result.startswith("N/A:")
    assert str(not_a_repo) in result


def test_returns_na_for_none_path() -> None:
    # When caller passes None (env var unset + no default match), wrapper
    # must produce a stable N/A string, not crash.
    result = get_commit(None)
    assert result.startswith("N/A:")


def test_returns_na_when_git_binary_missing(tiny_repo: Path, monkeypatch) -> None:
    """If git is not on PATH, subprocess.run raises FileNotFoundError.
    Wrapper must absorb it and surface a stable N/A string rather than
    propagating the exception.
    """

    def _boom(*args, **kwargs):  # noqa: ANN001 — signature mirrors subprocess.run
        raise FileNotFoundError(2, "No such file or directory: 'git'")

    monkeypatch.setattr(subprocess, "run", _boom)
    result = get_commit(str(tiny_repo))
    assert result.startswith("N/A:")


def test_returns_na_when_git_times_out(tiny_repo: Path, monkeypatch) -> None:
    """The 5-second timeout branch in _run_git is otherwise untested;
    a hung git on a corrupt repo must not deadlock the collector.
    """

    def _slow(*args, **kwargs):  # noqa: ANN001
        raise subprocess.TimeoutExpired(cmd="git", timeout=5)

    monkeypatch.setattr(subprocess, "run", _slow)
    result = get_commit(str(tiny_repo))
    assert result.startswith("N/A:")


def test_find_git_root_returns_repo_root(tiny_repo: Path) -> None:
    """find_git_root walks up from a directory inside a repo and returns
    the repo's top level — exercised by _resolve_loom but otherwise
    uncovered.
    """
    nested = tiny_repo / "sub" / "deeper"
    nested.mkdir(parents=True)
    root = find_git_root(str(nested))
    assert root is not None
    # Use realpath to absorb any /tmp -> /private/tmp symlink on macOS.
    import os

    assert os.path.realpath(root) == os.path.realpath(str(tiny_repo))


def test_find_git_root_returns_none_outside_repo(tmp_path: Path) -> None:
    not_a_repo = tmp_path / "scratch"
    not_a_repo.mkdir()
    assert find_git_root(str(not_a_repo)) is None
