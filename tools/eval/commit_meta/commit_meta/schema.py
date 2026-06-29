"""Pydantic models for the commit-metadata payload.

This module is the contract between Phase 0 WT-0-commit-meta (this package)
and Phase 1 WT-1-run-schema. Downstream code should:

    from commit_meta.schema import CommitMetaSchema

and consume it as-is. Renaming a field here is a coordinated change across
both worktrees.
"""

from __future__ import annotations

import json
from typing import Any

from pydantic import BaseModel, ConfigDict


class OSInfo(BaseModel):
    """Subset of host OS facts that affect agent execution determinism."""

    model_config = ConfigDict(extra="forbid")

    kernel: str
    distro: str
    arch: str


class CommitMetaSchema(BaseModel):
    """Commit SHAs for the four loom-adjacent repos + OS + collection time.

    Missing repos are recorded as ``"N/A: not present at <path>"`` strings
    rather than null — keeping the field types uniform simplifies the eval
    pipeline's schema in Phase 1.
    """

    model_config = ConfigDict(extra="forbid")

    loom_commit: str
    agentserver_commit: str
    modelserver_commit: str
    app_commit: str
    os: OSInfo
    collected_at_unix: int
    machine_hostname: str

    def to_json(self) -> str:
        """Serialize to a JSON string with stable key ordering."""
        # model_dump() gives a dict; json.dumps with sort_keys=False preserves
        # the declared field order so diffs stay legible.
        return json.dumps(self.model_dump(), indent=2)

    @classmethod
    def from_json(cls, data: str | bytes) -> "CommitMetaSchema":
        """Parse a JSON string or bytes into a validated schema instance."""
        payload: Any = json.loads(data)
        return cls.model_validate(payload)
