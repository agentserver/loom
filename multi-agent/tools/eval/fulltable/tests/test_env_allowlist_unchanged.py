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


def _strip_go_comments(text: str) -> str:
    """Replace every `//…\\n` and `/*…*/` comment in `text` with an
    equivalent-length span of spaces (preserving newlines and offsets),
    so a subsequent brace / quote walk sees comments as whitespace.

    Fresh-review r2 P1: without this pre-pass, tampered source like

        alwaysAllowedEnvKeys = []string{
            "PATH",
            // }
            "EVIL_KEY",
        }

    compiles fine in Go (the `}` is inside the comment) but the brace
    walker treated the comment `}` as the real close, silently missing
    `EVIL_KEY`. Same bypass with `/* } */`. String literals are still
    honored (a `//` inside `"…"` is data, not a comment).
    """
    out: list[str] = []
    i = 0
    n = len(text)
    while i < n:
        c = text[i]
        # Enter a string literal — copy verbatim through the terminator.
        if c == '"':
            out.append(c)
            i += 1
            while i < n:
                d = text[i]
                if d == "\\" and i + 1 < n:
                    out.append(text[i : i + 2])
                    i += 2
                    continue
                out.append(d)
                i += 1
                if d == '"':
                    break
            continue
        # Enter a raw string (`…`).
        if c == "`":
            out.append(c)
            i += 1
            while i < n:
                d = text[i]
                out.append(d)
                i += 1
                if d == "`":
                    break
            continue
        # Line comment.
        if c == "/" and i + 1 < n and text[i + 1] == "/":
            # Blank out until end-of-line (keep the newline).
            j = i
            while j < n and text[j] != "\n":
                j += 1
            out.append(" " * (j - i))
            i = j
            continue
        # Block comment.
        if c == "/" and i + 1 < n and text[i + 1] == "*":
            j = i + 2
            while j + 1 < n and not (text[j] == "*" and text[j + 1] == "/"):
                j += 1
            end = min(j + 2, n)
            # Preserve newlines inside the block so line numbers /
            # anchors elsewhere don't shift.
            blanked = "".join(ch if ch == "\n" else " " for ch in text[i:end])
            out.append(blanked)
            i = end
            continue
        out.append(c)
        i += 1
    return "".join(out)


def _find_var_body(text: str, name: str) -> str | None:
    """Locate `var? <name> = ...{...}` and return the body between the
    first `{` and its matching `}`. Handles nested braces (needed for
    `map[string][]string{"k": {"v1", "v2"}}`), skips over string
    literals so `"}"` inside a string doesn't confuse the matcher.

    Fresh-review r2 P1: strips Go comments FIRST so a `}` inside `//`
    or `/* */` cannot terminate the walk early — a specific tamper
    shape that previously slipped past the comparison test.
    """
    import re
    # Strip comments up-front — cannot use the raw source because a
    # `}` inside a `// …` line terminates the walk one step too early.
    scrubbed = _strip_go_comments(text)
    anchor = re.search(rf"(?:^|\n)\s*(?:var\s+)?{re.escape(name)}\s*=\s*", scrubbed)
    if not anchor:
        return None
    i = anchor.end()
    # Advance to the first `{`.
    while i < len(scrubbed) and scrubbed[i] != "{":
        i += 1
    if i == len(scrubbed):
        return None
    depth = 0
    start = i
    in_string = False
    escape = False
    while i < len(scrubbed):
        c = scrubbed[i]
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
                    return scrubbed[start + 1 : i]
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


def test_extractor_catches_tampered_with_brace_in_line_comment() -> None:
    """Fresh-review r2 P1 regression — a Go `//` comment containing a
    `}` used to terminate the brace walker early, silently missing keys
    added AFTER the fake close (Go compiles fine because the `}` is
    inside the comment).
    """
    tampered = """\
package harness

var alwaysAllowedEnvKeys = []string{
\t"PATH",
\t"HOME",
\t// spurious close: }
\t"EVIL_KEY",
}

var alwaysAllowedIfSetEnvKeys = []string{}

var perWorkloadAllowedEnvKeys = map[string][]string{}
"""
    ext = _extract_allowlists(tampered)
    assert "EVIL_KEY" in ext["alwaysAllowedEnvKeys"], (
        f"comment-bypass regression — `// }}` inside the block "
        f"terminated the walker early; got {ext['alwaysAllowedEnvKeys']!r}"
    )


def test_extractor_catches_tampered_with_brace_in_block_comment() -> None:
    """Same regression, block-comment variant: `/* } */` inside the
    block used to fake-close the walker."""
    tampered = """\
package harness

var alwaysAllowedEnvKeys = []string{
\t"PATH",
\t/* spurious } close */
\t"EVIL_KEY",
}

var alwaysAllowedIfSetEnvKeys = []string{}

var perWorkloadAllowedEnvKeys = map[string][]string{}
"""
    ext = _extract_allowlists(tampered)
    assert "EVIL_KEY" in ext["alwaysAllowedEnvKeys"], (
        f"block-comment bypass regression; got {ext['alwaysAllowedEnvKeys']!r}"
    )


def test_extractor_honors_brace_inside_string_literal() -> None:
    """String literals containing `}` must NOT terminate the walk."""
    tampered = """\
package harness

var alwaysAllowedEnvKeys = []string{
\t"PATH",
\t"HAS_BRACE_}",
\t"HOME",
}

var alwaysAllowedIfSetEnvKeys = []string{}

var perWorkloadAllowedEnvKeys = map[string][]string{}
"""
    ext = _extract_allowlists(tampered)
    assert "HAS_BRACE_}" in ext["alwaysAllowedEnvKeys"], (
        f"string-literal `}}` broke the walker; got {ext['alwaysAllowedEnvKeys']!r}"
    )
    # And the walker must have seen HOME (i.e., the string-literal `}`
    # didn't fake-close the block).
    assert "HOME" in ext["alwaysAllowedEnvKeys"], (
        f"walker terminated at string-literal `}}`; missed HOME; "
        f"got {ext['alwaysAllowedEnvKeys']!r}"
    )
