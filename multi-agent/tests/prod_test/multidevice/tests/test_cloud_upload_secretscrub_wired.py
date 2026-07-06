"""Cloud upload secret-scrub wiring tests. Covers spec §7(d)."""
from __future__ import annotations

import subprocess
import sys


def test_cloud_upload_secretscrub_wired(multidevice_dir, tmp_path):
    """Injects a fake `sk-abc123...` into a scratch yaml; cloud_upload.py
    must refuse to upload (exit non-zero) and stderr must say "secret
    detected"."""
    scratch = tmp_path / "leaked_cloud.yaml"
    scratch.write_text("vendor: digitalocean\ndroplet_token: sk-abc12345notrealbutlookslike\n")
    proc = subprocess.run(
        [
            sys.executable,
            str(multidevice_dir / "wrappers" / "cloud_upload.py"),
            "--input", str(scratch),
            "--dry-run",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode != 0, f"expected non-zero exit; got {proc.returncode}; stdout={proc.stdout!r}"
    assert "secret detected" in proc.stderr.lower(), \
        f"expected 'secret detected' in stderr; got: {proc.stderr!r}"


def test_cloud_upload_secretscrub_clean_passes(multidevice_dir, tmp_path):
    """A clean yaml (no secrets) passes the scanner and exits 0."""
    scratch = tmp_path / "clean_cloud.yaml"
    scratch.write_text(
        "kind: device\nrole: slave-c\ndevice_kind: cloud\n"
        "listen: 127.0.0.1:18093\n"
        "oauth_placeholder: <OAUTH_TOKEN_HERE_DO_NOT_COMMIT>\n"
        "vendor: digitalocean\n"
    )
    proc = subprocess.run(
        [
            sys.executable,
            str(multidevice_dir / "wrappers" / "cloud_upload.py"),
            "--input", str(scratch),
            "--dry-run",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, f"expected 0 exit; got {proc.returncode}; stderr={proc.stderr!r}"
