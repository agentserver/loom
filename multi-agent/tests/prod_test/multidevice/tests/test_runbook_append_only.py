"""RUNBOOK append-only tests. Covers spec §4."""
from __future__ import annotations

import subprocess

_MARKER = "## Multi-device deployment (§C5 smoke)"


def _base_runbook_bytes(worktree_root) -> bytes:
    proc = subprocess.run(
        ["git", "show",
         "origin/paper/v3-integration:multi-agent/tests/prod_test/E2E_RUNBOOK.md"],
        cwd=str(worktree_root),
        capture_output=True,
        check=True,
    )
    return proc.stdout


def test_runbook_host_mode_bytes_unchanged(runbook_path, worktree_root):
    """The prefix of E2E_RUNBOOK.md up to the '## Multi-device deployment
    (§C5 smoke)' marker must match the base commit's version byte-for-byte."""
    base = _base_runbook_bytes(worktree_root)
    current = runbook_path.read_bytes()
    # The Multi-device section is appended AFTER the base's tail; the
    # first N bytes (where N=len(base)) must match.
    assert current[: len(base)] == base, \
        "host-mode section bytes drifted; append-only rule violated"


def test_runbook_multidevice_section_required_content(runbook_path):
    text = runbook_path.read_text(encoding="utf-8")
    assert _MARKER in text
    # After the marker, we expect these anchors:
    tail = text.split(_MARKER, 1)[1]
    required = [
        "scope banner",              # explicit banner text (case-insensitive check below)
        "paper/v3/p3-prod-multidevice-run",  # follow-up worktree named
        "multidevice/README.md",     # cross-reference
        "OAuth device flow",         # documentation-only section
        "Handoff pointer",           # handoff section
        "tunnel",                    # tunnel discussion present
    ]
    lowered = tail.lower()
    for anchor in required:
        assert anchor.lower() in lowered, f"missing anchor after marker: {anchor!r}"


def test_runbook_topology_diagram_has_all_devices_and_ports(runbook_path):
    """Spec §4.2 item 3: diagram must have 4 device labels + port
    annotations on tunnels."""
    text = runbook_path.read_text(encoding="utf-8")
    tail = text.split(_MARKER, 1)[1]
    # Extract the first fenced code block from the tail (ASCII diagram).
    lines = tail.splitlines()
    in_fence = False
    fence_lines: list[str] = []
    for line in lines:
        if line.startswith("```"):
            if in_fence:
                break
            in_fence = True
            continue
        if in_fence:
            fence_lines.append(line)
    diagram = "\n".join(fence_lines)
    assert diagram, "no fenced code block in Multi-device section"

    for device in ("laptop", "headless", "windows", "cloud"):
        assert device in diagram, f"diagram missing device label {device!r}"

    # Cross-machine tunnel edges MUST be port-annotated (spec §4.2
    # item 3). A tunnel edge is any diagram line that mentions
    # `tunnel`; each such line must carry at least one `:1809x` port
    # token so a reviewer can see which port terminates the tunnel.
    import re
    tunnel_lines = [line for line in diagram.splitlines() if "tunnel" in line.lower()]
    assert tunnel_lines, "diagram has no lines mentioning 'tunnel'"
    for line in tunnel_lines:
        assert re.search(r":1809[1-4]|:18080", line), (
            f"tunnel line lacks port annotation: {line!r}"
        )
