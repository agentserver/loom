"""Spec §7 (b) — port pool starts at 18100 with EADDRINUSE retry."""
from __future__ import annotations

import socket
import threading

import pytest

from lib.portpool import DEFAULT_START, PortPool


def test_default_start_is_18100():
    assert DEFAULT_START == 18100


def test_assigns_from_start():
    calls = []

    def probe(port):
        calls.append(port)
        return True

    pool = PortPool(try_bind=probe)
    assert pool.assign_port() == 18100
    assert pool.assign_port() == 18101


def test_advances_past_in_use():
    """A synthetic in-use bind on 18100 forces the pool to try 18101."""
    in_use = {18100}

    def probe(port):
        return port not in in_use

    pool = PortPool(try_bind=probe)
    assert pool.assign_port() == 18101
    assert pool.assign_port() == 18102


def test_two_concurrent_assign_returns_distinct():
    """Same-process concurrency: no two threads see the same port."""
    def probe(port):
        return True

    pool = PortPool(try_bind=probe)
    ports = []
    lock = threading.Lock()

    def worker():
        p = pool.assign_port()
        with lock:
            ports.append(p)

    threads = [threading.Thread(target=worker) for _ in range(16)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert len(set(ports)) == 16


def test_pool_exhausted_raises():
    def probe(port):
        return False  # everything busy

    pool = PortPool(start=18100, end=18103, try_bind=probe)
    with pytest.raises(RuntimeError):
        pool.assign_port()


def test_real_bind_probe_works():
    """Sanity: the default probe actually binds a port on 127.0.0.1."""
    # Grab an ephemeral port from the OS, keep it bound, then verify the
    # pool skips it.
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        s.bind(("127.0.0.1", 0))
        blocked = s.getsockname()[1]
        # Point the pool at the blocked port + 1 to keep the test fast.
        pool = PortPool(start=blocked, end=blocked + 5)
        p = pool.assign_port()
        assert p != blocked, "should have skipped bound port"
