# loom Python 库 v0 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a Python package `loom` under `multi-agent/python/` that wraps the driver-agent MCP surface into a fluent workflow API; ≤50 lines of Python should reproduce the existing humanloop e2e case 2 (submit → ask_user → resume → final).

**Architecture:** Sync Python ≥3.10, zero runtime deps (stdlib only). Singleton `_DriverClient` spawns one `driver-agent serve-mcp` subprocess per Python process, talks JSON-RPC 2.0 over stdin/stdout, reused across calls. The fluent `Workflow` context wraps `submit_task` / `wait_task` / `resume_task` / `list_agents` / `register_slave_mcp` / `read_slave_file` / `write_slave_file`. inputs/outputs placeholder syntax (`{input:name}` / `{output:name}`) hides slave filesystem paths entirely. No envelope compilation in v0 — prompts pass through verbatim.

**Tech Stack:** Python 3.10+, stdlib `subprocess` + `json` + `tempfile` + `pathlib`. Build: `setuptools` via `pyproject.toml`. Tests: `pytest` + `pytest-timeout`. Driver dep: `driver-agent` Go binary (located via env var / PATH / repo-local fallback).

**Spec:** [docs/superpowers/specs/2026-05-27-loom-python-library-design.md](../specs/2026-05-27-loom-python-library-design.md)

---

## File Structure

**Created:**

```
multi-agent/python/
├── pyproject.toml
├── README.md
├── src/loom/
│   ├── __init__.py
│   ├── errors.py            # LoomError hierarchy
│   ├── _driver_bin.py       # locate driver-agent binary
│   ├── client.py            # _DriverClient: persistent driver-agent subprocess + JSON-RPC
│   ├── tasks.py             # FutureTask, TaskResult dataclasses
│   ├── humanloop.py         # Question, default_terminal_handler, expect_or_ask loop
│   ├── files.py             # inputs/outputs placeholder substitution, Blob
│   ├── capability.py        # list_slaves, find_slave, MCPSpec, scaffold_and_register
│   └── workflow.py          # Workflow context manager + fluent verbs
└── tests/
    ├── conftest.py          # fixtures: driver_client (real binary), mock_client
    ├── unit/
    │   ├── test_client.py
    │   ├── test_tasks.py
    │   ├── test_humanloop.py
    │   ├── test_files.py
    │   ├── test_capability.py
    │   └── test_workflow.py
    ├── integration/
    │   └── test_driver_roundtrip.py    # real driver-agent against mock-agentserver isn't built; skip if no live fleet
    └── e2e/
        ├── conftest.py                 # assert slave-local-prod visible
        ├── test_case1_happy.py
        ├── test_case2_ask_user.py
        ├── test_case4_request_permission.py
        └── test_case5_jetson.py
```

**Not modified:** No Go code, no other docs in this plan (a separate Task 13 appends a ROADMAP entry).

---

## Task 0: Scaffold package + pyproject + errors + _driver_bin

**Files:**
- Create: `multi-agent/python/pyproject.toml`
- Create: `multi-agent/python/README.md`
- Create: `multi-agent/python/src/loom/__init__.py`
- Create: `multi-agent/python/src/loom/errors.py`
- Create: `multi-agent/python/src/loom/_driver_bin.py`
- Create: `multi-agent/python/tests/conftest.py`
- Create: `multi-agent/python/tests/unit/__init__.py`
- Create: `multi-agent/python/.gitignore`

- [ ] **Step 1: Create `multi-agent/python/pyproject.toml`**

```toml
[build-system]
requires = ["setuptools>=68"]
build-backend = "setuptools.build_meta"

[project]
name = "loom-py"
version = "0.1.0.dev0"
description = "Python client for the loom multi-agent fabric on agentserver"
readme = "README.md"
requires-python = ">=3.10"
license = { text = "Apache-2.0" }
authors = [{ name = "agentserver" }]
dependencies = []  # zero runtime deps; stdlib only

[project.optional-dependencies]
dev = ["pytest>=8.0", "pytest-timeout>=2.3"]

[project.urls]
Homepage = "https://github.com/agentserver/loom"

[tool.setuptools.packages.find]
where = ["src"]

[tool.pytest.ini_options]
testpaths = ["tests"]
addopts = "-ra --strict-markers"
markers = [
  "integration: requires driver-agent binary",
  "e2e: requires live prod_test fleet (slave-local-prod, slave-jetson-prod)",
]
timeout = 60
```

- [ ] **Step 2: Create `multi-agent/python/README.md`**

```markdown
# loom-py

Python client for the [loom](https://github.com/agentserver/loom) multi-agent fabric.

Wraps the `driver-agent` MCP surface as a fluent workflow API. Zero runtime Python
deps; one external dep: the `driver-agent` Go binary on PATH.

## Install (dev)

```bash
pip install -e multi-agent/python
```

## 5-minute quickstart

```python
import loom

with loom.workflow(goal="say hello") as wf:
    res = wf.chat("Reply with the word HELLO and stop.",
                  target="slave-local-prod").wait()
print(res.output)  # "HELLO"
```

See `multi-agent/python/tests/e2e/` for more examples.
```

- [ ] **Step 3: Create `multi-agent/python/.gitignore`**

```
__pycache__/
*.egg-info/
.pytest_cache/
build/
dist/
.venv/
```

- [ ] **Step 4: Create `multi-agent/python/src/loom/errors.py`**

```python
"""Exception hierarchy for the loom client."""
from __future__ import annotations


class LoomError(Exception):
    """Base for all loom-py errors."""


class DriverUnavailable(LoomError):
    """driver-agent subprocess could not be started, or JSON-RPC failed."""


class TaskFailed(LoomError):
    """A task reached terminal state 'failed'."""

    def __init__(self, task_id: str, failure_reason: str, *, target: str | None = None):
        super().__init__(f"task {task_id} failed: {failure_reason}")
        self.task_id = task_id
        self.failure_reason = failure_reason
        self.target = target


class TaskCancelled(LoomError):
    """A task reached terminal state 'cancelled'."""

    def __init__(self, task_id: str):
        super().__init__(f"task {task_id} cancelled")
        self.task_id = task_id


class SessionLost(LoomError):
    """resume_task failed because the backend session no longer exists."""


class AcceptanceFailed(LoomError):
    """scaffold_and_register: the acceptance gate rejected the new MCP."""


class SlaveNotFound(LoomError):
    """find_slave: no agent in the workspace matches the criteria."""


class AmbiguousTarget(LoomError):
    """find_slave: multiple agents match; caller must disambiguate."""

    def __init__(self, candidates: list[str]):
        super().__init__(f"ambiguous target; candidates: {candidates}")
        self.candidates = candidates


class NoInteractiveHandler(LoomError):
    """expect_or_ask was called with the default terminal handler but stdin is not a TTY."""
```

- [ ] **Step 5: Create `multi-agent/python/src/loom/_driver_bin.py`**

