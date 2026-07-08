"""Wrap the .sh test scripts so pytest discovers + reports them."""
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest


TESTS_DIR = Path(__file__).resolve().parent


@pytest.mark.parametrize("script", [
    "test_workload_filter_semantics.sh",
    "test_sample_cap_with_workload.sh",
    "test_run_sh_duplicate_workload.sh",
    "test_results_root_scoping.sh",
    "test_results_root_scoping_dispatch.sh",
    "test_results_root_symlink_escape.sh",
])
def test_shell_test_passes(script: str) -> None:
    path = TESTS_DIR / script
    assert path.exists(), f"missing {path}"
    proc = subprocess.run(["bash", str(path)], capture_output=True, text=True)
    assert proc.returncode == 0, (
        f"{script} exit={proc.returncode}\n"
        f"stdout={proc.stdout}\nstderr={proc.stderr}"
    )
