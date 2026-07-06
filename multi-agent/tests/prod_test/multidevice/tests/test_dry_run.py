"""dry_run_all.sh integration tests. Covers spec §7(g) + smoke."""
from __future__ import annotations

import hashlib
import json
import os
import subprocess


def _run_smoke(multi_agent_root, hostname_input: str) -> None:
    env = os.environ.copy()
    env["HOSTNAME"] = hostname_input
    # Never set ALLOW_PROD_DEPLOY or ALLOW_TEARDOWN.
    env.pop("ALLOW_PROD_DEPLOY", None)
    env.pop("ALLOW_TEARDOWN", None)
    proc = subprocess.run(
        ["bash", "tests/prod_test/multidevice/dry_run_all.sh"],
        cwd=str(multi_agent_root),
        capture_output=True,
        text=True,
        check=False,
        env=env,
    )
    # Best-effort: on a shared host the fake HTTP servers may fail to
    # bind (ports in use). The outputs we care about are still written.
    assert proc.returncode == 0, (
        f"dry_run_all.sh exit {proc.returncode}\nstderr:\n{proc.stderr}"
    )


def test_dry_run_smoke_produces_outputs(multi_agent_root):
    _run_smoke(multi_agent_root, "alice-laptop")

    out_dir = multi_agent_root / "tests" / "eval" / "results" / "prod" / "dry_run_smoke"
    assert (out_dir / "topology_used.json").is_file()
    assert (out_dir / "prod_vs_stub_sample.csv").is_file()
    # analysis_template.md is checked in test_analysis_template.py.


def test_topology_used_hostname_redacted(multi_agent_root):
    _run_smoke(multi_agent_root, "alice-laptop")

    out = multi_agent_root / "tests" / "eval" / "results" / "prod" / \
        "dry_run_smoke" / "topology_used.json"
    doc = json.loads(out.read_text())
    expected = hashlib.sha256(b"alice-laptop").hexdigest()[:8]
    for host in doc.get("hosts", []):
        assert host.get("hostname_sha8") == expected, \
            f"host hash mismatch: got {host.get('hostname_sha8')}, expected {expected}"


def test_integration_no_secrets_in_smoke_outputs(multi_agent_root):
    """No sk-/Bearer <hex>/refresh_token in any smoke output file."""
    import re

    _run_smoke(multi_agent_root, "alice-laptop")

    out_dir = multi_agent_root / "tests" / "eval" / "results" / "prod" / "dry_run_smoke"
    scanner = re.compile(r"sk-[A-Za-z0-9]{8,}|ghp_|AKIA|xoxb|Bearer\s+[A-Za-z0-9]{20,}|refresh_token")
    for f in out_dir.iterdir():
        if not f.is_file():
            continue
        text = f.read_text(encoding="utf-8", errors="replace")
        # Placeholder literal is not caught by scanner (no `sk-` etc).
        assert not scanner.search(text), f"secret pattern found in {f.name}: {text!r}"
