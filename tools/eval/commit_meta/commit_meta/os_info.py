"""Host OS introspection.

Prefers /etc/os-release for the distro name (the freedesktop standard); falls
back to platform.system() so the collector still works on macOS and inside
minimal containers that ship without an os-release file.
"""

from __future__ import annotations

import os
import platform
import shlex
from typing import Dict

# Module-level so tests can monkeypatch to simulate a missing file.
_OS_RELEASE_PATH = "/etc/os-release"


def _read_os_release_distro(path: str) -> str | None:
    """Return the PRETTY_NAME (or NAME) field from /etc/os-release, or None.

    Errors in opening or decoding the file fall back to None so the caller
    can use platform.system() instead; we never raise from here. ``errors=
    "replace"`` keeps stray non-UTF-8 bytes (rare but possible in custom
    container images) from killing the whole collector.
    """
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as fh:
            entries: Dict[str, str] = {}
            for line in fh:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                key, _, raw = line.partition("=")
                # Values may be quoted; shlex handles both quote styles cleanly.
                try:
                    value = shlex.split(raw)[0] if raw else ""
                except ValueError:
                    value = raw
                entries[key.strip()] = value
    except OSError:
        return None
    return entries.get("PRETTY_NAME") or entries.get("NAME") or None


def collect_os_info() -> Dict[str, str]:
    """Return ``{"kernel": ..., "distro": ..., "arch": ...}`` for the host."""
    # Kernel string mirrors `uname -sr` so it's stable and grep-friendly.
    kernel = f"{platform.system()} {platform.release()}".strip()

    distro = _read_os_release_distro(_OS_RELEASE_PATH)
    if not distro:
        # Fallback: platform.system() (e.g. "Darwin", "Linux") — always set.
        distro = platform.system() or "unknown"

    arch = platform.machine() or os.uname().machine or "unknown"

    return {"kernel": kernel, "distro": distro, "arch": arch}
