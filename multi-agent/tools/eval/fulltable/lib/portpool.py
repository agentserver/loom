"""Loopback port allocator with EADDRINUSE retry (spec §7 (b)).

Contiguous range starting at 18100 — chosen so it does not overlap the
runner's default 18080 or the Phase 2 stub tests. Single-process
allocator: for `--parallel N` inside one `run.sh` invocation the
shared instance hands out distinct ports; across processes the OS
bind check provides the collision guard.
"""
from __future__ import annotations

import errno
import socket
import threading
from typing import Callable, Iterator, Optional

DEFAULT_START = 18100


class PortPool:
    """Sequentially probes ports starting at `start`; skips any in-use."""

    def __init__(
        self,
        start: int = DEFAULT_START,
        end: int = 18999,
        *,
        try_bind: Optional[Callable[[int], bool]] = None,
    ):
        if start < 1024 or end > 65535 or end <= start:
            raise ValueError(f"invalid port range [{start}, {end})")
        self._start = start
        self._end = end
        self._next = start
        self._lock = threading.Lock()
        self._try_bind = try_bind or _default_try_bind

    def assign_port(self) -> int:
        with self._lock:
            first_probed = self._next
            while self._next < self._end:
                port = self._next
                self._next += 1
                if self._try_bind(port):
                    return port
            raise RuntimeError(
                f"port pool exhausted starting from {first_probed}"
            )


def _default_try_bind(port: int) -> bool:
    """Attempt a bind on 127.0.0.1:port with SO_REUSEADDR=0 so a stale
    TIME_WAIT socket (or another live listener) fails us cleanly.

    Returns True on success (port is free), False on EADDRINUSE. Any
    other OSError propagates — we want to see permission denied /
    address-family errors, not silently skip past them.
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        # Force SO_REUSEADDR off so EADDRINUSE fires reliably; getsockopt
        # to test first would race against a concurrent bind.
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 0)
        s.bind(("127.0.0.1", port))
    except OSError as e:
        if e.errno == errno.EADDRINUSE:
            return False
        raise
    finally:
        s.close()
    return True
