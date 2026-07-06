#!/usr/bin/env python3
"""Dual-target dry-run patcher for motivation numbers.

Same invocation patches BOTH `paper_outputs/introduction_v3.md` (anchor +
sentence-regex mode) AND `paper_outputs/motivation_v3.md` (pure
`{{placeholder}}` mode). Under this worktree (p3-mini-case) it is ONLY ever
invoked with `--dry-run` — the two paper files remain untouched. `--execute`
is gated by `ALLOW_INTRO_REWRITE=1` env AND double-locked against smoke
input (`--allow-scaffold-smoke-input`). See docs/specs/wt3-mini-case.spec.md
§4 for the full contract.

Gate order (matches spec §5 test `test_numbers_dir_from_main_experiment` (e)):
    1. gate1  --execute + --allow-scaffold-smoke-input  → ErrExecuteSmokeConflict
    2. gate2  --numbers <dir> source-of-truth           → ErrNumbersDirNotFromMainExperiment
    3. gate3  --target list validation                  → ErrTargetMissing / ErrDuplicateTarget / ErrTargetPathRejected
    4. gate4  range + IQR ordering                      → ErrNumberOutOfRange / ErrIqrOrderInvalid
    5. gate5  --execute env check                       → downgrade to dry-run
    6. compute per-target diff                          → ErrIntroPatternMismatch / ErrIntroOutsideAnchor
                                                         → ErrMotivationPlaceholderMissing / ErrMotivationOutsidePlaceholder
    7. write / print
"""

from __future__ import annotations

import argparse
import difflib
import json
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path

CANONICAL_KEYS = (
    "contexts_count",
    "wrong_context_failure_manual_baseline",
    "manual_steps_ssh",
    "reuse_time_savings",
)

TARGET_WHITELIST = frozenset({"introduction_v3.md", "motivation_v3.md"})
TARGET_PARENT_BASENAME = "paper_outputs"

MAIN_EXP_SIGNATURE = frozenset({"runs.csv", "metrics.csv", "e4_stages.csv"})
SMOKE_INPUT_SIGNATURE = frozenset(f"{k}.json" for k in CANONICAL_KEYS)

RANGES: dict[str, tuple[float, float]] = {
    "contexts_count": (1.0, float("inf")),
    "wrong_context_failure_manual_baseline": (0.0, 1.0),
    "manual_steps_ssh": (1.0, float("inf")),
    "reuse_time_savings": (-100.0, 100.0),
}

# Error tokens (also referenced from tests).
ERR_EXECUTE_SMOKE = "ErrExecuteSmokeConflict"
ERR_NUMBERS_DIR = "ErrNumbersDirNotFromMainExperiment"
ERR_TARGET_MISSING = "ErrTargetMissing"
ERR_DUP_TARGET = "ErrDuplicateTarget"
ERR_TARGET_REJECT = "ErrTargetPathRejected"
ERR_RANGE = "ErrNumberOutOfRange"
ERR_IQR_ORDER = "ErrIqrOrderInvalid"
ERR_INTRO_MISMATCH = "ErrIntroPatternMismatch"
ERR_INTRO_OUTSIDE = "ErrIntroOutsideAnchor"
ERR_MOTIV_MISSING = "ErrMotivationPlaceholderMissing"
ERR_MOTIV_OUTSIDE = "ErrMotivationOutsidePlaceholder"


# ---------------------------------------------------------------------------
# Helpers: formatting + validation
# ---------------------------------------------------------------------------

def _format_scalar(value: float | int, canonical_key: str) -> str:
    if canonical_key in ("contexts_count", "manual_steps_ssh"):
        return str(int(round(float(value))))
    if canonical_key == "wrong_context_failure_manual_baseline":
        return f"{float(value):.3f}"
    if canonical_key == "reuse_time_savings":
        return f"{float(value):.1f}"
    raise ValueError(f"unknown canonical_key: {canonical_key}")


