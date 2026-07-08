"""pytest wrapper for test_windows_guard.sh."""
import subprocess
from pathlib import Path


def test_windows_guard() -> None:
    script = Path(__file__).parent / "test_windows_guard.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, f"{proc.stdout}\n{proc.stderr}"
