"""cloud_upload.py — pre-upload secret scanner for cloud_up.sh.

Spec §7(d): every cloud sandbox upload must be scanned via secretscrub
before it goes over the wire. This helper reads the input file, runs
`secretscrub_python.sanitize`, and exits non-zero if ANY redaction
occurs (fail-closed) — the upload is refused rather than silently
silently sending a scrubbed copy, because a token in the cloud config
means an operator got confused about where secrets live and should be
told loudly.

HARNESS-ONLY: --dry-run is the only supported mode in this worktree.
Real cloud uploads live in paper/v3/p3-prod-multidevice-run.
"""
from __future__ import annotations

import argparse
import pathlib
import sys

# Local import — `secretscrub_python.py` lives one directory up.
_HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(_HERE.parent))
from secretscrub_python import sanitize  # noqa: E402


def main() -> int:
    parser = argparse.ArgumentParser(prog="cloud_upload.py")
    parser.add_argument("--input", required=True, type=pathlib.Path,
                        help="path to the cloud config yaml to scan")
    parser.add_argument("--dry-run", action="store_true", default=False,
                        help="do not actually upload; scan only (default in this worktree)")
    args = parser.parse_args()

    if not args.input.is_file():
        print(f"cloud_upload.py: input file not found: {args.input}", file=sys.stderr)
        return 2

    text = args.input.read_text(encoding="utf-8", errors="replace")
    _sanitized, hits = sanitize(text)
    if hits > 0:
        print(
            f"cloud_upload.py: secret detected in {args.input}: {hits} redaction(s); "
            "refusing to upload. Move secrets out of this file (e.g. into "
            "OAuth token store outside the repo) before rerunning.",
            file=sys.stderr,
        )
        return 3

    if not args.dry_run:
        # Real upload lives in paper/v3/p3-prod-multidevice-run. Fail
        # loudly if we ever get here in this worktree — no ambient exit
        # path should hit a real doctl/e2b/ssh call here.
        print(
            "cloud_upload.py: --dry-run was expected; this worktree is "
            "harness-only. See paper/v3/p3-prod-multidevice-run for the "
            "real-upload implementation.",
            file=sys.stderr,
        )
        return 4

    print(f"cloud_upload.py: {args.input} scan clean (0 redactions)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