def _format_iqr(low: float | int, high: float | int, canonical_key: str) -> str:
    return f"{_format_scalar(low, canonical_key)}–{_format_scalar(high, canonical_key)}"


# ---------------------------------------------------------------------------
# gate2 — --numbers source-of-truth
# ---------------------------------------------------------------------------

def _check_numbers_dir(numbers_dir: Path, allow_smoke: bool) -> None:
    if not numbers_dir.is_dir():
        print(f"error: {ERR_NUMBERS_DIR}: not a directory: {numbers_dir}",
              file=sys.stderr)
        sys.exit(2)
    # Include ALL entries (files + subdirs + symlinks). Subdirs in a
    # smoke-mode dir mean stray artifacts from an old run — they must not
    # slip past the "exactly 4 basenames" contract just because they are
    # not regular files. Missing-check still needs file-only presence
    # (a subdir named `contexts_count.json` cannot be read as JSON).
    file_present = {p.name for p in numbers_dir.iterdir() if p.is_file()}
    all_present = {p.name for p in numbers_dir.iterdir()}
    if allow_smoke:
        missing = SMOKE_INPUT_SIGNATURE - file_present
        if missing:
            print(f"error: {ERR_NUMBERS_DIR} (smoke): missing {sorted(missing)}",
                  file=sys.stderr)
            sys.exit(2)
        # Spec §4.3 smoke branch: exactly the 4 canonical JSON basenames,
        # nothing else — including no stray subdirectories.
        extra = all_present - SMOKE_INPUT_SIGNATURE
        if extra:
            print(f"error: {ERR_NUMBERS_DIR} (smoke): unexpected extra entries "
                  f"{sorted(extra)}; smoke-mode dir must contain exactly the "
                  f"4 canonical JSON basenames (no subdirs, no stray files)",
                  file=sys.stderr)
            sys.exit(2)
    else:
        missing = MAIN_EXP_SIGNATURE - file_present
        if missing:
            print(
                f"error: {ERR_NUMBERS_DIR}: main-experiment signature files "
                f"missing from {numbers_dir}: {sorted(missing)}. "
                "Pass --allow-scaffold-smoke-input only for scaffold self-check.",
                file=sys.stderr,
            )
            sys.exit(2)


# ---------------------------------------------------------------------------
# gate3 — --target list validation
# ---------------------------------------------------------------------------

def _validate_targets(targets: list[str], paper_worktree: Path) -> dict[str, Path]:
    if not targets:
        print(f"error: {ERR_TARGET_MISSING}: expected two --target flags",
              file=sys.stderr)
        sys.exit(2)
    # Reject BEFORE resolve: two --target strings that share the same
    # basename (regardless of resolved path) — protects against the case
    # `--target foo/introduction_v3.md --target bar/introduction_v3.md`
    # where different `paper_outputs` dirs would silently overwrite in
    # the basename map.
    if len(targets) != 2:
        print(
            f"error: {ERR_TARGET_MISSING}: exactly 2 --target flags required, "
            f"got {len(targets)}",
            file=sys.stderr,
        )
        sys.exit(2)
    resolved = [Path(t).resolve() for t in targets]
    if len(resolved) != len(set(resolved)):
        print(f"error: {ERR_DUP_TARGET}: same --target passed twice",
              file=sys.stderr)
        sys.exit(2)
    seen_basenames: set[str] = set()
    for p in resolved:
        if p.name in seen_basenames:
            print(
                f"error: {ERR_DUP_TARGET}: two --target flags share basename "
                f"{p.name!r} (dual-target rule: exactly one target per "
                "whitelisted basename)",
                file=sys.stderr,
            )
            sys.exit(2)
        seen_basenames.add(p.name)
    by_basename: dict[str, Path] = {}
    pw_resolved = paper_worktree.resolve()
    for p in resolved:
        if p.name not in TARGET_WHITELIST:
            print(
                f"error: {ERR_TARGET_REJECT}: basename {p.name!r} not in whitelist "
                f"{sorted(TARGET_WHITELIST)}",
                file=sys.stderr,
            )
            sys.exit(2)
        if p.parent.name != TARGET_PARENT_BASENAME:
            print(
                f"error: {ERR_TARGET_REJECT}: parent basename "
                f"{p.parent.name!r} != {TARGET_PARENT_BASENAME!r} (target={p})",
                file=sys.stderr,
            )
            sys.exit(2)
        # Path must be inside the paper_writing worktree — prevents escapes
        # via a symlink or unrelated fs location.
        try:
            p.relative_to(pw_resolved)
        except ValueError:
            print(
                f"error: {ERR_TARGET_REJECT}: target {p} not inside paper_writing "
                f"worktree {pw_resolved}",
                file=sys.stderr,
            )
            sys.exit(2)
        by_basename[p.name] = p
    missing = TARGET_WHITELIST - set(by_basename.keys())
    if missing:
        print(
            f"error: {ERR_TARGET_MISSING}: both {sorted(TARGET_WHITELIST)} "
            f"required; missing {sorted(missing)}",
            file=sys.stderr,
        )
        sys.exit(2)
    return by_basename


