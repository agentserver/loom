"""Plan-review P1 — env allow-list in harness/env.go MUST NOT change
in this PR. A change belongs in a follow-up worktree with dedicated
security review. Test compares this branch's env.go against the base
branch's env.go.

Round-2 fresh review P0 fix: the earlier regex-based extractor only
matched `map[K]V{...}` shapes (matching `perWorkloadAllowedEnvKeys` but
NOT the two slice literals `alwaysAllowedEnvKeys = []string{...}` /
`alwaysAllowedIfSetEnvKeys = []string{...}`). Both slice-literal
allowlists silently extracted to `<not-found>`, so ANY addition of a
key (e.g., `OPENAI_API_KEY` into the always-forwarded list) would
compare `<not-found> == <not-found>` and pass. This defeated the
entire allowlist-frozen guarantee for the two most dangerous lists.

Fix: use a proper brace-matching extractor that handles BOTH slice
literals (`[]string{...}`) AND map literals (`map[string][]string{...}`),
plus a self-test that tampers with a fixture and asserts the
extractor rejects it.
"""
from __future__ import annotations

import subprocess
from pathlib import Path

from conftest import MODULE_ROOT

BASE_REF = "origin/paper/v3-integration"
ENV_GO = "multi-agent/tests/eval/baselines/harness/env.go"

_KNOWN_LISTS = (
    "alwaysAllowedEnvKeys",
    "alwaysAllowedIfSetEnvKeys",
    "perWorkloadAllowedEnvKeys",
)


def _find_var_body(text: str, name: str) -> str | None:
    """Locate `var? <name> = ...{...}` and return the body between the
    first `{` and its matching `}`. Handles nested braces (needed for
    `map[string][]string{"k": {"v1", "v2"}}`), skips over string
    literals so `"}"` inside a string doesn't confuse the matcher.
    """
    # Anchor on `name` followed by `=` (allow whitespace / linebreaks).
    # `var` may or may not appear on the same line.
    import re
    anchor = re.search(rf"(?:^|\n)\s*(?:var\s+)?{re.escape(name)}\s*=\s*", text)
    if not anchor:
        return None
    i = anchor.end()
    # Advance to the first `{`.
    while i < len(text) and text[i] != "{":
        i += 1
    if i == len(text):
        return None
    depth = 0
    start = i
    in_string = False
    escape = False
    while i < len(text):
        c = text[i]
        if in_string:
            if escape:
                escape = False
            elif c == "\\":
                escape = True
            elif c == '"':
                in_string = False
        else:
            if c == '"':
                in_string = True
            elif c == "{":
                depth += 1
            elif c == "}":
                depth -= 1
                if depth == 0:
                    return text[start + 1 : i]
        i += 1
    return None


def _extract_string_literals(body: str) -> list[str]:
    """Return every double-quoted string literal in `body`. Handles
    escape sequences (skips `\\"` so escaped quotes don't split a
    literal). Sorted for stable comparison.
    """
    out: list[str] = []
    i = 0
    while i < len(body):
        if body[i] == '"':
            j = i + 1
            buf: list[str] = []
            while j < len(body):
                c = body[j]
                if c == "\\" and j + 1 < len(body):
                    buf.append(body[j : j + 2])
                    j += 2
                    continue
                if c == '"':
                    break
                buf.append(c)
                j += 1
            out.append("".join(buf))
            i = j + 1
        else:
            i += 1
    return sorted(out)


def _extract_allowlists(text: str) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    for name in _KNOWN_LISTS:
        body = _find_var_body(text, name)
        if body is None:
            out[name] = ["<not-found>"]
            continue
        out[name] = _extract_string_literals(body)
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


def test_extractor_finds_all_three_lists_non_empty() -> None:
    """Self-test / anti-regression on the extractor. If ANY of the three
    known lists extracts to `<not-found>` on the current source, the
    extractor is broken — silently passing the comparison test above.
    This is the guard that catches the round-1 regex bug where slice
    literals silently returned `<not-found>`.
    """
    repo_root = MODULE_ROOT.parent
    current = _extract_allowlists((repo_root / ENV_GO).read_text())
    for name in _KNOWN_LISTS:
        assert current[name] != ["<not-found>"], (
            f"extractor could not find {name!r} in env.go — the "
            "brace-matching walker is broken. Fix _find_var_body / "
            "_extract_string_literals before trusting this guard."
        )
        assert len(current[name]) > 0, (
            f"extractor found {name!r} but extracted zero strings — "
            "likely a body/quote parsing bug. Inspect the body."
        )


def test_extractor_catches_tampered_fixture(tmp_path: Path) -> None:
    """Guard against a future extractor regression: tamper with a
    fixture, run the extractor on it, assert it detects the added key.
    If this test passes vacuously (i.e., extractor returns empty for
    both current and tampered), the comparison test above is
    silently useless.
    """
    original = """\
package harness

var alwaysAllowedEnvKeys = []string{
\t"PATH",
\t"HOME",
}

var alwaysAllowedIfSetEnvKeys = []string{
\t"AGENTSERVER_ROOT",
}

var perWorkloadAllowedEnvKeys = map[string][]string{
\t"credential-bound-model": {"EXPECTED_MODEL_ALIAS"},
}
"""
    tampered = original.replace(
        '\t"PATH",\n\t"HOME",\n',
        '\t"PATH",\n\t"HOME",\n\t"OPENAI_API_KEY",\n',
    )
    orig_ext = _extract_allowlists(original)
    tamp_ext = _extract_allowlists(tampered)

    # 1. Extractor must find all three lists in both.
    for name in _KNOWN_LISTS:
        assert orig_ext[name] != ["<not-found>"], (
            f"extractor broken on original: {name!r} = <not-found>"
        )
        assert tamp_ext[name] != ["<not-found>"], (
            f"extractor broken on tampered: {name!r} = <not-found>"
        )

    # 2. The added key MUST appear in the tampered extraction.
    assert "OPENAI_API_KEY" in tamp_ext["alwaysAllowedEnvKeys"], (
        f"extractor missed the tampered key OPENAI_API_KEY; "
        f"got {tamp_ext['alwaysAllowedEnvKeys']!r}. This means the "
        "comparison test would pass on a real security regression."
    )

    # 3. And the original and tampered MUST differ overall.
    assert orig_ext != tamp_ext, (
        "extractor returned identical results for original and tampered "
        "source — the anti-drift test is silently useless."
    )
