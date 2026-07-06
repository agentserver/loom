"""teardown.sh tests. Covers spec §7 + §7(j)."""
from __future__ import annotations

import re
import subprocess


def test_teardown_dry_run_prints_four(multidevice_dir):
    proc = subprocess.run(
        ["bash", str(multidevice_dir / "teardown.sh"), "--dry-run"],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, f"unexpected exit {proc.returncode}; stderr={proc.stderr!r}"
    matches = re.findall(r"curl|doctl|pwsh|rm -f.*token", proc.stdout)
    assert len(matches) >= 4, (
        f"expected ≥ 4 planned command matches (curl/doctl/pwsh/rm-token), "
        f"got {len(matches)}: {matches!r}\nstdout:\n{proc.stdout}"
    )


def test_teardown_double_gate(multidevice_dir):
    """--execute without ALLOW_TEARDOWN env falls back to dry-run and
    warns on stderr."""
    proc = subprocess.run(
        ["bash", str(multidevice_dir / "teardown.sh"), "--execute"],
        capture_output=True,
        text=True,
        check=False,
        # Explicitly do NOT set ALLOW_TEARDOWN — that's the case we test.
        env={"PATH": "/usr/bin:/bin"},
    )
    assert proc.returncode == 0, f"unexpected exit {proc.returncode}"
    assert "ALLOW_TEARDOWN not set" in proc.stderr, \
        f"expected fall-back warning in stderr: {proc.stderr!r}"
    # dry-run's planned commands still print.
    matches = re.findall(r"curl|doctl|pwsh|rm -f.*token", proc.stdout)
    assert len(matches) >= 4, \
        f"expected ≥ 4 planned command matches after fall-back: {matches!r}"
