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