# ---------------------------------------------------------------------------
# gate4 — load numbers + range + IQR ordering
# ---------------------------------------------------------------------------

@dataclass
class NumberRecord:
    canonical_key: str
    median: float
    iqr_low: float
    iqr_high: float


def _load_numbers(numbers_dir: Path) -> dict[str, NumberRecord]:
    records: dict[str, NumberRecord] = {}
    for key in CANONICAL_KEYS:
        p = numbers_dir / f"{key}.json"
        if not p.exists():
            print(f"error: {ERR_NUMBERS_DIR}: missing aggregate file for {key}: {p}",
                  file=sys.stderr)
            sys.exit(2)
        try:
            obj = json.loads(p.read_text(encoding="utf-8"))
        except json.JSONDecodeError as exc:
            print(f"error: {p} is not valid JSON: {exc}", file=sys.stderr)
            sys.exit(2)
        if obj.get("canonical_key") != key:
            print(f"error: {p} canonical_key mismatch: {obj.get('canonical_key')!r}",
                  file=sys.stderr)
            sys.exit(2)
        for field in ("median", "iqr_low", "iqr_high"):
            if field not in obj or obj[field] is None:
                print(f"error: {p} missing/null {field}", file=sys.stderr)
                sys.exit(2)
        records[key] = NumberRecord(
            canonical_key=key,
            median=float(obj["median"]),
            iqr_low=float(obj["iqr_low"]),
            iqr_high=float(obj["iqr_high"]),
        )
    return records


def _range_check(records: dict[str, NumberRecord]) -> None:
    for key, rec in records.items():
        lo, hi = RANGES[key]
        for label, value in (("median", rec.median),
                             ("iqr_low", rec.iqr_low),
                             ("iqr_high", rec.iqr_high)):
            if not (lo <= value <= hi):
                print(
                    f"error: {ERR_RANGE}: {key}.{label} = {value} out of [{lo}, {hi}]",
                    file=sys.stderr,
                )
                sys.exit(2)
        if not (rec.iqr_low <= rec.median <= rec.iqr_high):
            print(
                f"error: {ERR_IQR_ORDER}: {key} violates iqr_low ≤ median ≤ iqr_high "
                f"({rec.iqr_low}, {rec.median}, {rec.iqr_high})",
                file=sys.stderr,
            )
            sys.exit(2)


# ---------------------------------------------------------------------------
# gate6a — introduction patch (anchor + regex mode)
# ---------------------------------------------------------------------------

_INTRO_ANCHOR_START = re.compile(r"^现代\s*AI\s*agents", re.MULTILINE)
_INTRO_ANCHOR_END = re.compile(r"^因此，personal\s+compute\s+space", re.MULTILINE)


