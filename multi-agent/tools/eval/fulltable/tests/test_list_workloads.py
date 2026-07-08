"""--list-workloads emits the 5 ids newline-separated. Consumed by
run.sh for allowlist derivation (avoid literal duplication)."""
from __future__ import annotations

import subprocess
from conftest import FULLTABLE_DIR, MODULE_ROOT


def test_list_workloads_prints_five_ids() -> None:
    proc = subprocess.run(
        ["python3", "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--list-workloads"],
        cwd=str(MODULE_ROOT), env={"PYTHONPATH": str(FULLTABLE_DIR), "PATH": "/usr/bin:/bin"},
        capture_output=True, text=True, check=True,
    )
    ids = [l for l in proc.stdout.splitlines() if l.strip()]
    assert set(ids) == {
        "cross-device-code-mod", "remote-data-processing",
        "windows-only-artifact", "missing-parser-converter",
        "credential-bound-model",
    }
    assert len(ids) == 5  # deduped
