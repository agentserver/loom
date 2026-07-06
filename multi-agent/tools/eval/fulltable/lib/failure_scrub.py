"""Python mirror of `internal/secretscrub/scrub.go` (spec §7 (e)).

Same regex set as the Go source. Replaces every match with the literal
`[REDACTED]` (mirror of secretRE.ReplaceAllStringFunc with a fixed
replacement). `tail_lines(text, n=200)` returns the last N lines of a
stderr blob — the harness records only the tail per failure so a
long spew does not blow up smoke/failures.jsonl.
"""
from __future__ import annotations

import json
import re
from typing import Iterable, List

# secretRE — mirror of internal/secretscrub/scrub.go. Adding a new
# family here MUST also happen in the Go source (the Go source is the
# canonical set — this mirror only covers what the runner would land in
# stderr text).
SECRET_RE = re.compile(
    # OpenAI / Anthropic sk- family (covers sk-ant-...).
    r"sk-[A-Za-z0-9_\-]{8,}|"
    # JWT header.payload.signature.
    r"eyJ[A-Za-z0-9_\-\.]{16,}|"
    # AWS access key.
    r"AKIA[A-Z0-9]{12,}|"
    # GitHub tokens.
    r"gh[opsruA-Z]_[A-Za-z0-9]{20,}|"
    # GitHub fine-grained PAT.
    r"github_pat_[A-Za-z0-9_]{20,}|"
    # GitLab PAT.
    r"glpat-[A-Za-z0-9_\-]{20,}|"
    # Google API keys.
    r"AIza[A-Za-z0-9_\-]{20,}|"
    # Slack tokens.
    r"xox[baprs]-[A-Za-z0-9\-]{8,}|"
    # PEM-armored private keys.
    r"-----BEGIN [A-Z ]*PRIVATE KEY-----|"
    # Bearer-token header value (Python-only extra — internal/secretscrub
    # does not need this because dispatch already redacts headers, but
    # our shim eats free-form stderr which occasionally logs the raw
    # header).
    r"[Bb]earer\s+[A-Za-z0-9_\-\.=]{16,}"
)

TAIL_DEFAULT = 200

# Match the Go source's replacement literal exactly so operators can
# grep either side of the boundary uniformly.
REDACTED = "[REDACTED]"


def sanitize(text: str) -> str:
    """Replace every secret-shaped substring with `[REDACTED]`."""
    if not text:
        return text
    return SECRET_RE.sub(REDACTED, text)


def tail_lines(text: str, n: int = TAIL_DEFAULT) -> List[str]:
    """Return the LAST n lines. Trailing newline is dropped."""
    if not text:
        return []
    lines = text.splitlines()
    if len(lines) <= n:
        return lines
    return lines[-n:]


def scrub_and_tail(text: str, n: int = TAIL_DEFAULT) -> List[str]:
    """Convenience: sanitize the WHOLE input, then tail (keeps the leaked
    token from surviving in dropped lines' offsets)."""
    return tail_lines(sanitize(text), n=n)


def failure_record(
    run_id: str,
    configuration: str,
    workload_id: str,
    exit_code: int,
    stderr_text: str,
    n: int = TAIL_DEFAULT,
) -> dict:
    """Build the JSONL row written to smoke/failures.jsonl (spec §7 (e))."""
    return {
        "run_id": run_id,
        "configuration": configuration,
        "workload_id": workload_id,
        "exit_code": exit_code,
        "tail_stderr": scrub_and_tail(stderr_text, n=n),
    }


def dumps(record: dict) -> str:
    return json.dumps(record, ensure_ascii=False, sort_keys=True)
