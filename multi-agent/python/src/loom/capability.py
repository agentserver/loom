"""Slave discovery + dynamic MCP registration helpers.

Wraps list_agents / inspect_capabilities / register_slave_mcp into a higher-level
API: find_slave(skill=, mcp_tool=, name=) returns one slave or raises;
MCPSpec.from_dir loads a spec.json + cases.jsonl + source files for register.
"""
from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

from .errors import AmbiguousTarget, SlaveNotFound


def _filter_slaves(
    agents: list[dict],
    *,
    name: str | None = None,
    skill: str | None = None,
    mcp_tool: str | None = None,
) -> list[dict]:
    """Subset of slave agents matching all given criteria.

    Newer drivers return a role field. When present, only role="slave" is
    accepted; legacy responses without role keep the old criteria-only behavior.
    """
    out: list[dict] = []
    for a in agents:
        role = a.get("role")
        if role is not None and role != "slave":
            continue
        if name is not None and a.get("display_name") != name:
            continue
        if skill is not None:
            skills = a.get("skills") or []
            if skill not in skills:
                continue
        if mcp_tool is not None:
            tools = a.get("mcp_tools") or []
            if not any(t.get("name") == mcp_tool for t in tools):
                continue
        out.append(a)
    return out


@dataclass
class MCPSpec:
    """Loaded from a directory containing spec.json + cases.jsonl + .py source files.

    spec.json: the register_mcp spec (server name, tool list, schemas).
    cases.jsonl: one JSON test case per line, for the mcp-acceptance gate.
    source_files: list of files to upload to the slave for scaffold.
    """

    spec: dict
    cases: list[dict] = field(default_factory=list)
    source_files: list[Path] = field(default_factory=list)
    spec_dir: Path = field(default_factory=Path)

    @classmethod
    def from_dir(cls, directory: str | Path) -> "MCPSpec":
        d = Path(directory).resolve()
        spec_path = d / "spec.json"
        if not spec_path.is_file():
            raise FileNotFoundError(f"spec.json not found in {d}")
        spec = json.loads(spec_path.read_text())
        cases: list[dict] = []
        cases_path = d / "cases.jsonl"
        if cases_path.is_file():
            for line in cases_path.read_text().splitlines():
                line = line.strip()
                if line:
                    cases.append(json.loads(line))
        sources = sorted(p for p in d.glob("*.py") if p.is_file())
        return cls(spec=spec, cases=cases, source_files=sources, spec_dir=d)
