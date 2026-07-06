"""Shared fixtures for the WT-3-prod-multidevice test suite.

All tests are HARNESS-ONLY — they never invoke real deploy.sh --mode
prod, real OAuth device flow, or real cloud vendor CLIs.
"""
from __future__ import annotations

import pathlib
import subprocess

import pytest

# Repo-relative paths used across tests.
HERE = pathlib.Path(__file__).resolve().parent
MULTIDEVICE_DIR = HERE.parent
PROD_TEST_DIR = MULTIDEVICE_DIR.parent
TESTS_DIR = PROD_TEST_DIR.parent
MULTI_AGENT_ROOT = TESTS_DIR.parent
WORKTREE_ROOT = MULTI_AGENT_ROOT.parent


@pytest.fixture(scope="session")
def multidevice_dir() -> pathlib.Path:
    return MULTIDEVICE_DIR


@pytest.fixture(scope="session")
def multi_agent_root() -> pathlib.Path:
    return MULTI_AGENT_ROOT


@pytest.fixture(scope="session")
def worktree_root() -> pathlib.Path:
    return WORKTREE_ROOT


@pytest.fixture(scope="session")
def analysis_template_path() -> pathlib.Path:
    return MULTI_AGENT_ROOT / "tests" / "eval" / "results" / "prod" / \
        "dry_run_smoke" / "analysis_template.md"


@pytest.fixture(scope="session")
def runbook_path() -> pathlib.Path:
    return PROD_TEST_DIR / "E2E_RUNBOOK.md"


def _git(cmd: list[str], cwd: pathlib.Path) -> subprocess.CompletedProcess[str]:
    """Run git with text output; do not raise on non-zero."""
    return subprocess.run(
        ["git", *cmd],
        cwd=str(cwd),
        capture_output=True,
        text=True,
        check=False,
    )


@pytest.fixture(scope="session")
def git_runner():
    return _git
