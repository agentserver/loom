"""Spec §7 (h) — CI-friendly dry-run snapshot compare."""
from __future__ import annotations

import subprocess
from pathlib import Path

from conftest import FULLTABLE_DIR, MODULE_ROOT

SNAPSHOT = FULLTABLE_DIR / "tests" / "dry_run_snapshot.txt"


def _dry_run_stdout() -> str:
    proc = subprocess.run(
        ["bash", str(FULLTABLE_DIR / "run.sh"), "--dry-run"],
        cwd=str(MODULE_ROOT), capture_output=True, text=True, check=True,
    )
    return proc.stdout


def test_snapshot_matches():
    got = _dry_run_stdout()
    assert got == SNAPSHOT.read_text(), (
        "dry-run output drifted; regenerate with "
        "`bash tools/eval/fulltable/run.sh --dry-run > "
        "tools/eval/fulltable/tests/dry_run_snapshot.txt`"
    )


def test_snapshot_has_60_lines():
    lines = SNAPSHOT.read_text().splitlines()
    assert len(lines) == 60


def test_cloud_rows_have_dry_run():
    lines = SNAPSHOT.read_text().splitlines()
    cloud_lines = [l for l in lines if "cloud_sandbox" in l]
    assert len(cloud_lines) == 5
    for l in cloud_lines:
        assert "--dry-run" in l


def test_baseline_lines_prefix_bash():
    lines = SNAPSHOT.read_text().splitlines()
    baseline_dirs = ("manual_ssh", "single_machine", "cloud_sandbox")
    baseline_lines = [
        l for l in lines
        if any(d in l for d in baseline_dirs)
    ]
    assert len(baseline_lines) == 15  # 3 baselines × 5 workloads
    for l in baseline_lines:
        assert l.startswith("bash tests/eval/baselines/")


def test_ablation_lines_have_ablation_flag():
    lines = SNAPSHOT.read_text().splitlines()
    for l in lines:
        if "--ablation" in l:
            # The flag value should come from the matrix, so one of the 8.
            assert any(f in l for f in (
                "NoCapabilityDiscovery", "NoTypedContracts", "NoDryRun",
                "NoContractFormalization", "NoUserPromotionPath",
                "NoAcceptanceGate", "NoRegistryLookup", "NoObserver",
            ))
