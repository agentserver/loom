"""Port of internal/secretscrub/scrub.go regex set into Python.

Line-by-line mirror of secretRE at internal/secretscrub/scrub.go:46-63.
Do NOT extend this list independently — if scrub.go adds a new family,
update this file in the same PR and bump the SOURCE_SHA reference in a
comment. The tests in tests/test_cloud_upload_secretscrub_wired.py + the
`test_multidevice_dir_no_secrets` grep both rely on this behavior; drift
between the two sanitize implementations shows up as a redaction hit in
one and not the other.

HARNESS-ONLY: this is the Python-side helper used by
`wrappers/cloud_upload.py` (spec §7(d)). Real cloud uploads happen in
the follow-up worktree; here we only prove the wiring is in place by
running the scanner on any file cloud_up.sh would upload.
"""
from __future__ import annotations

import re

# Mirror of internal/secretscrub/scrub.go:46-63 (secretRE). Any
# additions here MUST be mirrored in the Go source.
_SECRET_RE = re.compile(
    r"sk-[A-Za-z0-9_\-]{8,}"
    r"|eyJ[A-Za-z0-9_\-\.]{16,}"
    r"|AKIA[A-Z0-9]{12,}"
    r"|gh[opsruA-Z]_[A-Za-z0-9]{20,}"
    r"|github_pat_[A-Za-z0-9_]{20,}"
    r"|glpat-[A-Za-z0-9_\-]{20,}"
    r"|AIza[A-Za-z0-9_\-]{20,}"
    r"|xox[baprs]-[A-Za-z0-9-]{8,}"
    r"|-----BEGIN [A-Z ]*PRIVATE KEY-----"
)

# Extra patterns caught by the multidevice-level scanner (spec §7(a)
# `test_multidevice_dir_no_secrets`) that scrub.go does not care about
# because they are not seen in observer trace text — but are worth
# blocking from ever being committed here.
_EXTRA_RE = re.compile(
    r"Bearer[\s]+[A-Za-z0-9_\-\.]{20,}|refresh_token"
)


def sanitize(text: str) -> tuple[str, int]:
    """Return (sanitized_text, num_hits).

    A hit is a single regex match. Callers refuse to upload when
    num_hits > 0 (see wrappers/cloud_upload.py).
    """
    if not text:
        return text, 0

    hits = 0

    def _replace(_match: re.Match) -> str:
        nonlocal hits
        hits += 1
        return "[REDACTED]"

    out = _SECRET_RE.sub(_replace, text)
    out = _EXTRA_RE.sub(_replace, out)
    return out, hits
