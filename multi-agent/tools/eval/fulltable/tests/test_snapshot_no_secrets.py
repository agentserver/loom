"""Global Constraints: regenerated dry_run_snapshot.txt MUST NOT contain
secret-shaped substrings or host/user path patterns. Spec §5
`TestSnapshotHasNoSecrets`.
"""
from __future__ import annotations

import re
from pathlib import Path

from conftest import FULLTABLE_DIR

SNAPSHOT = FULLTABLE_DIR / "tests" / "dry_run_snapshot.txt"

# Ordered from most-common → most-specific so failure message is useful.
LEAK_PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("openai/anthropic sk-", re.compile(r"sk-[A-Za-z0-9_\-]{6,}")),
    # Fresh-review P2 fix: the actual GitHub token prefixes are
    # exactly `ghp_ ghs_ gho_ ghr_ ghu_ ghc_` (lowercase). The earlier
    # class `[opsruA-Z]` folded in every uppercase A-Z and expanded the
    # match past the real grammar (not a bypass, but noisy on hits).
    ("github token", re.compile(r"gh[opsruc]_[A-Za-z0-9]{20,}")),
    ("bearer token", re.compile(r"Bearer\s+[A-Za-z0-9._\-]+", re.IGNORECASE)),
    ("refresh token literal", re.compile(r"refresh_token", re.IGNORECASE)),
    ("root path", re.compile(r"/root/")),
    ("home path with username", re.compile(r"/home/[a-z][a-z0-9_-]*/")),
]


def test_snapshot_has_no_secret_shaped_substrings() -> None:
    text = SNAPSHOT.read_text()
    hits: list[str] = []
    for label, pat in LEAK_PATTERNS:
        for m in pat.finditer(text):
            start = max(0, m.start() - 20)
            end = min(len(text), m.end() + 20)
            hits.append(f"  [{label}] pos {m.start()}: ...{text[start:end]!r}...")
    assert not hits, (
        "dry_run_snapshot.txt contains leak-shaped substring(s):\n"
        + "\n".join(hits)
        + "\nIf a hit is a false positive (e.g. legitimate UUID collision), "
        + "adjust LEAK_PATTERNS with a targeted exception; do NOT weaken the "
        + "generic patterns."
    )


def test_snapshot_has_no_username_or_home_env() -> None:
    """Belt: literal $USER / $HOME shouldn't survive planner string
    substitution either."""
    text = SNAPSHOT.read_text()
    for banned in ("$USER", "$HOME"):
        assert banned not in text, (
            f"literal {banned!r} survived planner substitution; "
            "check plan.py for missed os.path.expanduser or shell-expansion"
        )