@dataclass
class _IntroEdit:
    key: str
    sentence_pattern: re.Pattern
    fill_template: str  # contains a literal `<value>` token


_INTRO_EDITS = [
    _IntroEdit(
        key="contexts_count",
        sentence_pattern=re.compile(r"它们仍然是割裂的\s+raw\s+contexts"),
        fill_template=" (mini-case 覆盖 <value> 个 context)",
    ),
    _IntroEdit(
        key="wrong_context_failure_manual_baseline",
        sentence_pattern=re.compile(r"系统缺少一种机制来回答：哪个\s+context"),
        fill_template=" (SSH baseline 中 <value> 的比例出现 wrong-context failure)",
    ),
    _IntroEdit(
        key="manual_steps_ssh",
        sentence_pattern=re.compile(r"agent\s+仍可能选错机器"),
        fill_template=" — 在 manual_ssh baseline 里平均需 <value> 步 shell 命令才能完成同任务",
    ),
    _IntroEdit(
        key="reuse_time_savings",
        sentence_pattern=re.compile(r"不需要复用的能力可继续以\s+one-off\s+script\s+形态存在"),
        fill_template=" (若判定值得复用，第二次同族任务 wall-clock 可缩短 <value>%)",
    ),
]


def _patch_intro(
    original_text: str,
    records: dict[str, NumberRecord],
) -> str:
    lines = original_text.splitlines(keepends=True)
    start_idx = None
    end_idx = None
    for i, line in enumerate(lines):
        if start_idx is None and _INTRO_ANCHOR_START.match(line):
            start_idx = i
        elif start_idx is not None and _INTRO_ANCHOR_END.match(line):
            end_idx = i
            break
    if start_idx is None or end_idx is None:
        print(f"error: {ERR_INTRO_MISMATCH}: anchor start or end not found "
              f"(start={start_idx}, end={end_idx})",
              file=sys.stderr)
        sys.exit(2)

    segment = "".join(lines[start_idx:end_idx])
    missing_keys: list[str] = []
    for edit in _INTRO_EDITS:
        if not edit.sentence_pattern.search(segment):
            missing_keys.append(edit.key)
    if missing_keys:
        print(f"error: {ERR_INTRO_MISMATCH}: missing sentence patterns for "
              f"{missing_keys}",
              file=sys.stderr)
        sys.exit(2)

    # Perform insertions in-order on the segment text.
    new_segment = segment
    for edit in _INTRO_EDITS:
        value = _format_scalar(records[edit.key].median, edit.key)
        filled = edit.fill_template.replace("<value>", value)
        # `re.sub` at first match (count=1) — coverage check above already
        # guarantees exactly one match per pattern.
        new_segment = edit.sentence_pattern.sub(
            lambda m, ins=filled: m.group(0) + ins,
            new_segment,
            count=1,
        )

    # Post-condition: no changes outside the anchored [start, end) segment.
    # Compare on a *per-line* basis: rebuild the full-file line list from
    # `new_segment.splitlines(keepends=True)` so an insertion that spans a
    # newline is visible as an extra line inserted in the middle. Any drift
    # from `lines[:start_idx]` or `lines[end_idx:]` at the same slice
    # indices → guard tripped.
    new_segment_lines = new_segment.splitlines(keepends=True)
    new_lines = lines[:start_idx] + new_segment_lines + lines[end_idx:]
    new_text = "".join(new_lines)

    orig_before = lines[:start_idx]
    orig_after = lines[end_idx:]
    new_before = new_lines[:start_idx]
    new_after = new_lines[start_idx + len(new_segment_lines):]
    if (orig_before != new_before) or (orig_after != new_after):
        print(f"error: {ERR_INTRO_OUTSIDE}: intro anchor segment guard tripped "
              f"(anchor_lines={end_idx - start_idx}, "
              f"segment_lines_after_patch={len(new_segment_lines)})",
              file=sys.stderr)
        sys.exit(2)
    # And: an insertion that spans a newline would change the total line
    # count within the anchor segment. Reject that too (spec §4.5: intro
    # segment length must be preserved).
    if len(new_segment_lines) != (end_idx - start_idx):
        print(f"error: {ERR_INTRO_OUTSIDE}: intro anchor segment line count "
              f"changed: {end_idx - start_idx} → {len(new_segment_lines)}",
              file=sys.stderr)
        sys.exit(2)

    return new_text


