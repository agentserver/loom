"""Spec §7 (j) — smoke README warning + `_sample.*` suffix rule."""
from __future__ import annotations

from pathlib import Path

from conftest import MODULE_ROOT

SMOKE_ROOT = MODULE_ROOT / "tests" / "eval" / "results" / "smoke"
README = SMOKE_ROOT / "README.md"
PAPER = SMOKE_ROOT / "paper"

WARNING_LINES = (
    "> **本目录数据是脚手架 smoke，非论文用真数据；60 run 真跑归后续",
    "> `paper/v3/p3-stub-fulltable-run` worktree。**",
)


def test_readme_warning_verbatim():
    text = README.read_text()
    joined = "\n".join(WARNING_LINES)
    assert joined in text, (
        "smoke README missing the spec §7 (j) blockquote verbatim; "
        f"expected:\n{joined}"
    )


def test_readme_first_two_lines():
    lines = README.read_text().splitlines()
    assert lines[0] == WARNING_LINES[0]
    assert lines[1] == WARNING_LINES[1]


def test_every_paper_artifact_has_sample_suffix():
    files = [p for p in PAPER.iterdir() if p.is_file()]
    # `.gitignore` under smoke/ is fine; only paper/ contents are checked.
    for p in files:
        assert (p.name.endswith("_sample.csv")
                or p.name.endswith("_sample.json")), (
            f"paper artefact {p.name} does not end in _sample.{{csv,json}}"
        )
