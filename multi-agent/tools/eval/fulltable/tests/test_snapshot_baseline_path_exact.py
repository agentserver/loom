"""Spec §5 `TestSnapshotBaselinePathExact` — resolves spec-review P2#3
(upgraded to acceptance test). `single_machine_codex` matches the
substring `single_machine`, so a broken path
`tests/eval/baselines/single_machine/run.sh` (old Claude dir) could
regress silently. This test asserts the EXACT baseline `run.sh` path
appears on the 5 baseline lines.
"""
from __future__ import annotations

from conftest import FULLTABLE_DIR

SNAPSHOT = FULLTABLE_DIR / "tests" / "dry_run_snapshot.txt"

EXPECTED_BASELINE_PATH = "tests/eval/baselines/single_machine_codex/run.sh"
FORBIDDEN_LEGACY_PATH = "tests/eval/baselines/single_machine/run.sh"


def test_snapshot_uses_codex_baseline_path() -> None:
    text = SNAPSHOT.read_text()
    count = text.count(EXPECTED_BASELINE_PATH)
    assert count == 5, (
        f"expected exactly 5 lines invoking {EXPECTED_BASELINE_PATH}, "
        f"found {count}. matrix.yaml should have 5 codex baseline rows."
    )


def test_snapshot_does_not_use_legacy_claude_baseline_path() -> None:
    text = SNAPSHOT.read_text()
    assert FORBIDDEN_LEGACY_PATH not in text, (
        f"legacy Claude baseline path {FORBIDDEN_LEGACY_PATH} appears in "
        "snapshot; the enum swap (Task 6-7) or plan.py BASELINE_DIR "
        "(Task 7) is incomplete."
    )
