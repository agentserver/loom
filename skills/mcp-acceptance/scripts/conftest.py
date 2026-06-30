"""Pytest fixtures for mcp_acceptance.py's WT-1-acceptance-golden suite.

The synth_cases fixture writes synthetic cases.jsonl files inside the
multi-agent module root (under tests/eval/golden/_pytest_tmp/) so they
pass the runner's --cases path-traversal check. The "_" prefix means
golden_schema_test.go:TestNoUnexpectedFamilies tolerates the dir.

Per-session cleanup removes the dir at teardown.
"""
from __future__ import annotations

import hashlib
import json
import os
import shutil
import sys
from pathlib import Path
from typing import Any, Callable

import pytest

# Anchor: this conftest lives at .../skills/mcp-acceptance/scripts/conftest.py.
# parents[3] backs out of scripts/ mcp-acceptance/ skills/ to repo root.
REPO_ROOT = Path(__file__).resolve().parents[3]
MODULE_ROOT = (REPO_ROOT / "multi-agent").resolve()
GOLDEN_ROOT = MODULE_ROOT / "tests" / "eval" / "golden"
PYTEST_TMP = GOLDEN_ROOT / "_pytest_tmp"
RUNNER = REPO_ROOT / "skills" / "mcp-acceptance" / "scripts" / "mcp_acceptance.py"
ORACLE = REPO_ROOT / "skills" / "mcp-acceptance" / "scripts" / "_echo_oracle_server.py"

# Five canonical family directories — mirrors expectedFamilies in
# golden_schema_test.go.
FAMILIES = [
    "api-wrapper-for-local-service",
    "csv-profiler",
    "image-metadata-extractor",
    "log-parser",
    "refund-policy-checker",
]


@pytest.fixture(scope="session", autouse=True)
def _ensure_and_cleanup_pytest_tmp():
    """Create the _pytest_tmp scratch dir; remove it after the session.

    We also pre-clean any stale residue from a previously-crashed
    session BEFORE creating the dir. The "_" prefix means
    golden_schema_test.go:TestNoUnexpectedFamilies tolerates surviving
    leftovers, but accumulated stale jsonl from a SIGKILLed worker can
    still confuse a future-self developer who greps the tree. Pre-clean
    keeps the workspace tidy across crashes.
    """
    if PYTEST_TMP.exists():
        shutil.rmtree(PYTEST_TMP, ignore_errors=True)
    PYTEST_TMP.mkdir(parents=True, exist_ok=True)
    yield
    if PYTEST_TMP.exists():
        shutil.rmtree(PYTEST_TMP, ignore_errors=True)


@pytest.fixture
def synth_cases(tmp_path: Path) -> Callable[..., Path]:
    """Return a helper that writes a synthetic cases.jsonl inside _pytest_tmp/.

    Filename is hashed from tmp_path so concurrent xdist workers do not
    collide and so a re-run of one test deterministically clobbers its
    own previous file.
    """
    digest = hashlib.sha1(str(tmp_path).encode("utf-8")).hexdigest()[:12]

    def write(lines: list[dict[str, Any]], suffix: str = "") -> Path:
        slug = f"{digest}{suffix}.jsonl"
        path = PYTEST_TMP / slug
        with path.open("w", encoding="utf-8") as f:
            for line in lines:
                f.write(json.dumps(line, ensure_ascii=False) + "\n")
        return path

    return write


@pytest.fixture(scope="session")
def runner_path() -> Path:
    return RUNNER


@pytest.fixture(scope="session")
def oracle_path() -> Path:
    return ORACLE


@pytest.fixture(scope="session")
def python_exe() -> str:
    return sys.executable
