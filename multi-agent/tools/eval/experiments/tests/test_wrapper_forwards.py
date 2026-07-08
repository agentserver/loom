"""pytest wrappers for test_wrapper_forwards.sh + test_wrapper_real_preflight.sh."""
import subprocess
from pathlib import Path


def test_wrapper_forwards() -> None:
    script = Path(__file__).parent / "test_wrapper_forwards.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, f"{proc.stdout}\n{proc.stderr}"


def test_wrapper_real_preflight() -> None:
    script = Path(__file__).parent / "test_wrapper_real_preflight.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, f"{proc.stdout}\n{proc.stderr}"
