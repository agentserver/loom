"""Wrapper static-analysis tests.

Covers spec §7(c) (Windows ExecutionPolicy scope) and §7(k)
(ALLOW_PROD_DEPLOY gate).
"""
from __future__ import annotations

import re


def test_windows_execpolicy_scope_process(multidevice_dir):
    """Every Set-ExecutionPolicy line's -Scope value literally equals
    Process; no LocalMachine / CurrentUser."""
    ps1_files = list(multidevice_dir.rglob("*.ps1"))
    assert ps1_files, "no ps1 files found under multidevice/"
    for ps1 in ps1_files:
        text = ps1.read_text()
        # There must be at least one Set-ExecutionPolicy line per ps1
        # that touches deploy.ps1 subprocess.
        matches = re.findall(r"Set-ExecutionPolicy[^\n]+", text)
        assert matches, f"{ps1}: no Set-ExecutionPolicy line found"
        for m in matches:
            assert re.search(r"-Scope\s+Process\b", m), \
                f"{ps1}: Set-ExecutionPolicy without -Scope Process: {m!r}"
            assert "LocalMachine" not in m, \
                f"{ps1}: LocalMachine scope forbidden: {m!r}"
            assert "CurrentUser" not in m, \
                f"{ps1}: CurrentUser scope forbidden: {m!r}"


def _wrappers(multidevice_dir):
    yield multidevice_dir / "wrappers" / "laptop_up.sh"
    yield multidevice_dir / "wrappers" / "headless_up.sh"
    yield multidevice_dir / "wrappers" / "cloud_up.sh"
    yield multidevice_dir / "wrappers" / "windows_up.ps1"


def _strip_comments(text: str, is_ps1: bool) -> str:
    """Drop shell / powershell comment lines so the mode-prod scan
    doesn't false-positive on documentation."""
    out_lines: list[str] = []
    for line in text.splitlines():
        stripped = line.lstrip()
        # Both bash and pwsh use `#` for line comments; pwsh also
        # supports `<# ... #>` block comments but our wrappers do
        # not use those.
        if stripped.startswith("#"):
            continue
        # Drop inline `#` comments (safe for the substrings we scan
        # since we never quote a `#` inside a bind literal here).
        if is_ps1:
            # PowerShell `#` inline comments — but a `$env:` var name
            # can contain `#` in principle; ours don't, so this simple
            # split is safe for the current wrappers.
            hash_pos = line.find(" #")
        else:
            hash_pos = line.find(" #")
        if hash_pos >= 0:
            line = line[:hash_pos]
        out_lines.append(line)
    return "\n".join(out_lines)


def test_no_prod_deploy_without_env(multidevice_dir):
    """Spec §7(k): every wrapper must gate any `--mode prod` or
    `-Mode prod` invocation on ALLOW_PROD_DEPLOY=1."""
    for wrapper in _wrappers(multidevice_dir):
        raw = wrapper.read_text()
        # If a wrapper never mentions prod at all, that's fine.
        if "prod" not in raw.lower():
            continue
        # Strip comment lines / inline comments before the executable
        # scan — comments legitimately reference `--mode prod` in
        # documentation.
        code = _strip_comments(raw, is_ps1=wrapper.suffix == ".ps1")
        # Any `prod` mode invocation must go through the $MODE /
        # $Mode variable, not a literal `--mode prod` argument.
        assert not re.search(r"--mode\s+prod\b", code), \
            f"{wrapper}: literal `--mode prod` in executable code; must go through $MODE variable"
        assert not re.search(r"-Mode\s+prod\b", code), \
            f"{wrapper}: literal `-Mode prod` in executable code; must go through $Mode variable"
        # The gate itself must exist in any wrapper that references prod.
        assert "ALLOW_PROD_DEPLOY" in raw, \
            f"{wrapper}: mentions prod but has no ALLOW_PROD_DEPLOY gate"


def test_dry_run_all_never_sets_allow_prod_deploy(multidevice_dir):
    """Spec §7(k): dry_run_all.sh MUST NOT set ALLOW_PROD_DEPLOY."""
    dry = (multidevice_dir / "dry_run_all.sh").read_text()
    assert "ALLOW_PROD_DEPLOY=" not in dry, \
        "dry_run_all.sh must never assign ALLOW_PROD_DEPLOY"
