"""Spec §3 CLI — help / subcommand-registration smoke tests."""

from __future__ import annotations

import subprocess
import sys


def _run(args: list[str]) -> subprocess.CompletedProcess:
    """Invoke the CLI in a subprocess so exit-code assertions are honest."""
    return subprocess.run(
        [sys.executable, "-m", "eval_metrics", *args],
        capture_output=True,
        text=True,
    )


def test_help_smoke() -> None:
    """`--help` prints usage and exits 0 (spec §3)."""
    r = _run(["--help"])
    assert r.returncode == 0
    assert "usage:" in r.stdout.lower()
    assert "extract" in r.stdout


def test_extract_subcommand_registered() -> None:
    """`extract --help` is a recognised subcommand (spec §3)."""
    r = _run(["extract", "--help"])
    assert r.returncode == 0
    assert "--observer-db" in r.stdout
    assert "--format" in r.stdout
    assert "--metric-set" in r.stdout
