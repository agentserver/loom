"""JSON serializer (spec §3.3 JSON).

Single JSON object; keys emitted in fixed order
`{metric_set, row_count, metrics, notes}` — the object-key order
matters to reviewers who eyeball the file and to tests that assert on
key-order. Python 3.7+ dicts preserve insertion order, so a plain
`json.dumps(dict_in_order)` is enough.

`notes` is ALWAYS present, even when empty (`{}`), per spec §3.3
"downstream consumers can rely on the key being present".
"""

from __future__ import annotations

import io
import json
from typing import Any

from eval_metrics.metrics import Metric, MetricResult


def build_json_object(
    metric_set_value: str,
    row_count: int,
    metrics: list[Metric],
    results: dict[str, MetricResult],
) -> dict[str, Any]:
    """Return the JSON-serialisable dict for the invocation.

    Structured metric values land as nested dicts; the full subkey set
    is always present, with `None` for missing subkeys (mirrors CSV
    empty-cell semantics).

    Scalar `None` values also stay `None` — jq / json.loads see this as
    `null`, which is exactly the spec §7 (f) requirement.
    """
    metrics_obj: dict[str, Any] = {}
    notes_obj: dict[str, str] = {}

    for m in metrics:
        res = results[m.name]
        if m.is_structured:
            # Enforce subkey-order stability by rebuilding from m.subkeys
            # even if the compute closure returned a dict with a
            # different key order.
            metrics_obj[m.name] = {k: res.value.get(k) for k in m.subkeys}
        else:
            metrics_obj[m.name] = res.value
        if res.note is not None:
            notes_obj[m.name] = res.note

    # Fixed key order per spec §3.3.
    return {
        "metric_set": metric_set_value,
        "row_count": row_count,
        "metrics": metrics_obj,
        "notes": notes_obj,
    }


def write_json(
    fp: io.TextIOBase,
    metric_set_value: str,
    row_count: int,
    metrics: list[Metric],
    results: dict[str, MetricResult],
) -> None:
    """Write the JSON object followed by a trailing newline."""
    obj = build_json_object(metric_set_value, row_count, metrics, results)
    # indent=2 makes the output diff-friendly and matches the CSV row's
    # human-readability goal; sort_keys=False keeps our chosen order.
    json.dump(obj, fp, indent=2, sort_keys=False)
    fp.write("\n")