```python
"""Locate the driver-agent Go binary."""
from __future__ import annotations

import os
import shutil
from pathlib import Path

from .errors import DriverUnavailable

_REPO_LOCAL_CANDIDATES = (
    "multi-agent/tests/prod_test/bin/driver-agent.linux-amd64",
    "multi-agent/tests/prod_test/bin/driver-agent.linux-arm64",
    "tests/prod_test/bin/driver-agent.linux-amd64",
    "tests/prod_test/bin/driver-agent.linux-arm64",
)


def resolve_driver_bin() -> str:
    """Return absolute path to a driver-agent binary, or raise DriverUnavailable.

    Order:
      1. env var LOOM_DRIVER_BIN
      2. driver-agent on PATH
      3. repo-local prod_test/bin/driver-agent.linux-*
    """
    if env := os.environ.get("LOOM_DRIVER_BIN"):
        if Path(env).is_file():
            return str(Path(env).resolve())
        raise DriverUnavailable(f"LOOM_DRIVER_BIN={env} is not a file")

    if which := shutil.which("driver-agent"):
        return which

    for rel in _REPO_LOCAL_CANDIDATES:
        cwd_attempt = Path.cwd() / rel
        if cwd_attempt.is_file():
            return str(cwd_attempt.resolve())

    raise DriverUnavailable(
        "driver-agent binary not found. Install via the loom Go build, "
        "or set LOOM_DRIVER_BIN=/abs/path/to/driver-agent."
    )


def resolve_driver_cfg() -> str | None:
    """Return absolute path to a driver config.yaml, or None to use default."""
    if env := os.environ.get("LOOM_DRIVER_CFG"):
        return str(Path(env).resolve())
    for rel in ("multi-agent/tests/prod_test/driver/config.yaml",
                "tests/prod_test/driver/config.yaml"):
        p = Path.cwd() / rel
        if p.is_file():
            return str(p.resolve())
    return None
```

- [ ] **Step 6: Create `multi-agent/python/src/loom/__init__.py`** (initial empty exports; populated as later tasks add symbols)

```python
"""loom — Python client for the loom multi-agent fabric on agentserver."""
from .errors import (
    LoomError,
    DriverUnavailable,
    TaskFailed,
    TaskCancelled,
    SessionLost,
    AcceptanceFailed,
    SlaveNotFound,
    AmbiguousTarget,
    NoInteractiveHandler,
)

__version__ = "0.1.0.dev0"
__all__ = [
    "__version__",
    "LoomError",
    "DriverUnavailable",
    "TaskFailed",
    "TaskCancelled",
    "SessionLost",
    "AcceptanceFailed",
    "SlaveNotFound",
    "AmbiguousTarget",
    "NoInteractiveHandler",
]
```

- [ ] **Step 7: Create `multi-agent/python/tests/conftest.py`**

```python
"""Shared pytest fixtures."""
from __future__ import annotations

import os
import pytest


def _have_driver_bin() -> bool:
    try:
        from loom._driver_bin import resolve_driver_bin
        resolve_driver_bin()
        return True
    except Exception:
        return False


def pytest_collection_modifyitems(config, items):
    """Skip integration / e2e if prerequisites missing."""
    have_bin = _have_driver_bin()
    have_e2e = have_bin and bool(os.environ.get("LOOM_E2E_LIVE"))
    skip_integration = pytest.mark.skip(
        reason="driver-agent binary not available (set LOOM_DRIVER_BIN)"
    )
    skip_e2e = pytest.mark.skip(
        reason="e2e disabled (set LOOM_E2E_LIVE=1 + ensure prod_test fleet up)"
    )
    for item in items:
        if "integration" in item.keywords and not have_bin:
            item.add_marker(skip_integration)
        if "e2e" in item.keywords and not have_e2e:
            item.add_marker(skip_e2e)
```

- [ ] **Step 8: Create `multi-agent/python/tests/unit/__init__.py`** (empty file)

```bash
mkdir -p multi-agent/python/tests/unit
touch multi-agent/python/tests/unit/__init__.py
```

- [ ] **Step 9: Install + verify**

```bash
cd multi-agent/python && pip install -e ".[dev]" && python -c "import loom; print(loom.__version__); print(loom.LoomError)"
```

Expected:

```
0.1.0.dev0
<class 'loom.errors.LoomError'>
```

- [ ] **Step 10: Commit**

```bash
git add multi-agent/python/
git commit -m "feat(loom-py): package scaffold (pyproject, errors, _driver_bin)"
```

---

## Task 1: `client.py` — driver-agent subprocess + JSON-RPC

**Files:**
- Create: `multi-agent/python/src/loom/client.py`
- Create: `multi-agent/python/tests/unit/test_client.py`

- [ ] **Step 1: Write the failing tests**

`multi-agent/python/tests/unit/test_client.py`:

```python
"""Unit tests for loom.client (fake subprocess via io.BytesIO)."""
import io
import json

import pytest

from loom.client import _DriverClient, _next_id
from loom.errors import DriverUnavailable


class _FakeProc:
    """Stand-in for subprocess.Popen with stdin/stdout BytesIO and a planned reply."""

    def __init__(self, planned_responses: list[dict]):
        self.stdin = io.BytesIO()
        # Pre-fill stdout with newline-delimited JSON the client expects.
        self.stdout = io.BytesIO(
            b"".join(json.dumps(r).encode() + b"\n" for r in planned_responses)
        )
        self.returncode = None
        self.terminated = False
        self.killed = False

    def poll(self):
        return self.returncode

    def terminate(self):
        self.terminated = True
        self.returncode = 0

    def kill(self):
        self.killed = True
        self.returncode = -9

    def wait(self, timeout=None):
        return self.returncode or 0


def test_next_id_monotonic():
    a = _next_id()
    b = _next_id()
    assert b > a


def test_call_serializes_request_and_parses_response(monkeypatch):
    planned = [
        {"jsonrpc": "2.0", "id": 1, "result": {
            "protocolVersion": "2024-11-05", "capabilities": {},
        }},
        {"jsonrpc": "2.0", "id": 2, "result": {
            "content": [{"type": "text", "text": json.dumps({"agents": []})}]
        }},
    ]
    fake = _FakeProc(planned)
    monkeypatch.setattr("loom.client._spawn_driver", lambda *a, **kw: fake)

    client = _DriverClient(bin_path="/fake/driver-agent")
    out = client.call("list_agents", {})
    assert out == {"agents": []}
    # Request sent on stdin should contain tools/call for list_agents
    sent = fake.stdin.getvalue().decode()
    assert '"method":"initialize"' in sent
    assert '"name":"list_agents"' in sent


def test_call_propagates_jsonrpc_error(monkeypatch):
    planned = [
        {"jsonrpc": "2.0", "id": 1, "result": {
            "protocolVersion": "2024-11-05", "capabilities": {},
        }},
        {"jsonrpc": "2.0", "id": 2, "error": {
            "code": -32000, "message": "task failed something"
        }},
    ]
    fake = _FakeProc(planned)
    monkeypatch.setattr("loom.client._spawn_driver", lambda *a, **kw: fake)

    client = _DriverClient(bin_path="/fake/driver-agent")
    with pytest.raises(DriverUnavailable, match="task failed something"):
        client.call("submit_task", {"prompt": "x"})


def test_singleton_default_instance(monkeypatch):
    """get_client() returns the same instance across calls in one process."""
    monkeypatch.setattr(
        "loom.client.resolve_driver_bin", lambda: "/fake/driver-agent")
    monkeypatch.setattr(
        "loom.client.resolve_driver_cfg", lambda: None)
    monkeypatch.setattr(
        "loom.client._spawn_driver", lambda *a, **kw: _FakeProc([
            {"jsonrpc": "2.0", "id": 1, "result": {}}
        ]))
    # reset module-level singleton
    import loom.client
    loom.client._singleton = None
    a = loom.client.get_client()
    b = loom.client.get_client()
    assert a is b
```

