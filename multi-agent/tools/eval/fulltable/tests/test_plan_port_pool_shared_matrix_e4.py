"""S057 — matrix and E4 planning share ONE PortPool (spec §7 (b))."""
from __future__ import annotations

import errno
import json
import socket
import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT


def _try_bind(port):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
    try:
        s.bind(("127.0.0.1", port))
    except OSError:
        s.close()
        raise
    s.listen(1)
    return s


def _extract_ports(stdout: str) -> list[int]:
    ports = []
    for line in stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        plan = json.loads(line)
        argv = plan["argv"]
        if "--stub-listen" not in argv:
            continue  # baseline row (none in first 2 matrix rows anyway)
        idx = argv.index("--stub-listen")
        ports.append(int(argv[idx + 1].split(":", 1)[1]))
    return ports


def test_shared_pool_skips_busy_range_across_matrix_and_e4():
    """Bind 18100..18102; assert every planned port is ≥ 18103 and no
    port repeats between matrix and E4 rows."""
    holders = []
    try:
        for p in (18100, 18101, 18102):
            try:
                holders.append(_try_bind(p))
            except OSError as e:
                if e.errno == errno.EADDRINUSE:
                    pytest.skip(f"port {p} already busy on this host")
                raise
        proc = subprocess.run(
            [sys.executable, "-m", "lib.plan",
             "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
             "--smoke-root", "tests/eval/results/smoke",
             "--starting-port", "18100",
             "--timeout", "60s",
             "sample", "--n", "2",
             "--include-e4", "--e4", str(FULLTABLE_DIR / "e4_stages.yaml")],
            cwd=str(FULLTABLE_DIR), capture_output=True, text=True, check=True,
        )
    finally:
        for h in holders:
            h.close()

    ports = _extract_ports(proc.stdout)
    # 2 matrix rows (both full_loom, both use stub) + 2 e4 rows.
    assert len(ports) == 4, f"expected 4 planned stub ports, got {ports}"
    assert len(set(ports)) == 4, f"duplicate ports across scopes: {ports}"
    for p in ports:
        assert p >= 18103, (
            f"planner reused busy port {p}; expected all ≥ 18103; "
            f"full port list {ports}"
        )


def test_shared_pool_no_repeats_when_free():
    """Sanity: no synthetic blocks → matrix + E4 ports are all distinct
    (the offset-hack version would have collided at N-boundaries)."""
    proc = subprocess.run(
        [sys.executable, "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--starting-port", "18500",  # avoid the CI host's 18100 range
         "--timeout", "60s",
         "sample", "--n", "3",
         "--include-e4", "--e4", str(FULLTABLE_DIR / "e4_stages.yaml")],
        cwd=str(FULLTABLE_DIR), capture_output=True, text=True, check=True,
    )
    ports = _extract_ports(proc.stdout)
    assert len(ports) == 6, f"expected 6 ports (3 matrix + 3 e4), got {ports}"
    assert len(set(ports)) == 6, f"duplicate ports: {ports}"
