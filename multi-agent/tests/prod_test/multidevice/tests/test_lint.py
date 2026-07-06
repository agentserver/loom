"""lint.sh clean-tree tests. Covers plan Step 10."""
from __future__ import annotations

import subprocess


def test_lint_runs_clean_on_current_tree(multidevice_dir):
    proc = subprocess.run(
        ["bash", str(multidevice_dir / "lint.sh")],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, (
        f"lint.sh failed on current tree with rc={proc.returncode}\n"
        f"stdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
    )
    assert "clean" in proc.stdout, f"expected 'clean' in stdout: {proc.stdout!r}"
