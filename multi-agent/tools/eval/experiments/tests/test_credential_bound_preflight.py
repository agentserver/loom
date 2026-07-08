"""pytest wrapper for the 10-fixture credential-bound preflight suite."""
import subprocess
from pathlib import Path


def test_credential_bound_preflight() -> None:
    script = Path(__file__).parent / "test_credential_bound_preflight.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    if "SKIP:" in proc.stdout and proc.returncode == 0:
        import pytest
        pytest.skip(proc.stdout.strip())
    assert proc.returncode == 0, (
        f"credential-bound preflight failed:\n"
        f"stdout:\n{proc.stdout}\n"
        f"stderr:\n{proc.stderr}"
    )