- [ ] **Step 2: Run, expect FAIL (module doesn't exist)**

```bash
cd multi-agent/python && pytest tests/unit/test_client.py -v
```

Expected: `ModuleNotFoundError: No module named 'loom.client'`.

- [ ] **Step 3: Implement `client.py`**

```python
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
        line = (json.dumps(obj) + "\n").encode()
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
        # Loop until we get a response with matching id (initialize may have queued earlier).
        while True:
            resp = self._read_response()
            if resp.get("id") == req_id:
                break
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
```

- [ ] **Step 4: Run, expect PASS**

```bash
cd multi-agent/python && pytest tests/unit/test_client.py -v
```

Expected: 4 passing.

- [ ] **Step 5: Commit**

```bash
git add multi-agent/python/src/loom/client.py multi-agent/python/tests/unit/test_client.py
git commit -m "feat(loom-py): _DriverClient with JSON-RPC over stdin/stdout"
```

---

## Task 2: `tasks.py` — FutureTask + TaskResult dataclasses

**Files:**
- Create: `multi-agent/python/src/loom/tasks.py`
- Create: `multi-agent/python/tests/unit/test_tasks.py`

- [ ] **Step 1: Write failing tests**

`multi-agent/python/tests/unit/test_tasks.py`:

```python
"""Unit tests for FutureTask / TaskResult."""
import pytest

from loom.tasks import TaskResult, FutureTask
from loom.errors import TaskFailed, TaskCancelled


def test_taskresult_completed():
    r = TaskResult(task_id="T1", status="completed", output="HELLO",
                   session_id="S1", outputs={})
    assert r.is_terminal is True
    assert r.is_awaiting is False


def test_taskresult_awaiting_user():
    r = TaskResult(task_id="T1", status="awaiting_user", output="",
                   session_id="S1", outputs={},
                   question={"kind": "ask_user", "question": "pick", "options": ["a", "b"]})
    assert r.is_terminal is True   # terminal for the wait() loop's purposes
    assert r.is_awaiting is True
    assert r.question.kind == "ask_user"
    assert r.question.options == ["a", "b"]


def test_taskresult_failed_raises_on_unwrap():
    r = TaskResult(task_id="T1", status="failed", output="",
                   session_id="", outputs={},
                   failure_reason="claude exit: ...")
    with pytest.raises(TaskFailed) as exc:
        r.unwrap()
    assert exc.value.task_id == "T1"
    assert "claude exit" in str(exc.value)


def test_taskresult_cancelled_raises_on_unwrap():
    r = TaskResult(task_id="T1", status="cancelled", output="",
                   session_id="", outputs={})
    with pytest.raises(TaskCancelled):
        r.unwrap()


def test_taskresult_unwrap_returns_output_when_completed():
    r = TaskResult(task_id="T1", status="completed", output="HELLO",
                   session_id="S1", outputs={})
    assert r.unwrap() == "HELLO"
```

- [ ] **Step 2: Run, expect FAIL**

```bash
cd multi-agent/python && pytest tests/unit/test_tasks.py -v
```

- [ ] **Step 3: Implement `tasks.py`**

```python
"""FutureTask + TaskResult: result objects returned by Workflow.chat/submit.

FutureTask is the in-flight handle that wraps wait/resume. TaskResult is the
terminal snapshot returned by .wait() — including the awaiting_user case,
which is terminal for the polling loop but means "needs human input next".
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from .errors import TaskCancelled, TaskFailed


@dataclass
class Question:
    """Surfaced when a chat task pauses on ask_user / request_permission."""

    kind: str                         # "ask_user" | "request_permission"
    question: str = ""
    options: list[str] = field(default_factory=list)
    context: str = ""
    intent: str = ""
    target: str = ""
    reason: str = ""

    @classmethod
    def from_dict(cls, d: dict) -> "Question":
        return cls(
            kind=d.get("kind", ""),
            question=d.get("question", ""),
            options=list(d.get("options", []) or []),
            context=d.get("context", ""),
            intent=d.get("intent", ""),
            target=d.get("target", ""),
            reason=d.get("reason", ""),
        )


@dataclass
class TaskResult:
    """Snapshot of a task after .wait() returns."""

    task_id: str
    status: str                       # completed | failed | cancelled | awaiting_user
    output: str
    session_id: str
    outputs: dict[str, str]           # logical name -> local path (from write_paths PUT-back)
    failure_reason: str = ""
    question: Question | None = None  # set iff status == "awaiting_user"
    raw: dict = field(default_factory=dict)  # original driver response

    @property
    def is_terminal(self) -> bool:
        return self.status in ("completed", "failed", "cancelled", "awaiting_user")

    @property
    def is_awaiting(self) -> bool:
        return self.status == "awaiting_user"

    def unwrap(self) -> str:
        """Return output if completed; raise TaskFailed / TaskCancelled otherwise."""
        if self.status == "completed":
            return self.output
        if self.status == "failed":
            raise TaskFailed(self.task_id, self.failure_reason or "unknown")
        if self.status == "cancelled":
            raise TaskCancelled(self.task_id)
        # awaiting_user: caller should have handled via expect_or_ask
        raise RuntimeError(
            f"task {self.task_id} is awaiting_user; call .expect_or_ask(handler) before .wait()"
        )

    @classmethod
    def from_driver_response(cls, task_id: str, resp: dict,
                              outputs: dict[str, str] | None = None) -> "TaskResult":
        question = None
        if resp.get("status") == "awaiting_user" and resp.get("question"):
            qd = resp["question"]
            if isinstance(qd, str):
                import json as _json
                try:
                    qd = _json.loads(qd)
                except _json.JSONDecodeError:
                    qd = {"kind": "ask_user", "question": qd}
            question = Question.from_dict(qd)
        return cls(
            task_id=task_id,
            status=resp.get("status", ""),
            output=resp.get("output", "") or resp.get("final_output", ""),
            session_id=resp.get("session_id", ""),
            outputs=outputs or {},
            failure_reason=resp.get("failure_reason", ""),
            question=question,
            raw=resp,
        )


@dataclass
class FutureTask:
    """In-flight handle, returned by Workflow.chat / Workflow.submit.

    Holds the task_id + reference to its workflow so .wait(), .expect_or_ask()
    can dispatch back through the driver client. Not directly instantiated by
    users; created by Workflow methods.
    """

    workflow: "Any"                   # forward-ref to Workflow to avoid cycle
    task_id: str
    target: str
    session_id: str = ""              # captured from submit response
    outputs_pending: dict[str, str] = field(default_factory=dict)
    _question_handler: Any = None

    def expect_or_ask(self, handler: Any = None) -> "FutureTask":
        """Attach a handler that auto-answers awaiting_user pauses during .wait().

        handler signature: (Question) -> str
        If None, uses humanloop.default_terminal_handler.
        """
        self._question_handler = handler
        return self

    def wait(self, timeout: float | None = None) -> TaskResult:
        """Block until the task reaches a terminal state. If a question handler
        was attached, loop awaiting_user → handler → resume_task until completed.
        Delegates to workflow._wait_with_humanloop."""
        return self.workflow._wait_with_humanloop(self, timeout=timeout)
```

- [ ] **Step 4: Run, expect PASS**

```bash
cd multi-agent/python && pytest tests/unit/test_tasks.py -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/python/src/loom/tasks.py multi-agent/python/tests/unit/test_tasks.py
git commit -m "feat(loom-py): FutureTask + TaskResult + Question dataclasses"
```

---

## Task 3: `humanloop.py` — default terminal handler + expect_or_ask loop helper

**Files:**
- Create: `multi-agent/python/src/loom/humanloop.py`
- Create: `multi-agent/python/tests/unit/test_humanloop.py`

- [ ] **Step 1: Write failing tests**

`multi-agent/python/tests/unit/test_humanloop.py`:

```python
"""Unit tests for humanloop default handler + sync-loop helper."""
import io
import sys

import pytest

from loom.humanloop import default_terminal_handler, _ttyish
from loom.tasks import Question
from loom.errors import NoInteractiveHandler


def test_default_handler_ask_user_no_options(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    monkeypatch.setattr("builtins.input", lambda prompt: "blue")
    q = Question(kind="ask_user", question="pick a color")
    assert default_terminal_handler(q) == "blue"


def test_default_handler_ask_user_with_options(monkeypatch, capsys):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    captured_prompt = {}

    def fake_input(prompt):
        captured_prompt["p"] = prompt
        return "red"

    monkeypatch.setattr("builtins.input", fake_input)
    q = Question(kind="ask_user", question="pick", options=["red", "blue"])
    assert default_terminal_handler(q) == "red"
    assert "red/blue" in captured_prompt["p"]


def test_default_handler_request_permission_y(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    monkeypatch.setattr("builtins.input", lambda prompt: "y")
    q = Question(kind="request_permission", intent="run_bash", target="rm -rf /tmp/x")
    assert default_terminal_handler(q) == "approve"


def test_default_handler_request_permission_n(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: True)
    monkeypatch.setattr("builtins.input", lambda prompt: "")
    q = Question(kind="request_permission", intent="run_bash", target="rm -rf /tmp/x")
    assert default_terminal_handler(q) == "deny"


def test_default_handler_no_tty(monkeypatch):
    monkeypatch.setattr("loom.humanloop._ttyish", lambda: False)
    q = Question(kind="ask_user", question="who")
    with pytest.raises(NoInteractiveHandler):
        default_terminal_handler(q)
```

- [ ] **Step 2: Run, expect FAIL**

```bash
cd multi-agent/python && pytest tests/unit/test_humanloop.py -v
```

- [ ] **Step 3: Implement `humanloop.py`**

```python
"""Human-in-the-loop helpers.

default_terminal_handler is the no-arg fallback for FutureTask.expect_or_ask:
reads stdin if it's a TTY, else raises NoInteractiveHandler to force the user
to supply a non-default handler when running unattended.
"""
from __future__ import annotations

import sys

from .errors import NoInteractiveHandler
from .tasks import Question


def _ttyish() -> bool:
    """True iff stdin AND stdout are both TTYs (so input/print make sense)."""
    return (
        hasattr(sys.stdin, "isatty") and sys.stdin.isatty()
        and hasattr(sys.stdout, "isatty") and sys.stdout.isatty()
    )


def default_terminal_handler(q: Question) -> str:
    """Default handler for FutureTask.expect_or_ask() when no handler is passed.

    Reads stdin if both stdin/stdout are TTYs; raises NoInteractiveHandler otherwise.
    """
    if not _ttyish():
        raise NoInteractiveHandler(
            "expect_or_ask was called without a handler but stdin is not a TTY. "
            "Pass a handler explicitly: .expect_or_ask(my_handler) where my_handler "
            "takes a Question and returns a string."
        )
    if q.kind == "ask_user":
        if q.options:
            prompt = f"{q.question} ({'/'.join(q.options)}): "
        else:
            prompt = f"{q.question}: "
        return input(prompt).strip()
    if q.kind == "request_permission":
        reason = f" ({q.reason})" if q.reason else ""
        prompt = f"Approve {q.intent} on {q.target}{reason}? (y/N): "
        return "approve" if input(prompt).strip().lower() == "y" else "deny"
    raise NoInteractiveHandler(f"unknown question kind: {q.kind}")
```

- [ ] **Step 4: Run, expect PASS**

```bash
cd multi-agent/python && pytest tests/unit/test_humanloop.py -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/python/src/loom/humanloop.py multi-agent/python/tests/unit/test_humanloop.py
git commit -m "feat(loom-py): default_terminal_handler + NoInteractiveHandler guard"
```

---

## Task 4: `files.py` — inputs/outputs placeholder substitution + Blob

**Files:**
- Create: `multi-agent/python/src/loom/files.py`
- Create: `multi-agent/python/tests/unit/test_files.py`

- [ ] **Step 1: Write failing tests**

`multi-agent/python/tests/unit/test_files.py`:

```python
"""Unit tests for inputs/outputs placeholder substitution."""
import pytest

from loom.files import substitute_io_placeholders


def test_substitute_input_placeholder():
    prompt = "Read {input:data} and process it."
    inputs = {"data": "/loom/scratch/abc/data.csv"}
    out = substitute_io_placeholders(prompt, inputs=inputs, outputs={})
    assert out == "Read /loom/scratch/abc/data.csv and process it."


def test_substitute_output_placeholder():
    prompt = "Write summary to {output:report}."
    outputs = {"report": "/loom/scratch/abc/report.md"}
    out = substitute_io_placeholders(prompt, inputs={}, outputs=outputs)
    assert out == "Write summary to /loom/scratch/abc/report.md."


def test_substitute_both():
    prompt = "Read {input:a} write {output:b}."
    out = substitute_io_placeholders(
        prompt,
        inputs={"a": "/in/a.txt"},
        outputs={"b": "/out/b.txt"},
    )
    assert out == "Read /in/a.txt write /out/b.txt."


def test_substitute_missing_input_raises():
    prompt = "Read {input:missing}."
    with pytest.raises(KeyError, match="missing"):
        substitute_io_placeholders(prompt, inputs={}, outputs={})


def test_substitute_no_placeholders_passes_through():
    prompt = "no placeholders here"
    assert substitute_io_placeholders(prompt, inputs={}, outputs={}) == prompt
```

- [ ] **Step 2: Run, expect FAIL**

```bash
cd multi-agent/python && pytest tests/unit/test_files.py -v
```

- [ ] **Step 3: Implement `files.py`**

```python
"""inputs/outputs placeholder substitution + Blob abstraction.

User writes prompts with {input:name} / {output:name} placeholders. The library
allocates unique slave-side scratch paths per task and substitutes them in
before sending the prompt. Blob wraps file content returned by read_file.
"""
from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

_PH_INPUT = re.compile(r"\{input:([^}]+)\}")
_PH_OUTPUT = re.compile(r"\{output:([^}]+)\}")


def substitute_io_placeholders(
    prompt: str,
    *,
    inputs: dict[str, str],
    outputs: dict[str, str],
) -> str:
    """Replace {input:NAME} / {output:NAME} placeholders with slave-side paths.

    Raises KeyError if a placeholder references an undeclared name.
    """
    def sub_input(m: re.Match) -> str:
        name = m.group(1)
        if name not in inputs:
            raise KeyError(f"{{input:{name}}} referenced but not in inputs={list(inputs)}")
        return inputs[name]

    def sub_output(m: re.Match) -> str:
        name = m.group(1)
        if name not in outputs:
            raise KeyError(f"{{output:{name}}} referenced but not in outputs={list(outputs)}")
        return outputs[name]

    s = _PH_INPUT.sub(sub_input, prompt)
    s = _PH_OUTPUT.sub(sub_output, s)
    return s


@dataclass
class Blob:
    """File content returned by Workflow.read_file."""

    data: bytes
    slave_path: str = ""

    def bytes(self) -> bytes:
        return self.data

    def text(self, encoding: str = "utf-8") -> str:
        return self.data.decode(encoding)

    def save_to(self, local_path: str | Path) -> str:
        p = Path(local_path)
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(self.data)
        return str(p)
```

- [ ] **Step 4: Run, expect PASS**

```bash
cd multi-agent/python && pytest tests/unit/test_files.py -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/python/src/loom/files.py multi-agent/python/tests/unit/test_files.py
git commit -m "feat(loom-py): substitute_io_placeholders + Blob"
```

---

## Task 5: `capability.py` — list_slaves / find_slave / MCPSpec / scaffold_and_register

**Files:**
- Create: `multi-agent/python/src/loom/capability.py`
- Create: `multi-agent/python/tests/unit/test_capability.py`

- [ ] **Step 1: Write failing tests**

`multi-agent/python/tests/unit/test_capability.py`:

```python
"""Unit tests for slave discovery + MCP scaffolding helpers."""
import json
from pathlib import Path

import pytest

from loom.capability import _filter_slaves, MCPSpec
from loom.errors import SlaveNotFound, AmbiguousTarget


SAMPLE_AGENTS = [
    {"agent_id": "a1", "display_name": "slave-local-prod",
     "skills": ["chat", "bash", "register_mcp"],
     "mcp_tools": [{"name": "add", "server": "calc"}]},
    {"agent_id": "a2", "display_name": "slave-jetson-prod",
     "skills": ["chat", "bash"],
     "mcp_tools": [{"name": "weather_forecast", "server": "weather"},
                   {"name": "add", "server": "calc"}]},
    {"agent_id": "a3", "display_name": "driver-myhost", "skills": []},
]


def test_filter_by_skill():
    out = _filter_slaves(SAMPLE_AGENTS, skill="register_mcp")
    assert [a["display_name"] for a in out] == ["slave-local-prod"]


def test_filter_by_mcp_tool():
    out = _filter_slaves(SAMPLE_AGENTS, mcp_tool="weather_forecast")
    assert [a["display_name"] for a in out] == ["slave-jetson-prod"]


def test_filter_no_match_returns_empty():
    out = _filter_slaves(SAMPLE_AGENTS, mcp_tool="nonexistent")
    assert out == []


def test_mcpspec_from_dir(tmp_path):
    spec = {"name": "mytool", "tools": [{"name": "do_thing"}]}
    cases = [{"in": {"a": 1}, "expect_substring": "ok"}]
    (tmp_path / "spec.json").write_text(json.dumps(spec))
    (tmp_path / "cases.jsonl").write_text("\n".join(json.dumps(c) for c in cases))
    src_file = tmp_path / "server.py"
    src_file.write_text("# fake server")
    s = MCPSpec.from_dir(tmp_path)
    assert s.spec["name"] == "mytool"
    assert len(s.cases) == 1
    assert s.source_files == [src_file]
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Implement `capability.py`**

```python
"""Slave discovery + dynamic MCP registration helpers.

Wraps list_agents / inspect_capabilities / register_slave_mcp into a higher-level
API: find_slave(skill=, mcp_tool=, name=) returns one slave or raises;
MCPSpec.from_dir loads a spec.json + cases.jsonl + source files for register.
"""
from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

from .errors import AmbiguousTarget, SlaveNotFound


def _filter_slaves(
    agents: list[dict],
    *,
    name: str | None = None,
    skill: str | None = None,
    mcp_tool: str | None = None,
) -> list[dict]:
    """Subset of agents matching all given criteria. Drivers (no skills) excluded
    when filtering by skill/mcp_tool."""
    out: list[dict] = []
    for a in agents:
        if name is not None and a.get("display_name") != name:
            continue
        if skill is not None:
            skills = a.get("skills") or []
            if skill not in skills:
                continue
        if mcp_tool is not None:
            tools = a.get("mcp_tools") or []
            if not any(t.get("name") == mcp_tool for t in tools):
                continue
        out.append(a)
    return out


@dataclass
class MCPSpec:
    """Loaded from a directory containing spec.json + cases.jsonl + .py source files.

    spec.json: the register_mcp spec (server name, tool list, schemas).
    cases.jsonl: one JSON test case per line, for the mcp-acceptance gate.
    source_files: list of files to upload to the slave for scaffold.
    """

    spec: dict
    cases: list[dict] = field(default_factory=list)
    source_files: list[Path] = field(default_factory=list)
    spec_dir: Path = field(default_factory=Path)

    @classmethod
    def from_dir(cls, directory: str | Path) -> "MCPSpec":
        d = Path(directory).resolve()
        spec_path = d / "spec.json"
        if not spec_path.is_file():
            raise FileNotFoundError(f"spec.json not found in {d}")
        spec = json.loads(spec_path.read_text())
        cases: list[dict] = []
        cases_path = d / "cases.jsonl"
        if cases_path.is_file():
            for line in cases_path.read_text().splitlines():
                line = line.strip()
                if line:
                    cases.append(json.loads(line))
        sources = sorted(p for p in d.glob("*.py") if p.is_file())
        return cls(spec=spec, cases=cases, source_files=sources, spec_dir=d)
```

- [ ] **Step 4: Run, expect PASS**

```bash
cd multi-agent/python && pytest tests/unit/test_capability.py -v
```

- [ ] **Step 5: Commit**

```bash
git add multi-agent/python/src/loom/capability.py multi-agent/python/tests/unit/test_capability.py
git commit -m "feat(loom-py): _filter_slaves + MCPSpec.from_dir"
```

---

## Task 6: `workflow.py` — Workflow context + fluent verbs (chat/wait/expect_or_ask)

**Files:**
- Create: `multi-agent/python/src/loom/workflow.py`
- Create: `multi-agent/python/tests/unit/test_workflow.py`
- Modify: `multi-agent/python/src/loom/__init__.py` (export workflow / Workflow / Question)

- [ ] **Step 1: Write failing tests**

`multi-agent/python/tests/unit/test_workflow.py`:

```python
"""Unit tests for Workflow / FutureTask end-to-end (with mocked driver client)."""
import pytest

from loom import workflow
from loom.client import _DriverClient, set_client
from loom.errors import SlaveNotFound, AmbiguousTarget


class _MockClient:
    """Replace _DriverClient with scripted call() responses."""

    def __init__(self, script: list[tuple[str, dict]]):
        # script: list of (tool_name_expected, response_dict) in call order
        self.script = list(script)
        self.calls: list[tuple[str, dict]] = []

    def call(self, tool: str, arguments: dict, *, timeout=None):
        self.calls.append((tool, arguments))
        if not self.script:
            raise AssertionError(f"unexpected call: {tool}({arguments})")
        expected_tool, response = self.script.pop(0)
        assert tool == expected_tool, f"expected {expected_tool}, got {tool}"
        return response

    def close(self):
        pass

    def restart(self):
        pass


def test_workflow_chat_happy_path():
    mock = _MockClient([
        ("submit_task", {
            "task_id": "T1", "session_id": "", "target_id": "a1",
            "target_display_name": "slave-A", "manifest": {},
        }),
        ("wait_task", {
            "status": "completed", "output": "HELLO", "is_final": True,
            "final_output": "HELLO",
        }),
    ])
    set_client(mock)
    try:
        with workflow(goal="hi") as wf:
            res = wf.chat("Reply HELLO", target="slave-A").wait()
        assert res.status == "completed"
        assert res.output == "HELLO"
        # Verify submit_task was called with the prompt unchanged
        submit_args = mock.calls[0][1]
        assert submit_args["prompt"] == "Reply HELLO"
        assert submit_args["skill"] == "chat"
        assert submit_args["target_display_name"] == "slave-A"
    finally:
        set_client(None)


def test_workflow_chat_awaiting_user_then_resume():
    mock = _MockClient([
        ("submit_task", {"task_id": "T1", "session_id": "", "target_id": "a1",
                         "target_display_name": "slave-A", "manifest": {}}),
        ("wait_task", {
            "status": "awaiting_user",
            "session_id": "S-1", "current_task_id": "T1", "target_id": "a1",
            "question": {"kind": "ask_user", "question": "pick", "options": ["red", "blue"]},
            "is_final": False,
        }),
        ("resume_task", {
            "status": "completed", "output": "red", "is_final": True,
            "final_output": "red",
        }),
    ])
    set_client(mock)
    try:
        def handler(q):
            assert q.options == ["red", "blue"]
            return "red"
        with workflow(goal="…") as wf:
            res = wf.chat("pick a color", target="slave-A").expect_or_ask(handler).wait()
        assert res.status == "completed"
        assert res.output == "red"
        # Verify resume was called with the answer
        resume_args = mock.calls[2][1]
        assert resume_args["answer"] == "red"
    finally:
        set_client(None)


def test_workflow_find_slave_by_skill_unique():
    mock = _MockClient([
        ("list_agents", {"agents": [
            {"agent_id": "a1", "display_name": "slave-A", "skills": ["chat"]},
            {"agent_id": "a2", "display_name": "slave-B", "skills": ["bash"]},
        ]}),
    ])
    set_client(mock)
    try:
        with workflow(goal="…") as wf:
            name = wf.find_slave(skill="chat")
        assert name == "slave-A"
    finally:
        set_client(None)


def test_workflow_find_slave_none_raises():
    mock = _MockClient([
        ("list_agents", {"agents": [
            {"agent_id": "a1", "display_name": "slave-A", "skills": ["chat"]},
        ]}),
    ])
    set_client(mock)
    try:
        with workflow(goal="…") as wf:
            with pytest.raises(SlaveNotFound):
                wf.find_slave(skill="bash")
    finally:
        set_client(None)


def test_workflow_find_slave_ambiguous_raises():
    mock = _MockClient([
        ("list_agents", {"agents": [
            {"agent_id": "a1", "display_name": "slave-A", "skills": ["chat"]},
            {"agent_id": "a2", "display_name": "slave-B", "skills": ["chat"]},
        ]}),
    ])
    set_client(mock)
    try:
        with workflow(goal="…") as wf:
            with pytest.raises(AmbiguousTarget) as exc:
                wf.find_slave(skill="chat")
        assert set(exc.value.candidates) == {"slave-A", "slave-B"}
    finally:
        set_client(None)
```

- [ ] **Step 2: Run, expect FAIL**

```bash
cd multi-agent/python && pytest tests/unit/test_workflow.py -v
```

- [ ] **Step 3: Implement `workflow.py`**

```python
"""Workflow context manager + fluent verb API.

A Workflow groups one or more tasks under a shared `goal` (Python-side metadata
only; not pushed to wire format in v0 — see spec § 6). Provides:
  - wf.chat(prompt, target, ...) → FutureTask
  - wf.submit(prompt, target, skill=, ...) → FutureTask (general form)
  - wf.list_slaves(skill=, mcp_tool=, name=) → list of display_names
  - wf.find_slave(skill=, mcp_tool=, name=) → unique display_name or raise
  - wf._wait_with_humanloop(future, timeout=) → TaskResult  (used by FutureTask.wait)
"""
from __future__ import annotations

from contextlib import contextmanager
from typing import Any, Callable, Iterator

from .capability import _filter_slaves
from .client import get_client
from .errors import AmbiguousTarget, SlaveNotFound
from .files import substitute_io_placeholders
from .humanloop import default_terminal_handler
from .tasks import FutureTask, Question, TaskResult


class Workflow:
    """Container for a sequence of tasks sharing a goal.

    v0: Python-side metadata only; not encoded into wire format.
    """

    def __init__(
        self,
        goal: str,
        success_criteria: list[str] | None = None,
        *,
        cancel_on_exit: bool = False,
    ):
        self.goal = goal
        self.success_criteria = success_criteria or []
        self.cancel_on_exit = cancel_on_exit
        self._tasks: list[FutureTask] = []
        self._client = get_client()

    def submit(
        self,
        prompt: str,
        target: str,
        *,
        skill: str = "chat",
        inputs: dict[str, str] | None = None,
        outputs: dict[str, str] | None = None,
        timeout_sec: int | None = None,
    ) -> FutureTask:
        """General task submission. Use wf.chat() for the common chat case."""
        in_map = inputs or {}
        out_map = outputs or {}
        # For v0 inputs/outputs are passed through to driver's read_paths/write_paths
        # AND substituted into the prompt. The actual slave-side path comes from
        # driver after submit; v0 substitutes the local path (driver will rewrite
        # internally). This is simpler than two-phase substitution and matches the
        # existing submit_task semantics.
        args: dict[str, Any] = {
            "prompt": substitute_io_placeholders(prompt, inputs=in_map, outputs=out_map),
            "target_display_name": target,
            "skill": skill,
        }
        if in_map:
            args["read_paths"] = list(in_map.values())
        if out_map:
            args["write_paths"] = [{"path": p, "overwrite": True} for p in out_map.values()]
        if timeout_sec is not None:
            args["timeout_sec"] = timeout_sec

        resp = self._client.call("submit_task", args)
        future = FutureTask(
            workflow=self,
            task_id=resp.get("task_id", ""),
            target=target,
            session_id=resp.get("session_id", ""),
            outputs_pending=dict(out_map),
        )
        self._tasks.append(future)
        return future

    def chat(self, prompt: str, target: str, **kw) -> FutureTask:
        return self.submit(prompt, target=target, skill="chat", **kw)

    def list_slaves(
        self,
        *,
        name: str | None = None,
        skill: str | None = None,
        mcp_tool: str | None = None,
    ) -> list[str]:
        """Return display_names of slaves matching all criteria."""
        agents = self._client.call("list_agents", {}).get("agents", [])
        matches = _filter_slaves(agents, name=name, skill=skill, mcp_tool=mcp_tool)
        return [a["display_name"] for a in matches]

    def find_slave(
        self,
        *,
        name: str | None = None,
        skill: str | None = None,
        mcp_tool: str | None = None,
    ) -> str:
        """Return the unique display_name of a matching slave; raise otherwise."""
        names = self.list_slaves(name=name, skill=skill, mcp_tool=mcp_tool)
        if not names:
            criteria = {"name": name, "skill": skill, "mcp_tool": mcp_tool}
            raise SlaveNotFound(f"no slave matches: {criteria}")
        if len(names) > 1:
            raise AmbiguousTarget(names)
        return names[0]

    def _wait_with_humanloop(
        self,
        future: FutureTask,
        *,
        timeout: float | None = None,
    ) -> TaskResult:
        """Block until terminal. If a question handler is attached and an
        awaiting_user is returned, call the handler and resume_task; loop."""
        current_task_id = future.task_id
        while True:
            wait_args = {"task_id": current_task_id}
            if timeout is not None:
                wait_args["timeout_sec"] = int(timeout)
            resp = self._client.call("wait_task", wait_args)
            outputs_resolved = dict(future.outputs_pending)
            result = TaskResult.from_driver_response(
                current_task_id, resp, outputs=outputs_resolved,
            )
            if not result.is_awaiting:
                return result
            # awaiting_user: handler-or-default → resume → loop
            handler = future._question_handler or default_terminal_handler
            answer = handler(result.question)
            # The current task is done; next round polls a new chat_resume task.
            current_task_id = result.task_id  # driver's wait_task returns the same id
            resume_resp = self._client.call("resume_task", {
                "last_task_id": current_task_id,
                "answer": answer,
            })
            # resume_task returns a wait_task-shaped response directly (it blocks
            # internally until the new chat_resume task terminates).
            outputs_resolved = dict(future.outputs_pending)
            result = TaskResult.from_driver_response(
                resume_resp.get("task_id", current_task_id),
                resume_resp,
                outputs=outputs_resolved,
            )
            if not result.is_awaiting:
                return result
            # Multi-round: another awaiting_user; loop with new task_id
            current_task_id = result.task_id


@contextmanager
def workflow(
    goal: str,
    success_criteria: list[str] | None = None,
    *,
    cancel_on_exit: bool = False,
) -> Iterator[Workflow]:
    """Entry point: ``with loom.workflow(goal=...) as wf: ...``."""
    wf = Workflow(goal, success_criteria, cancel_on_exit=cancel_on_exit)
    try:
        yield wf
    finally:
        if cancel_on_exit:
            for ft in wf._tasks:
                try:
                    wf._client.call("cancel_task", {"task_id": ft.task_id})
                except Exception:
                    pass
```

- [ ] **Step 4: Update `__init__.py`**

Replace the existing `multi-agent/python/src/loom/__init__.py` content with:

```python
"""loom — Python client for the loom multi-agent fabric on agentserver."""
from .errors import (
    LoomError,
    DriverUnavailable,
    TaskFailed,
    TaskCancelled,
    SessionLost,
    AcceptanceFailed,
    SlaveNotFound,
    AmbiguousTarget,
    NoInteractiveHandler,
)
from .tasks import FutureTask, TaskResult, Question
from .files import Blob
from .capability import MCPSpec
from .workflow import Workflow, workflow

__version__ = "0.1.0.dev0"
__all__ = [
    "__version__",
    "workflow",
    "Workflow",
    "FutureTask",
    "TaskResult",
    "Question",
    "Blob",
    "MCPSpec",
    "LoomError",
    "DriverUnavailable",
    "TaskFailed",
    "TaskCancelled",
    "SessionLost",
    "AcceptanceFailed",
    "SlaveNotFound",
    "AmbiguousTarget",
    "NoInteractiveHandler",
]
```

- [ ] **Step 5: Run, expect PASS**

```bash
cd multi-agent/python && pytest tests/unit/ -v
```

Expected: all unit tests across `client/tasks/humanloop/files/capability/workflow` pass.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/python/src/loom/workflow.py multi-agent/python/src/loom/__init__.py \
        multi-agent/python/tests/unit/test_workflow.py
git commit -m "feat(loom-py): Workflow context + submit/chat/wait/expect_or_ask/find_slave"
```

---

## Task 7: e2e `test_case1_happy.py` (live prod_test)

**Files:**
- Create: `multi-agent/python/tests/e2e/__init__.py`
- Create: `multi-agent/python/tests/e2e/conftest.py`
- Create: `multi-agent/python/tests/e2e/test_case1_happy.py`

**Prerequisite:** prod_test fleet up; `LOOM_E2E_LIVE=1` env var set; driver-agent binary on PATH or LOOM_DRIVER_BIN; `slave-local-prod` visible in `list_agents`.

- [ ] **Step 1: Create the conftest**

```bash
mkdir -p multi-agent/python/tests/e2e
touch multi-agent/python/tests/e2e/__init__.py
```

`multi-agent/python/tests/e2e/conftest.py`:

```python
"""e2e fixtures: assert prod_test fleet is up."""
import pytest

import loom


@pytest.fixture(scope="session")
def slave_local_prod():
    """Sanity-check that slave-local-prod is visible. Skip if not."""
    with loom.workflow(goal="probe fleet") as wf:
        try:
            names = wf.list_slaves()
        except Exception as e:
            pytest.skip(f"driver not reachable: {e}")
    if "slave-local-prod" not in names:
        pytest.skip(f"slave-local-prod not visible; got {names}")
    return "slave-local-prod"
```

- [ ] **Step 2: Write the test**

`multi-agent/python/tests/e2e/test_case1_happy.py`:

```python
"""e2e case 1: happy chat with humanloop MCP injected but ask_user not called.

Equivalent to multi-agent/tests/humanloop_e2e/scripts/case1_happy.py but written
via loom-py to prove the library covers the same shape in ≤15 lines.
"""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case1_happy(slave_local_prod):
    with loom.workflow(goal="say HELLO") as wf:
        res = wf.chat(
            "Reply with the single word HELLO and stop. Do not call any tool.",
            target=slave_local_prod,
            timeout_sec=180,
        ).wait()
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert "HELLO" in res.output.upper(), f"output: {res.output!r}"
```

- [ ] **Step 3: Run (live)**

```bash
cd multi-agent/python && LOOM_E2E_LIVE=1 pytest tests/e2e/test_case1_happy.py -v -m e2e --timeout=300
```

Expected: PASS (if fleet is up and slave-local-prod connected). If fleet down: SKIPPED with reason.

- [ ] **Step 4: Commit**

```bash
git add multi-agent/python/tests/e2e/
git commit -m "test(loom-py): e2e case 1 (happy chat) via loom workflow API"
```

---

## Task 8: e2e `test_case2_ask_user.py` + `test_case4_request_permission.py`

**Files:**
- Create: `multi-agent/python/tests/e2e/test_case2_ask_user.py`
- Create: `multi-agent/python/tests/e2e/test_case4_request_permission.py`

- [ ] **Step 1: Write case 2**

`multi-agent/python/tests/e2e/test_case2_ask_user.py`:

```python
"""e2e case 2: single ask_user → resume → final.

Auto-answers "blue" via a non-interactive handler so the test doesn't hang
waiting for stdin.
"""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case2_ask_user(slave_local_prod):
    def handler(q: loom.Question) -> str:
        assert q.kind == "ask_user"
        assert "blue" in q.options or len(q.options) == 0
        return "blue"

    with loom.workflow(goal="ask_user round-trip") as wf:
        res = (
            wf.chat(
                'Call the ask_user tool with question="pick a color" '
                'options=["red","blue"]. When you receive the user\'s '
                "answer in your next user turn, reply with exactly that "
                "color word and stop.",
                target=slave_local_prod,
                timeout_sec=180,
            )
            .expect_or_ask(handler)
            .wait()
        )
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert "blue" in res.output.lower(), f"output: {res.output!r}"
```

- [ ] **Step 2: Write case 4**

`multi-agent/python/tests/e2e/test_case4_request_permission.py`:

```python
"""e2e case 4: request_permission marker distinct from ask_user."""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case4_request_permission(slave_local_prod):
    captured: dict = {}

    def handler(q: loom.Question) -> str:
        captured["kind"] = q.kind
        captured["intent"] = q.intent
        captured["target"] = q.target
        return "denied"

    with loom.workflow(goal="request_permission round-trip") as wf:
        res = (
            wf.chat(
                'Call the request_permission tool with intent="run_bash" '
                'target="rm -rf /tmp/some_dir" reason="cleanup". '
                "When you receive the user's answer in your next user turn, "
                "reply with exactly that answer verbatim and stop.",
                target=slave_local_prod,
                timeout_sec=180,
            )
            .expect_or_ask(handler)
            .wait()
        )
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert captured["kind"] == "request_permission"
    assert captured["intent"] == "run_bash"
    assert captured["target"] == "rm -rf /tmp/some_dir"
```

- [ ] **Step 3: Run live**

```bash
cd multi-agent/python && LOOM_E2E_LIVE=1 pytest \
  tests/e2e/test_case2_ask_user.py tests/e2e/test_case4_request_permission.py \
  -v -m e2e --timeout=300
```

- [ ] **Step 4: Commit**

```bash
git add multi-agent/python/tests/e2e/test_case2_ask_user.py \
        multi-agent/python/tests/e2e/test_case4_request_permission.py
git commit -m "test(loom-py): e2e case 2 (ask_user) + case 4 (request_permission)"
```

---

## Task 9: e2e `test_case5_jetson.py`

**Files:**
- Create: `multi-agent/python/tests/e2e/test_case5_jetson.py`

**Prerequisite (in addition to case 1's):** `slave-jetson-prod` re-registered into the active agentserver workspace.

- [ ] **Step 1: Write the test**

`multi-agent/python/tests/e2e/test_case5_jetson.py`:

```python
"""e2e case 5: cross-node — chat with ask_user on the arm64 jetson.

Auto-skipped (not failed) when slave-jetson-prod is not in the workspace.
"""
import pytest

import loom

pytestmark = pytest.mark.e2e


def test_case5_jetson():
    with loom.workflow(goal="cross-node ask_user on jetson") as wf:
        names = wf.list_slaves()
        if "slave-jetson-prod" not in names:
            pytest.skip(
                f"slave-jetson-prod not in current workspace; visible: {names}"
            )
        def handler(q):
            return "blue"
        res = (
            wf.chat(
                'Call ask_user with question="pick a color" '
                'options=["red","blue"]. When you receive the answer, '
                "reply with exactly that color word and stop.",
                target="slave-jetson-prod",
                timeout_sec=480,
            )
            .expect_or_ask(handler)
            .wait()
        )
    assert res.status == "completed", f"got {res.status}: {res.failure_reason}"
    assert "blue" in res.output.lower(), f"output: {res.output!r}"
```

- [ ] **Step 2: Run live**

```bash
cd multi-agent/python && LOOM_E2E_LIVE=1 pytest tests/e2e/test_case5_jetson.py -v -m e2e --timeout=600
```

- [ ] **Step 3: Commit**

```bash
git add multi-agent/python/tests/e2e/test_case5_jetson.py
git commit -m "test(loom-py): e2e case 5 (jetson cross-node arm64)"
```

---

## Task 10: README quickstart polish + roadmap entry

**Files:**
- Modify: `multi-agent/python/README.md`
- Modify: `docs/superpowers/ROADMAP.md`

- [ ] **Step 1: Expand README with 4 quickstart snippets**

Replace `multi-agent/python/README.md` content (built on Task 0's stub):

````markdown
# loom-py

Python client for the [loom](https://github.com/agentserver/loom) multi-agent
fabric. Wraps the `driver-agent` MCP surface as a fluent workflow API. Zero
runtime Python deps; one external dep: the `driver-agent` Go binary.

## Install (dev)

```bash
pip install -e multi-agent/python
```

Then make sure `driver-agent` is reachable via one of:

- `$LOOM_DRIVER_BIN=/abs/path/to/driver-agent`
- `driver-agent` on `$PATH`
- repo-local `multi-agent/tests/prod_test/bin/driver-agent.linux-amd64`

## Quickstart

### 1. Happy chat

```python
import loom

with loom.workflow(goal="say HELLO") as wf:
    res = wf.chat("Reply with HELLO and stop.",
                  target="slave-local-prod").wait()
print(res.output)
```

### 2. Human in the loop (ask_user)

```python
import loom

with loom.workflow(goal="pick a color") as wf:
    res = (
        wf.chat('Call ask_user(question="pick a color", options=["red","blue"]) '
                "then reply with that color.",
                target="slave-local-prod")
          .expect_or_ask()        # default handler reads stdin from terminal
          .wait()
    )
print(res.output)
```

For non-interactive contexts pass a custom handler:

```python
def handler(q: loom.Question) -> str:
    if q.kind == "request_permission":
        return "approve" if policy_check(q) else "deny"
    return ask_my_chat_ui(q.question, q.options)

res = wf.chat(...).expect_or_ask(handler).wait()
```

### 3. Find a capable slave

```python
import loom

with loom.workflow(goal="weather lookup") as wf:
    try:
        slave = wf.find_slave(mcp_tool="weather_forecast")
    except loom.SlaveNotFound:
        slave = wf.find_slave(skill="register_mcp")  # bootstrap
        # ... scaffold + register the MCP, then retry
    res = wf.chat("Will it rain in Beijing tomorrow?", target=slave).wait()
```

### 4. File I/O via placeholders

```python
import loom

with loom.workflow(goal="CSV → summary") as wf:
    res = wf.chat(
        "Read {input:data} and write a summary to {output:report}.",
        target="slave-local-prod",
        inputs={"data": "./data.csv"},
        outputs={"report": "./report.md"},
    ).wait()
print(res.outputs["report"])  # './report.md' (already populated locally)
```

## Status

v0 covers:

- core task semantics (submit / wait / get / cancel)
- humanloop pause/resume (`expect_or_ask`)
- capability discovery + dynamic MCP (`list_slaves` / `find_slave` / `MCPSpec`)
- file I/O via `inputs` / `outputs` placeholders
- workflow context manager with fluent verbs

Not yet (planned for v0.2):

- DAG / fanout
- Codex backend differences
- TASK_CONTRACT envelope compilation (see spec § 6 for trigger criteria)
- retry / metrics / PyPI publish

See `docs/superpowers/specs/2026-05-27-loom-python-library-design.md`.
````

- [ ] **Step 2: Append roadmap entry**

Add to the DONE table in `docs/superpowers/ROADMAP.md` (right after the project-intro-html entry):

```markdown
| [loom-python-library](specs/2026-05-27-loom-python-library-design.md) | [loom-python-library](plans/2026-05-27-loom-python-library.md) | loom Python 库 v0(`multi-agent/python/`;workflow context + fluent verbs + humanloop + 动态 MCP + inputs/outputs;评估"agent 编程语言"组合子形状)|
```

- [ ] **Step 3: Commit**

```bash
git add multi-agent/python/README.md docs/superpowers/ROADMAP.md
git commit -m "docs(loom-py): expanded README + roadmap DONE entry"
```

---

## Done definition

- All 11 tasks (0–10) commit-by-commit complete.
- `cd multi-agent/python && pytest tests/unit/ -v` → all green (no external deps).
- `cd multi-agent/python && LOOM_E2E_LIVE=1 pytest tests/e2e/ -v` → cases 1/2/4 PASS against `slave-local-prod`; case 5 PASS or auto-SKIP depending on jetson availability.
- `python -c "import loom; print(loom.__version__)"` prints `0.1.0.dev0`.
- README quickstart code blocks 1–4 verifiably runnable (paste-and-run in REPL).
- 4 e2e files under `tests/e2e/` each ≤ 30 lines of Python (the v0 success criterion).
- `docs/superpowers/ROADMAP.md` has a DONE entry pointing to this plan + spec.

Hand off to `superpowers:finishing-a-development-branch` to decide on merge / PR.
