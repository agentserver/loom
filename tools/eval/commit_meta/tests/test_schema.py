"""Schema round-trip tests.

WT-1-run-schema will import CommitMetaSchema from commit_meta.schema, so the
public surface (field names, optional/required-ness, to_json/from_json) must be
stable across the two worktrees.
"""

from __future__ import annotations

import json

import pytest
from pydantic import ValidationError

from commit_meta.schema import CommitMetaSchema, OSInfo


def _sample_payload() -> dict:
    return {
        "loom_commit": "820430d (paper/v3/p0-commit-meta clean)",
        "agentserver_commit": "deadbee",
        "modelserver_commit": "N/A: not present at /root/modelserver",
        "app_commit": "N/A: not present at /root/app",
        "os": {
            "kernel": "Linux 7.0.9-arch1-1",
            "distro": "Arch Linux",
            "arch": "x86_64",
        },
        "collected_at_unix": 1730000000,
        "machine_hostname": "loom-dev",
    }


def test_pydantic_roundtrip() -> None:
    payload = _sample_payload()
    schema = CommitMetaSchema.from_json(json.dumps(payload))
    assert isinstance(schema.os, OSInfo)
    assert schema.loom_commit == payload["loom_commit"]
    assert schema.os.kernel == payload["os"]["kernel"]
    # Round-trip must preserve every field byte-for-byte.
    roundtripped = json.loads(schema.to_json())
    assert roundtripped == payload


def test_field_names_match_wt1_contract() -> None:
    # Locking the exact field names so WT-1-run-schema can import & consume
    # without rename/migration. If you change a name, change the WT-1 plan too.
    fields = set(CommitMetaSchema.model_fields.keys())
    assert fields == {
        "loom_commit",
        "agentserver_commit",
        "modelserver_commit",
        "app_commit",
        "os",
        "collected_at_unix",
        "machine_hostname",
    }


def test_na_strings_accepted_for_missing_repos() -> None:
    payload = _sample_payload()
    payload["agentserver_commit"] = "N/A: not present at /root/agentserver"
    schema = CommitMetaSchema.from_json(json.dumps(payload))
    assert schema.agentserver_commit.startswith("N/A:")


def test_from_json_rejects_unknown_top_level_field() -> None:
    """extra='forbid' on CommitMetaSchema must block stray top-level keys
    so Phase 1 fails loudly when the contract drifts, not silently.
    """
    payload = _sample_payload()
    payload["surprise_field"] = "should be rejected"
    with pytest.raises(ValidationError):
        CommitMetaSchema.from_json(json.dumps(payload))


def test_from_json_rejects_unknown_os_field() -> None:
    """extra='forbid' on the nested OSInfo must also block stray keys —
    otherwise a renamed/added OS field on one side of the contract goes
    undetected.
    """
    payload = _sample_payload()
    payload["os"]["microarch"] = "znver4"
    with pytest.raises(ValidationError):
        CommitMetaSchema.from_json(json.dumps(payload))
