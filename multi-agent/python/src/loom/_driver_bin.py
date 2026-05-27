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
