"""Spec §4.4 — E4 --stage runner-flag handoff paragraph."""
from __future__ import annotations

from pathlib import Path

from conftest import MODULE_ROOT, REPO_ROOT

HANDOFF = REPO_ROOT / "docs" / "specs" / "wt3-stub-fulltable.handoff.md"
SMOKE_README = MODULE_ROOT / "tests" / "eval" / "results" / "smoke" / "README.md"


def test_handoff_file_exists():
    assert HANDOFF.exists(), f"expected {HANDOFF}"


def test_handoff_names_stage_flag():
    text = HANDOFF.read_text()
    assert "Stage A/B/C flag handoff to runner" in text


def test_smoke_readme_links_to_handoff():
    """Handoff should be discoverable by grepping the smoke README for
    the handoff filename (spec §4.4 last bullet)."""
    text = SMOKE_README.read_text()
    assert "wt3-stub-fulltable.handoff.md" in text
