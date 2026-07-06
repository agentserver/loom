"""Schema tests for per-device templates + topology samples.

Covers spec §2 (topology enum, workload hard cap) + §7(a)
(oauth_placeholder literal) + §7(h) (cloud vendor enum).
"""
from __future__ import annotations

import copy
import json
import pathlib

import pytest

yaml = pytest.importorskip("yaml")
jsonschema = pytest.importorskip("jsonschema")


def _load_schema(multidevice_dir: pathlib.Path) -> dict:
    return json.loads((multidevice_dir / "topology.schema.json").read_text())


def _load_yaml(path: pathlib.Path) -> dict:
    return yaml.safe_load(path.read_text())


def test_schema_positive_all_templates(multidevice_dir):
    schema = _load_schema(multidevice_dir)
    for name in ("laptop", "headless", "windows", "cloud"):
        doc = _load_yaml(multidevice_dir / f"{name}.yaml.template")
        jsonschema.validate(doc, schema)


def test_schema_positive_all_topology_enums(multidevice_dir):
    schema = _load_schema(multidevice_dir)
    samples = list((multidevice_dir / "topologies").glob("*.yaml.sample"))
    seen: set[str] = set()
    for s in samples:
        doc = _load_yaml(s)
        jsonschema.validate(doc, schema)
        seen.add(doc["topology_enum"])
    assert seen == {"4-device", "3-device-nowin", "3-device-nocloud", "2-device-min"}


def test_workload_hard_cap(multidevice_dir):
    """Spec §3.3: workloads list length > 2 must be rejected."""
    schema = _load_schema(multidevice_dir)
    doc = _load_yaml(multidevice_dir / "topologies" / "4-device.yaml.sample")
    bad = copy.deepcopy(doc)
    bad["workloads"] = [
        "cross-device-code-mod",
        "windows-only-artifact",
        "cross-device-code-mod",
    ]
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(bad, schema)


def test_workload_name_whitelist(multidevice_dir):
    """Spec §3.3: workload names not in {cross-device-code-mod,
    windows-only-artifact} must be rejected."""
    schema = _load_schema(multidevice_dir)
    doc = _load_yaml(multidevice_dir / "topologies" / "4-device.yaml.sample")
    bad = copy.deepcopy(doc)
    bad["workloads"] = ["remote-data-processing"]
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(bad, schema)


def test_oauth_placeholder_literal_required(multidevice_dir):
    """Spec §7(a): oauth_placeholder MUST literally equal
    <OAUTH_TOKEN_HERE_DO_NOT_COMMIT>."""
    schema = _load_schema(multidevice_dir)
    doc = _load_yaml(multidevice_dir / "laptop.yaml.template")
    bad = copy.deepcopy(doc)
    bad["oauth_placeholder"] = "sk-realtokenwouldgohere"
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(bad, schema)


def test_cloud_vendor_yaml_single_line(multidevice_dir):
    """Spec §7(h): cloud.yaml.template `vendor:` is one line with an
    enum value."""
    text = (multidevice_dir / "cloud.yaml.template").read_text()
    vendor_lines = [
        line for line in text.splitlines()
        if line.startswith("vendor:")
    ]
    assert len(vendor_lines) == 1, f"expected 1 vendor: line, got {vendor_lines!r}"
    value = vendor_lines[0].split(":", 1)[1].strip()
    assert value in {"digitalocean", "e2b", "vps"}, f"vendor value {value!r} not in enum"


def test_per_workload_reps_locked_to_three(multidevice_dir):
    """Spec §3.2: N=3 rep convention locked so the real-run worktree
    cannot silently change it."""
    schema = _load_schema(multidevice_dir)
    doc = _load_yaml(multidevice_dir / "topologies" / "4-device.yaml.sample")
    bad = copy.deepcopy(doc)
    bad["per_workload_reps"] = 5
    with pytest.raises(jsonschema.ValidationError):
        jsonschema.validate(bad, schema)


def test_2_device_min_topology_notes(multidevice_dir):
    """Spec §2.2 fallback: 2-device-min sample must document both
    dropped coverage axes."""
    doc = _load_yaml(multidevice_dir / "topologies" / "2-device-min.yaml.sample")
    notes = doc.get("notes", "")
    assert "cross-OS" in notes, f"missing cross-OS impact note: {notes!r}"
    assert ("cross-internet" in notes or "cloud" in notes), \
        f"missing cross-internet / cloud impact note: {notes!r}"


def test_3_device_nowin_notes_present(multidevice_dir):
    doc = _load_yaml(multidevice_dir / "topologies" / "3-device-nowin.yaml.sample")
    notes = doc.get("notes", "")
    assert "Windows" in notes or "cross-OS" in notes


def test_3_device_nocloud_notes_present(multidevice_dir):
    doc = _load_yaml(multidevice_dir / "topologies" / "3-device-nocloud.yaml.sample")
    notes = doc.get("notes", "")
    assert "cloud" in notes or "cross-internet" in notes or "tailscale" in notes.lower() or "伪云" in notes
