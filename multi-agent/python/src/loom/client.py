"""Persistent driver-agent subprocess + JSON-RPC 2.0 over stdin/stdout.

One subprocess per Python process. Singleton via get_client(). atexit hook
terminates the subprocess gracefully on Python exit.
"""
from __future__ import annotations

import atexit
import itertools
import json
import subprocess
import threading
from typing import Any

from .errors import DriverUnavailable
from ._driver_bin import resolve_driver_bin, resolve_driver_cfg

_id_counter = itertools.count(1)
_id_lock = threading.Lock()


def _next_id() -> int:
    with _id_lock:
        return next(_id_counter)


def _spawn_driver(bin_path: str, cfg_path: str | None) -> subprocess.Popen:
    args = [bin_path, "serve-mcp"]
    if cfg_path:
        args += ["--config", cfg_path]
    return subprocess.Popen(
        args,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,  # noisy startup; users can re-enable via env later
        bufsize=0,
    )


class _DriverClient:
    """Singleton-per-process driver MCP client.

    Spawns driver-agent serve-mcp lazily on first call; reuses the same
    subprocess for all subsequent calls in the same Python process. NOT
    thread-safe for concurrent calls — v0 assumes single-threaded use.
    """

    def __init__(self, bin_path: str | None = None, cfg_path: str | None = None):
        self.bin_path = bin_path or resolve_driver_bin()
        self.cfg_path = cfg_path if cfg_path is not None else resolve_driver_cfg()
        self._proc: subprocess.Popen | None = None
        self._initialized = False
        atexit.register(self.close)

    def _ensure_started(self) -> None:
        if self._proc is not None and self._proc.poll() is None:
            return
        self._proc = _spawn_driver(self.bin_path, self.cfg_path)
        # MCP handshake: initialize + initialized notification
        self._send({
            "jsonrpc": "2.0", "id": _next_id(), "method": "initialize",
            "params": {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "loom-py", "version": "0.1.0"},
            },
        })
        _ = self._read_response()  # initialize reply; we don't care about contents
        self._send({
            "jsonrpc": "2.0", "method": "notifications/initialized", "params": {},
        })
        self._initialized = True

    def _send(self, obj: dict) -> None:
        assert self._proc is not None and self._proc.stdin is not None
        line = (json.dumps(obj, separators=(",", ":")) + "\n").encode()
        try:
            self._proc.stdin.write(line)
            self._proc.stdin.flush()
        except BrokenPipeError as e:
            raise DriverUnavailable(f"driver-agent stdin closed: {e}") from e

    def _read_response(self) -> dict:
        assert self._proc is not None and self._proc.stdout is not None
        while True:
            line = self._proc.stdout.readline()
            if not line:
                raise DriverUnavailable("driver-agent stdout closed unexpectedly")
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue  # skip non-JSON noise
            if "id" in obj or "error" in obj:
                return obj

    def call(self, tool: str, arguments: dict, *, timeout: float | None = None) -> Any:
        """Invoke a driver MCP tool. Returns the parsed inner JSON or raises.

        The driver MCP wraps tool output in result.content[0].text (which is itself
        a JSON string). We unwrap that here so callers get a plain dict.
        """
        self._ensure_started()
        req_id = _next_id()
        self._send({
            "jsonrpc": "2.0", "id": req_id, "method": "tools/call",
            "params": {"name": tool, "arguments": arguments},
        })
        # v0 is single-threaded: request/response are strictly ordered, so the
        # next response on stdout is ours. (We don't filter on id because some
        # servers echo arbitrary ids — strict request/response ordering is
        # guaranteed by the MCP stdio contract.)
        resp = self._read_response()
        if "error" in resp:
            err = resp["error"]
            raise DriverUnavailable(f"{tool} error: {err.get('message', err)}")
        text = (
            resp.get("result", {})
            .get("content", [{}])[0]
            .get("text", "")
        )
        if not text:
            return {}
        try:
            return json.loads(text)
        except json.JSONDecodeError:
            return {"_raw": text}

    def close(self) -> None:
        if self._proc is None or self._proc.poll() is not None:
            return
        try:
            self._proc.stdin.close()
        except Exception:
            pass
        try:
            self._proc.wait(timeout=2)
        except subprocess.TimeoutExpired:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self._proc.kill()
                self._proc.wait()

    def restart(self) -> None:
        """Tear down + force re-spawn on next call. Use after recoverable failures."""
        self.close()
        self._proc = None
        self._initialized = False


_singleton: _DriverClient | None = None
_singleton_lock = threading.Lock()


def get_client() -> _DriverClient:
    global _singleton
    with _singleton_lock:
        if _singleton is None:
            _singleton = _DriverClient()
        return _singleton


def set_client(client: _DriverClient | None) -> None:
    """Override the singleton (testing)."""
    global _singleton
    with _singleton_lock:
        _singleton = client
