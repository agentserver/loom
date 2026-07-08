"""Spec §7 (a) — stub bind must be loopback."""
from __future__ import annotations

import pytest

from conftest import FULLTABLE_DIR  # noqa: F401  (sys.path side-effect)
from lib.plan import (
    ErrNonLoopbackStubListen,
    build_stub_listen,
    enumerate_matrix_argvs,
)


def test_loopback_ok():
    assert build_stub_listen(18100) == "127.0.0.1:18100"


def test_localhost_normalises_to_127():
    assert build_stub_listen(18100, host="localhost") == "127.0.0.1:18100"


@pytest.mark.parametrize("host", ["0.0.0.0", "10.0.0.1", "192.168.1.5", "::"])
def test_non_loopback_rejected(host):
    with pytest.raises(ErrNonLoopbackStubListen):
        build_stub_listen(18100, host=host)


def test_planned_matrix_starts_with_loopback(tmp_path):
    from pathlib import Path

    matrix = FULLTABLE_DIR / "matrix.yaml"
    smoke_root = tmp_path / "tests" / "eval" / "results" / "smoke"
    smoke_root.mkdir(parents=True)
    plans = enumerate_matrix_argvs(matrix, smoke_root=smoke_root)
    for plan in plans:
        if plan.configuration in {"manual_ssh", "single_machine_codex",
                                  "cloud_sandbox_e2b"}:
            # Baselines don't take --stub-listen; skip.
            continue
        assert "--stub-listen" in plan.argv
        idx = plan.argv.index("--stub-listen")
        assert plan.argv[idx + 1].startswith("127.0.0.1:"), plan.argv
