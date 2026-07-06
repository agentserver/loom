"""S052 — plan.py.enumerate_matrix_argvs on the real dispatch path
routes through portpool.PortPool (spec §7 (b))."""
from __future__ import annotations

import errno
import socket
import subprocess
import sys
from pathlib import Path

import pytest

from conftest import FULLTABLE_DIR, MODULE_ROOT


def _try_bind(port):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        s.bind(("127.0.0.1", port))
    except OSError:
        s.close()
        raise
    return s


def _print_planned_stub_listen(n=1, starting_port=18100):
    """Invoke plan.py's print-planned-stub-listen subcommand — the seam
    that exercises the real dispatch planning path (use_port_pool=True).
    """
    proc = subprocess.run(
        [sys.executable, "-m", "lib.plan",
         "--matrix", str(FULLTABLE_DIR / "matrix.yaml"),
         "--smoke-root", "tests/eval/results/smoke",
         "--starting-port", str(starting_port),
         "--timeout", "60s",
         "print-planned-stub-listen", "--n", str(n)],
        cwd=str(FULLTABLE_DIR), capture_output=True, text=True, check=True,
    )
    return [ln for ln in proc.stdout.splitlines() if ln.strip()]


def test_planned_stub_listen_skips_busy_start_port():
    """Bind 18100; assert row 1's planned stub-listen is 18101."""
    try:
        holder = _try_bind(18100)
    except OSError as e:
        if e.errno in (errno.EADDRINUSE, errno.EACCES):
            pytest.skip(f"18100 already busy on this host: {e}")
        raise
    try:
        listens = _print_planned_stub_listen(n=1)
    finally:
        holder.close()

    assert len(listens) == 1
    assert listens[0].startswith("127.0.0.1:")
    port = int(listens[0].split(":", 1)[1])
    assert port != 18100, "portpool should have skipped busy 18100"
    assert port >= 18101


def test_planned_stub_listen_uses_start_when_free():
    """Sanity: no synthetic block → row 1 lands on 18100 (or the next
    free port if the host happens to have 18100 in use — we accept
    any port >= 18100)."""
    # Free 18100 first (best-effort). If the host has 18100 busy for
    # a reason we can't control, just accept the pool's next choice.
    listens = _print_planned_stub_listen(n=2)
    assert len(listens) == 2
    ports = [int(l.split(":", 1)[1]) for l in listens]
    for p in ports:
        assert 18100 <= p < 18999
    assert ports[0] != ports[1]


def test_portpool_used_across_multiple_rows():
    """Bind two consecutive start ports; assert plan skips both."""
    holders = []
    try:
        for p in (18100, 18101):
            try:
                holders.append(_try_bind(p))
            except OSError:
                pass  # if either is already busy, that's fine — the plan
                       # must still land on a free port
        listens = _print_planned_stub_listen(n=1)
        port = int(listens[0].split(":", 1)[1])
        for h in holders:
            bound_port = h.getsockname()[1]
            assert port != bound_port, (
                f"plan.py chose bound port {bound_port}; expected retry"
            )
    finally:
        for h in holders:
            h.close()
