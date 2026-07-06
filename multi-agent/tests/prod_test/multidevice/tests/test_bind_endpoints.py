"""Bind-endpoint parser tests. Covers spec §7(b)."""
from __future__ import annotations

import pathlib
import re
import urllib.parse

import pytest

yaml = pytest.importorskip("yaml")

_LOOPBACK_HOSTS = {"127.0.0.1", "::1", "localhost"}


def _parse_endpoint(raw: str) -> tuple[str, int]:
    """Parse a `<host>:<port>` string into (host, port). Raises ValueError
    on malformed input."""
    # Add scheme so urllib.parse understands it.
    parsed = urllib.parse.urlparse(f"http://{raw}")
    if parsed.hostname is None:
        raise ValueError(f"no hostname parsed from {raw!r}")
    if parsed.port is None:
        raise ValueError(f"no port parsed from {raw!r}")
    return parsed.hostname, parsed.port


def _is_loopback_bind(raw: str) -> tuple[bool, str]:
    """Return (accepted, reason).

    Rejects wildcards (`0.0.0.0`, `[::]`, empty host) and non-loopback
    hosts (external IPs, arbitrary DNS names).
    """
    if not raw or not raw.strip():
        return False, "empty endpoint"
    stripped = raw.strip()
    if stripped.startswith(":"):
        return False, "bare :PORT is a wildcard bind"
    try:
        host, _port = _parse_endpoint(stripped)
    except ValueError as exc:
        return False, str(exc)
    if host in {"0.0.0.0", "::", ""}:
        return False, f"host {host!r} is a wildcard bind"
    if host in _LOOPBACK_HOSTS:
        return True, "loopback"
    return False, f"host {host!r} is not loopback ({_LOOPBACK_HOSTS})"


def test_bind_endpoints_parser_positive_and_negative():
    """Positive: 127.0.0.1, ::1, localhost with port. Negative: 0.0.0.0,
    bare :PORT, [::], external IP."""
    good = [
        "127.0.0.1:18091",
        "127.0.0.1:18092",
        "localhost:18093",
        "[::1]:18094",
    ]
    for g in good:
        accepted, reason = _is_loopback_bind(g)
        assert accepted, f"{g!r} should be accepted (reason: {reason})"

    bad = [
        "0.0.0.0:18091",
        ":18091",
        "[::]:18091",
        "10.0.0.5:18091",
        "203.0.113.5:18091",
    ]
    for b in bad:
        accepted, reason = _is_loopback_bind(b)
        assert not accepted, f"{b!r} should be REJECTED (accepted with reason={reason})"


def _extract_bind_values(text: str) -> list[str]:
    """Grep-style: pick up `listen:` / `bind:` / `endpoint:` values
    from a YAML/shell file."""
    out: list[str] = []
    for m in re.finditer(
        r"(?:listen|bind|endpoint)\s*[:=]\s*['\"]?([^'\"\s#]+)",
        text,
    ):
        out.append(m.group(1))
    return out


def test_all_template_and_sample_binds_are_loopback(multidevice_dir):
    """Every listen/bind value in templates + samples must parse loopback."""
    files: list[pathlib.Path] = []
    files.extend(multidevice_dir.glob("*.yaml.template"))
    files.extend((multidevice_dir / "topologies").glob("*.yaml.sample"))
    for f in files:
        text = f.read_text()
        binds = _extract_bind_values(text)
        for raw in binds:
            accepted, reason = _is_loopback_bind(raw)
            assert accepted, f"{f.name}: bind {raw!r} rejected: {reason}"


def test_wrappers_only_contain_loopback_binds(multidevice_dir):
    """Wrappers pass literal port-only flags to Phase 2 scripts; they
    should never contain non-loopback host addresses (0.0.0.0, external IP)."""
    for wrapper in [
        multidevice_dir / "wrappers" / "laptop_up.sh",
        multidevice_dir / "wrappers" / "headless_up.sh",
        multidevice_dir / "wrappers" / "cloud_up.sh",
        multidevice_dir / "wrappers" / "windows_up.ps1",
    ]:
        text = wrapper.read_text()
        assert "0.0.0.0:" not in text, f"{wrapper}: 0.0.0.0 bind forbidden"
        assert "[::]:" not in text, f"{wrapper}: [::] bind forbidden"


def test_bind_endpoints_parser_rejects_injected_bad_values(tmp_path):
    """Positive control: parser catches every disallowed form."""
    injected = [
        "0.0.0.0:18091",
        ":18091",
        "[::]:18091",
        "192.168.1.7:18091",
    ]
    for inj in injected:
        accepted, _ = _is_loopback_bind(inj)
        assert not accepted, f"injected {inj!r} slipped past the parser"