# ---------------------------------------------------------------------------
# gate6b — motivation patch ({{placeholder}} mode)
# ---------------------------------------------------------------------------

_PLACEHOLDER_RE = re.compile(r"\{\{([a-zA-Z0-9_]+)\}\}")


def _patch_motivation(
    original_text: str,
    records: dict[str, NumberRecord],
) -> str:
    required = set()
    for key in CANONICAL_KEYS:
        required.add(f"{key}_median")
        required.add(f"{key}_iqr")
    tokens_present = _PLACEHOLDER_RE.findall(original_text)  # list, preserves multiplicity
    tokens_present_set = set(tokens_present)
    missing = required - tokens_present_set
    if missing:
        print(
            f"error: {ERR_MOTIV_MISSING}: missing placeholders {sorted(missing)}",
            file=sys.stderr,
        )
        sys.exit(2)
    # Reject any unknown `{{...}}` token — spec §4.2 requires *exactly*
    # 8 placeholders, no extras. An unknown extra token cannot be
    # substituted and would silently survive to the paper.
    extra_unknown = tokens_present_set - required
    if extra_unknown:
        print(
            f"error: {ERR_MOTIV_MISSING}: unknown placeholders {sorted(extra_unknown)} "
            "present in motivation_v3.md (spec §4.2 requires exactly 8)",
            file=sys.stderr,
        )
        sys.exit(2)
    # And reject any DUPLICATE of a known placeholder — the exact-8 rule
    # is a total count assertion, not just "each present at least once".
    # A copy-paste that produced e.g. two {{contexts_count_median}} tokens
    # would otherwise silently allow 9+ placeholders through.
    if len(tokens_present) != len(required):
        from collections import Counter
        counts = Counter(tokens_present)
        dups = {tok: n for tok, n in counts.items() if n > 1}
        print(
            f"error: {ERR_MOTIV_MISSING}: motivation_v3.md has "
            f"{len(tokens_present)} placeholders; spec §4.2 requires exactly "
            f"{len(required)}. Duplicates: {dups}",
            file=sys.stderr,
        )
        sys.exit(2)

    def _replace(match: re.Match) -> str:
        token = match.group(1)
        if token.endswith("_median"):
            key = token[: -len("_median")]
            if key not in CANONICAL_KEYS:
                return match.group(0)
            return _format_scalar(records[key].median, key)
        if token.endswith("_iqr"):
            key = token[: -len("_iqr")]
            if key not in CANONICAL_KEYS:
                return match.group(0)
            return _format_iqr(records[key].iqr_low, records[key].iqr_high, key)
        return match.group(0)

    new_text = _PLACEHOLDER_RE.sub(_replace, original_text)

    # Verify all changed lines contain (originally) a {{...}} token.
    orig_lines = original_text.splitlines()
    new_lines = new_text.splitlines()
    if len(orig_lines) != len(new_lines):
        print(f"error: {ERR_MOTIV_OUTSIDE}: line count changed", file=sys.stderr)
        sys.exit(2)
    for i, (o, n) in enumerate(zip(orig_lines, new_lines), start=1):
        if o == n:
            continue
        if not _PLACEHOLDER_RE.search(o):
            print(
                f"error: {ERR_MOTIV_OUTSIDE}: line {i} changed but had no "
                "{{...}} placeholder in original",
                file=sys.stderr,
            )
            sys.exit(2)
    return new_text


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------

