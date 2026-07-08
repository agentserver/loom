"""Plan-review P1 — env allow-list in harness/env.go MUST NOT change
in this PR. A change belongs in a follow-up worktree with dedicated
security review. Test compares this branch's env.go against the base
branch's env.go."""
from __future__ import annotations

import re
import subprocess
from pathlib import Path

from conftest import MODULE_ROOT

BASE_REF = "origin/paper/v3-integration"
ENV_GO = "multi-agent/tests/eval/baselines/harness/env.go"


def _extract_allowlists(text: str) -> dict[str, list[str]]:
    """Return dict of allowlist-name → sorted contents. Tolerant of
    formatting drift — walks the source looking for the known var
    names + their `{...}` block."""
    out = {}
    for name in ("alwaysAllowedEnvKeys",
                 "alwaysAllowedIfSetEnvKeys",
                 "perWorkloadAllowedEnvKeys"):
        m = re.search(rf"{name}\s*=\s*(?:map\[[^\]]+\][^{{]*)?{{([^}}]*)}}", text, re.S)
        if not m:
            out[name] = ["<not-found>"]
            continue
        body = m.group(1)
        strs = re.findall(r'"([^"]+)"', body)
        out[name] = sorted(strs)
    return out


def test_env_allowlists_unchanged() -> None:
    repo_root = MODULE_ROOT.parent
    current_text = (repo_root / ENV_GO).read_text()
    base_text = subprocess.run(
        ["git", "-C", str(repo_root), "show", f"{BASE_REF}:{ENV_GO}"],
        capture_output=True, text=True, check=True,
    ).stdout
    current = _extract_allowlists(current_text)
    base = _extract_allowlists(base_text)
    assert current == base, (
        "env allowlist changed in this PR — this is out-of-scope for "
        "wt4-codex-only per spec Global Constraints. Move the change "
        "to a follow-up worktree with its own review.\n"
        f"current: {current}\n"
        f"base:    {base}"
    )
