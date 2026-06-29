"""Hash-stable content generation for the mock model.

The reply must be a pure function of `(model, messages)` so that
reproducibility runs and ablations produce byte-identical outputs across
process boundaries and across machines.
"""

from __future__ import annotations

import hashlib
import json
from typing import Any, Sequence

DEFAULT_CONTENT_PREFIX = "MOCK"


def canonical_json(value: Any) -> str:
    """JSON-serialize `value` with sorted keys and no whitespace.

    Used as the canonical form fed into the hash so that dict insertion
    order, indentation, or separator choices can't shift the digest.
    """
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def deterministic_digest(model: str, messages: Sequence[Any]) -> str:
    """Return the 16-hex-char sha256 slice of `(model, messages)`.

    Split out so callers (e.g. the server) can reuse the digest as both the
    response `content` body and a stable `id` without re-parsing a formatted
    string. Equal-by-value inputs produce equal digests because we canonicalize
    via `canonical_json`.
    """
    payload = model + canonical_json(list(messages))
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16]


def deterministic_content(
    model: str,
    messages: Sequence[Any],
    *,
    prefix: str = DEFAULT_CONTENT_PREFIX,
) -> str:
    """Return `<prefix>[<16 hex chars>]` keyed on (model, messages).

    `messages` is whatever the caller has — typically a list of dicts or
    Pydantic models converted to dicts.
    """
    return f"{prefix}[{deterministic_digest(model, messages)}]"