def _diff(basename: str, original: str, new: str) -> str:
    diff_lines = difflib.unified_diff(
        original.splitlines(keepends=True),
        new.splitlines(keepends=True),
        fromfile=f"a/{basename}",
        tofile=f"b/{basename}",
        n=3,
    )
    return "".join(diff_lines)


def _cli() -> None:
    p = argparse.ArgumentParser(description="Dual-target dry-run patcher.")
    p.add_argument("--target", action="append", default=[],
                   help="Repeatable; expected twice (introduction_v3.md + motivation_v3.md).")
    p.add_argument("--numbers", required=True, type=Path)
    p.add_argument("--provenance-path", required=True, type=Path,
                   help="Path to the (generated or template) provenance MD; "
                        "recorded in diff header as attribution.")
    p.add_argument("--paper-worktree", type=Path, required=True,
                   help="Paper_writing worktree root; the resolve-under-worktree "
                        "guard in gate3 rule 3 is meaningless if this can be "
                        "inferred from --target itself, so it is explicit.")
    mode = p.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true")
    mode.add_argument("--execute", action="store_true")
    p.add_argument("--allow-scaffold-smoke-input", action="store_true",
                   help="Accept smoke-mode <numbers> layout instead of main-experiment.")
    args = p.parse_args()

    # ------------------------------------------------------------------ gate1
    if args.execute and args.allow_scaffold_smoke_input:
        print(
            f"error: {ERR_EXECUTE_SMOKE}: --execute + --allow-scaffold-smoke-input "
            "combination is forbidden regardless of ALLOW_INTRO_REWRITE.",
            file=sys.stderr,
        )
        sys.exit(2)

    # ------------------------------------------------------------------ gate2
    _check_numbers_dir(args.numbers, allow_smoke=args.allow_scaffold_smoke_input)

    # ------------------------------------------------------------------ gate3
    targets = _validate_targets(args.target, args.paper_worktree)

    # ------------------------------------------------------------------ gate4
    records = _load_numbers(args.numbers)
    _range_check(records)

    # ------------------------------------------------------------------ gate5
    execute = args.execute
    if execute and os.environ.get("ALLOW_INTRO_REWRITE") != "1":
        print("dry-run mode (ALLOW_INTRO_REWRITE not set)", file=sys.stderr)
        execute = False

    # ------------------------------------------------------------------ gate6/7 compute
    intro_path = targets["introduction_v3.md"]
    motiv_path = targets["motivation_v3.md"]
    intro_orig = intro_path.read_text(encoding="utf-8")
    motiv_orig = motiv_path.read_text(encoding="utf-8")

    # Compute both patches BEFORE any write — atomic rollback.
    try:
        intro_new = _patch_intro(intro_orig, records)
    except SystemExit:
        raise
    try:
        motiv_new = _patch_motivation(motiv_orig, records)
    except SystemExit:
        # Motivation failure aborts both — no partial write happens because
        # we haven't written yet.
        raise

    intro_diff = _diff("introduction_v3.md", intro_orig, intro_new)
    motiv_diff = _diff("motivation_v3.md", motiv_orig, motiv_new)

    print(f"# --- introduction_v3.md ---")
    print(intro_diff, end="" if intro_diff else "\n")
    print(f"# --- motivation_v3.md ---")
    print(motiv_diff, end="" if motiv_diff else "\n")

    if execute:
        intro_path.write_text(intro_new, encoding="utf-8")
        motiv_path.write_text(motiv_new, encoding="utf-8")
        print(f"wrote: {intro_path}", file=sys.stderr)
        print(f"wrote: {motiv_path}", file=sys.stderr)
    # provenance-path is recorded here for traceability but not used yet.
    _ = args.provenance_path


if __name__ == "__main__":
    _cli()
