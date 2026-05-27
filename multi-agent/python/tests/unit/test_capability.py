"""Unit tests for slave discovery + MCP scaffolding helpers."""
import json
from pathlib import Path

import pytest

from loom.capability import _filter_slaves, MCPSpec
from loom.errors import SlaveNotFound, AmbiguousTarget


SAMPLE_AGENTS = [
    {"agent_id": "a1", "display_name": "slave-local-prod",
     "skills": ["chat", "bash", "register_mcp"],
     "mcp_tools": [{"name": "add", "server": "calc"}]},
    {"agent_id": "a2", "display_name": "slave-jetson-prod",
     "skills": ["chat", "bash"],
     "mcp_tools": [{"name": "weather_forecast", "server": "weather"},
                   {"name": "add", "server": "calc"}]},
    {"agent_id": "a3", "display_name": "driver-myhost", "skills": []},
]


def test_filter_by_skill():
    out = _filter_slaves(SAMPLE_AGENTS, skill="register_mcp")
    assert [a["display_name"] for a in out] == ["slave-local-prod"]


def test_filter_by_mcp_tool():
    out = _filter_slaves(SAMPLE_AGENTS, mcp_tool="weather_forecast")
    assert [a["display_name"] for a in out] == ["slave-jetson-prod"]


def test_filter_no_match_returns_empty():
    out = _filter_slaves(SAMPLE_AGENTS, mcp_tool="nonexistent")
    assert out == []


def test_mcpspec_from_dir(tmp_path):
    spec = {"name": "mytool", "tools": [{"name": "do_thing"}]}
    cases = [{"in": {"a": 1}, "expect_substring": "ok"}]
    (tmp_path / "spec.json").write_text(json.dumps(spec))
    (tmp_path / "cases.jsonl").write_text("\n".join(json.dumps(c) for c in cases))
    src_file = tmp_path / "server.py"
    src_file.write_text("# fake server")
    s = MCPSpec.from_dir(tmp_path)
    assert s.spec["name"] == "mytool"
    assert len(s.cases) == 1
    assert s.source_files == [src_file]
