"""Path fixtures for the p3-mini-case motivation test suite."""

from __future__ import annotations

import os
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent

# multi-agent module root (…/multi-agent/); parent(4) walks from
# tests/eval/motivation/tests/ up to the module root.
MA_ROOT = HERE.parents[3]

WORKLOAD_DIR = MA_ROOT / "tests" / "eval" / "workloads" / "motivation-e2e"
TRACE_DIR = WORKLOAD_DIR / "fixtures" / "traces"
MOTIVATION_PKG = MA_ROOT / "tests" / "eval" / "motivation"
FAKE_MAIN_DIR = MOTIVATION_PKG / "tests" / "fixtures" / "fake_main_experiment_dir"
SMOKE_RESULTS_DIR = MOTIVATION_PKG / "results" / "dry_run_smoke"

PAPER_WORKTREE = Path(
    os.environ.get("PAPER_WORKTREE", "/root/paper_writing/.worktrees/p3-mini-case")
).resolve()
PAPER_OUTPUTS = PAPER_WORKTREE / "paper_outputs"


@pytest.fixture
def ma_root() -> Path:
    return MA_ROOT


@pytest.fixture
def workload_dir() -> Path:
    return WORKLOAD_DIR


@pytest.fixture
def trace_dir() -> Path:
    return TRACE_DIR


@pytest.fixture
def motivation_pkg() -> Path:
    return MOTIVATION_PKG


@pytest.fixture
def fake_main_dir() -> Path:
    return FAKE_MAIN_DIR


@pytest.fixture
def smoke_results_dir() -> Path:
    return SMOKE_RESULTS_DIR


@pytest.fixture
def paper_worktree() -> Path:
    return PAPER_WORKTREE


@pytest.fixture
def paper_outputs() -> Path:
    return PAPER_OUTPUTS
