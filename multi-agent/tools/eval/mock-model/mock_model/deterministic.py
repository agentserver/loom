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


def deterministic_content(
    model: str,
    messages: Sequence[Any],
    *,
    prefix: str = DEFAULT_CONTENT_PREFIX,
) -> str:
    """Return `<prefix>[<16 hex chars>]` keyed on (model, messages).

    `messages` is whatever the caller has — typically a list of dicts or
    Pydantic models converted to dicts. We canonicalize via `canonical_json`
    so equal-by-value inputs produce equal digests.
    """
    payload = model + canonical_json(list(messages))
    digest = hashlib.sha256(payload.encode("utf-8")).hexdigest()[:16]
    return f"{prefix}[{digest}]"
