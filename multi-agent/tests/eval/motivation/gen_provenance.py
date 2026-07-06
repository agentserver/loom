#!/usr/bin/env python3
"""Fill the motivation-data-provenance template.

Substitutes per-key + shared placeholders. Asserts clean tree on BOTH
paper_writing and multi-agent worktrees (`git status --porcelain
--untracked-files=normal` empty). After substitution, greps for any
surviving `{{...}}` token → exit 2 `ErrUnresolvedPlaceholder`.

`--dry-run` writes to `/tmp/motivation_data_provenance.md.smoke` instead of
the real target; still runs both correctness checks.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ERR_DIRTY = "ErrDirtyTree"
ERR_UNRESOLVED = "ErrUnresolvedPlaceholder"

CANONICAL_KEYS = (
    "contexts_count",
    "wrong_context_failure_manual_baseline",
    "manual_steps_ssh",
    "reuse_time_savings",
)


def _sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _git_sha(worktree: Path) -> str:
    out = subprocess.check_output(["git", "-C", str(worktree), "rev-parse", "HEAD"])
    return out.decode().strip()


def _assert_clean(worktree: Path) -> None:
    out = subprocess.check_output(
        ["git", "-C", str(worktree), "status", "--porcelain", "--untracked-files=normal"]
    )
    if out.strip():
        print(f"error: {ERR_DIRTY}: worktree {worktree} has uncommitted/untracked "
              f"changes:\n{out.decode()}",
              file=sys.stderr)
        sys.exit(2)


def _seed_from_spec_yaml(spec_yaml: Path) -> int:
    text = spec_yaml.read_text(encoding="utf-8")
    m = re.search(r"^\s*wrong_ctx_seed\s*:\s*(-?\d+)\s*$", text, re.MULTILINE)
    if not m:
        raise ValueError(f"wrong_ctx_seed: <int> not found in {spec_yaml}")
    return int(m.group(1))


def _format_number(value: float | int, canonical_key: str, kind: str) -> str:
    """Format a scalar according to canonical schema (spec §2).

    kind == "median" or "iqr_end" (single scalar).
    """
    if canonical_key in ("contexts_count", "manual_steps_ssh"):
        return str(int(round(float(value))))
    if canonical_key == "wrong_context_failure_manual_baseline":
        return f"{float(value):.3f}"
    if canonical_key == "reuse_time_savings":
        return f"{float(value):.1f}"
    raise ValueError(f"unknown canonical_key: {canonical_key}")


def _format_iqr(low: float | int, high: float | int, canonical_key: str) -> str:
    return f"{_format_number(low, canonical_key, 'iqr_end')}–{_format_number(high, canonical_key, 'iqr_end')}"


def substitute(
    template: str,
    aggregate_dir: Path,
    raw_dir: Path,
    spec_yaml: Path,
    paper_worktree: Path,
    multi_agent_worktree: Path,
) -> str:
    out = template

    seed = _seed_from_spec_yaml(spec_yaml)
    out = out.replace("{{wrong_ctx_seed}}", str(seed))
    out = out.replace("{{paper_git_sha}}", _git_sha(paper_worktree))
    out = out.replace("{{multi_agent_git_sha}}", _git_sha(multi_agent_worktree))

    for key in CANONICAL_KEYS:
        agg_path = aggregate_dir / f"{key}.json"
        if not agg_path.exists():
            print(f"error: aggregate file missing: {agg_path}", file=sys.stderr)
            sys.exit(2)
        agg = json.loads(agg_path.read_text(encoding="utf-8"))
        median_s = _format_number(agg["median"], key, "median")
        iqr_s = _format_iqr(agg["iqr_low"], agg["iqr_high"], key)
        n = int(agg["n_samples"])

        # Raw sources for _source_sha256_list.
        raw_shas = []
        for jf in sorted(raw_dir.glob("*.json")):
            try:
                raw_obj = json.loads(jf.read_text(encoding="utf-8"))
            except json.JSONDecodeError:
                continue
            if raw_obj.get("canonical_key") != key:
                continue
            raw_shas.append(f"{jf.name}={_sha256_file(jf)}")
        source_list = ", ".join(raw_shas) if raw_shas else "(no raw files)"

        cli_str = (
            f"python tests/eval/motivation/collect_"
            f"{'contexts_count' if key == 'contexts_count' else 'wrong_context_failure' if key == 'wrong_context_failure_manual_baseline' else 'manual_steps' if key == 'manual_steps_ssh' else 'reuse_time_savings'}"
            f".py ...  # see tests/eval/motivation/dry_run_smoke.sh"
        )

        subs = {
            f"{{{{{key}_median}}}}": median_s,
            f"{{{{{key}_iqr}}}}": iqr_s,
            f"{{{{{key}_n_samples}}}}": str(n),
            f"{{{{{key}_source_sha256_list}}}}": source_list,
            f"{{{{{key}_output_sha256}}}}": _sha256_file(agg_path),
            f"{{{{{key}_cli}}}}": cli_str,
        }
        for placeholder, value in subs.items():
            out = out.replace(placeholder, value)

    return out


def _final_placeholder_check(rendered: str) -> None:
    survivors = re.findall(r"\{\{[a-zA-Z0-9_]+\}\}", rendered)
    if survivors:
        print(
            f"error: {ERR_UNRESOLVED}: {sorted(set(survivors))} still present "
            "after substitution",
            file=sys.stderr,
        )
        sys.exit(2)


def _cli() -> None:
    p = argparse.ArgumentParser(description="Render motivation-data provenance MD.")
    p.add_argument("--template", required=True, type=Path)
    p.add_argument("--aggregate-dir", required=True, type=Path)
    p.add_argument("--raw-dir", required=True, type=Path)
    p.add_argument("--spec-yaml", required=True, type=Path,
                   help="Path to motivation-e2e/spec.yaml (for wrong_ctx_seed).")
    p.add_argument("--paper-worktree", required=True, type=Path)
    p.add_argument("--multi-agent-worktree", required=True, type=Path)
    p.add_argument("--out", required=True, type=Path)
    p.add_argument("--dry-run", action="store_true",
                   help="Write to /tmp/motivation_data_provenance.md.smoke and "
                        "still run correctness checks.")
    args = p.parse_args()

    _assert_clean(args.paper_worktree)
    _assert_clean(args.multi_agent_worktree)

    template = args.template.read_text(encoding="utf-8")
    rendered = substitute(
        template,
        aggregate_dir=args.aggregate_dir,
        raw_dir=args.raw_dir,
        spec_yaml=args.spec_yaml,
        paper_worktree=args.paper_worktree,
        multi_agent_worktree=args.multi_agent_worktree,
    )
    _final_placeholder_check(rendered)

    target = Path("/tmp/motivation_data_provenance.md.smoke") if args.dry_run else args.out
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(rendered, encoding="utf-8")
    print(f"wrote {target} ({len(rendered)} bytes)")


if __name__ == "__main__":
    _cli()
