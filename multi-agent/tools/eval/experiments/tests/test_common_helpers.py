"""pytest wrapper for _common.sh test suite."""
import subprocess
from pathlib import Path


def test_common_helpers() -> None:
    script = Path(__file__).parent / "test_common_helpers.sh"
    proc = subprocess.run(["bash", str(script)], capture_output=True, text=True)
    assert proc.returncode == 0, (
        f"_common.sh tests failed:\n"
        f"stdout:\n{proc.stdout}\n"
        f"stderr:\n{proc.stderr}"
    )
