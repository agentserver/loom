#!/usr/bin/env bash
# WT-3-stub-fulltable orchestrator. Modes: --dry-run / --sample N /
# --resume / --parallel N. Spec §4.
#
# Sample cap (spec §7 (k) + §4.3): --sample N > 3 requires
# ALLOW_FULL_RUN=1; otherwise print ErrFullRunNotAllowed and exit 2 —
# BEFORE the --dry-run short-circuit fires.
#
# Preflight (spec §7 (f)): before ANY dispatch (any invocation that is
# not --dry-run) invoke lib.commit_meta --preflight to assert clean
# tree; on dirty exit 2 with stderr ErrDirtyWorktree.
#
# Dispatch shim (plan §Step 10.2): LOOM_FULLTABLE_DISPATCH_SHIM=1
# lets CI-safe integration tests exercise the preflight without
# starting real runner/stub/observer subprocesses. When set, the shim
# runs the preflight normally, then prints
#   SHIM: would dispatch <N> rows
# to stdout and exits 0.
set -euo pipefail

# Resolve paths: this script lives at multi-agent/tools/eval/fulltable/run.sh.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
fulltable_dir="$here"
module_root="$(cd "$here/../../.." && pwd)"         # multi-agent/
worktree_root="$(cd "$module_root/.." && pwd)"      # repo root (has .git)
# Guard: our tree lives at <repo>/multi-agent/tools/eval/fulltable — the
# smoke_root_abs paths below must resolve under multi-agent/, not the
# repo root or an accidental sibling. `basename` check keeps mis-nested
# checkouts from writing to /tmp/… or /root/….
if [[ "$(basename "$module_root")" != "multi-agent" ]]; then
  echo "run.sh: module_root resolves to $module_root; expected .../multi-agent/" >&2
  exit 2
fi
smoke_root_abs="$module_root/tests/eval/results/smoke"
smoke_root_rel="tests/eval/results/smoke"           # from module_root
export PYTHONPATH="$fulltable_dir:${PYTHONPATH:-}"

# Fresh-review P2: under `set -u`, referencing `$HOME` when HOME is
# unset (e.g. CI running under systemd with `PrivateHome`) aborts the
# script BEFORE any results-root guard fires — turning a security
# refusal into a cryptic "HOME: unbound variable". Bind a safe default
# once, then use $home in the case patterns below.
home="${HOME:-/nonexistent-home}"

usage() {
  cat <<'EOF' >&2
Usage:
  run.sh --dry-run
  run.sh --sample N               (N ≤ 3; > 3 requires ALLOW_FULL_RUN=1)
  run.sh --resume [--sample N]    (defaults to --sample 3; skips rows with a
                                   completed sidecar; deletes stale per-row CSVs)
  run.sh --parallel N
  run.sh --workload <id>          (filter to one workload; at most once)
  run.sh --results-root <abs-dir> (override results dir; absolute, allowlisted)
  run.sh --inject-fake-failure-on-row N

Env:
  ALLOW_FULL_RUN=1                allow --sample N > 3
  LOOM_FULLTABLE_DISPATCH_SHIM=1  preflight + print SHIM line; do NOT
                                  exec runner/baseline. Test seam.
  LOOM_FULLTABLE_WRAPPER_SHIM=1   dual-guard with --dry-run: print
                                  SHIM_WORKLOAD_FILTER + SHIM_RESULTS_ROOT
                                  and exit 0 BEFORE preflight.
EOF
}

mode=""
sample_n=0
parallel=1
inject_row=0
resume=0
dry_flag=0
workload=""
workload_count=0
results_root=""
results_root_count=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_flag=1; shift ;;
    --sample) sample_n="$2"; shift 2 ;;
    --sample=*) sample_n="${1#--sample=}"; shift ;;
    --resume) resume=1; shift ;;
    --parallel) parallel="$2"; shift 2 ;;
    --parallel=*) parallel="${1#--parallel=}"; shift ;;
    --workload) workload="$2"; workload_count=$((workload_count + 1)); shift 2 ;;
    --workload=*) workload="${1#--workload=}"; workload_count=$((workload_count + 1)); shift ;;
    --results-root) results_root="$2"; results_root_count=$((results_root_count + 1)); shift 2 ;;
    --results-root=*) results_root="${1#--results-root=}"; results_root_count=$((results_root_count + 1)); shift ;;
    --inject-fake-failure-on-row) inject_row="$2"; shift 2 ;;
    --inject-fake-failure-on-row=*) inject_row="${1#--inject-fake-failure-on-row=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "run.sh: unknown flag $1" >&2; usage; exit 2 ;;
  esac
done
# --- Task 11 additions: --workload / --results-root / SHIM guards --------

# Duplicate --workload rejection (before dispatch, before preflight,
# before sample-cap check).
if [[ "$workload_count" -gt 1 ]]; then
  echo "run.sh: --workload may be given at most once (got $workload_count)" >&2
  exit 2
fi

# Sample cap fires BEFORE workload allowlist check, so a
# --sample 60 --workload junk request always fails on the cap.
if (( sample_n > 3 )) && [[ "${ALLOW_FULL_RUN:-}" != "1" ]]; then
  echo "ErrFullRunNotAllowed: --sample N=$sample_n exceeds smoke cap 3; set ALLOW_FULL_RUN=1" >&2
  exit 2
fi

# Workload allowlist derived from plan.py (avoids literal duplication).
# Plan-review Phase C P0: `grep -qx "$workload"` treats $workload as a
# regex and empty $workload is silently accepted. Fix: iterate the
# allowlist with EXACT string equality via `[[ ]]`, and reject an
# explicitly-given `--workload ''` at the count level.
if (( workload_count > 0 )); then
  if [[ -z "$workload" ]]; then
    echo "run.sh: --workload requires a non-empty id" >&2
    exit 2
  fi
  allow=$(python3 -m lib.plan \
      --matrix "$fulltable_dir/matrix.yaml" \
      --smoke-root "$smoke_root_rel" \
      --list-workloads 2>&1) || {
    echo "run.sh: plan.py --list-workloads failed:" >&2
    echo "$allow" >&2
    exit 2
  }
  match=0
  while IFS= read -r allowed; do
    if [[ "$workload" == "$allowed" ]]; then
      match=1
      break
    fi
  done <<< "$allow"
  if (( match == 0 )); then
    echo "run.sh: unknown workload id: $workload; expected one of:" >&2
    echo "$allow" >&2
    exit 2
  fi
fi

# --results-root override: validate + rewrite smoke_root_* if set.
# Belt-side of plan.py's _validate_results_root — this fires BEFORE
# any mkdir so a bad path can't create partial state.
#
# Fresh-review P1: reject an explicitly-passed empty value. If the
# caller wrote `--results-root ''`, that is a bug, not a silent
# fallback to the default smoke root.
if (( results_root_count > 0 )) && [[ -z "$results_root" ]]; then
  echo "run.sh: --results-root requires a non-empty absolute path" >&2
  exit 2
fi
if [[ -n "$results_root" ]]; then
  case "$results_root" in
    /*) : ;;
    *) echo "run.sh: --results-root must be absolute; got $results_root" >&2; exit 2 ;;
  esac
  case "$results_root" in
    /|/tmp|/root|"$home"|"$home/.codex") \
      echo "run.sh: --results-root $results_root is an unsafe root; refusing" >&2; exit 2 ;;
  esac
  case "$results_root" in
    /tmp/*) echo "run.sh: --results-root under /tmp is unsafe; refusing" >&2; exit 2 ;;
    "$home/.codex/"*) echo "run.sh: --results-root $results_root resolves under \$HOME/.codex/; refusing" >&2; exit 2 ;;
  esac
  # Canonicalize (realpath -m first, python3 fallback). Refuse if both fail.
  resolved=""
  if command -v realpath >/dev/null 2>&1; then
    resolved="$(realpath -m -- "$results_root" 2>/dev/null)" || resolved=""
  fi
  if [[ -z "$resolved" ]]; then
    resolved="$(python3 -c 'import sys, pathlib; print(pathlib.Path(sys.argv[1]).resolve(strict=False))' "$results_root" 2>/dev/null)" || resolved=""
  fi
  if [[ -z "$resolved" ]]; then
    echo "run.sh: --results-root $results_root cannot be canonicalized; refusing" >&2; exit 2
  fi
  case "$resolved" in
    /|/tmp|/tmp/*|/root|"$home"|"$home/.codex"|"$home/.codex/"*) \
      echo "run.sh: --results-root $results_root resolves to unsafe $resolved; refusing" >&2; exit 2 ;;
  esac
  # git-repo top-level rejection.
  # Fresh-review P1: `cd "$resolved"` fails when $resolved doesn't yet
  # exist (common for a fresh --results-root), so git_top would stay
  # empty and the check silently no-ops. Mirror plan.py: probe from
  # $resolved when it exists, else from its parent (walk up until a
  # dir exists).
  git_probe_dir="$resolved"
  while [[ -n "$git_probe_dir" && ! -d "$git_probe_dir" ]]; do
    parent="$(dirname "$git_probe_dir")"
    [[ "$parent" == "$git_probe_dir" ]] && break
    git_probe_dir="$parent"
  done
  if [[ -d "$git_probe_dir" ]]; then
    if git_top="$(cd "$git_probe_dir" && git rev-parse --show-toplevel 2>/dev/null)"; then
      if [[ -n "$git_top" && "$resolved" == "$git_top" ]]; then
        echo "run.sh: --results-root $results_root is a git repository top-level; refusing" >&2; exit 2
      fi
    fi
  fi
  # Non-empty target unless --resume
  if [[ -e "$resolved" && "$resume" != "1" ]]; then
    if [[ -n "$(ls -A "$resolved" 2>/dev/null || true)" ]]; then
      echo "run.sh: --results-root $resolved exists and is non-empty; pass --resume or point at a fresh path" >&2; exit 2
    fi
  fi
  smoke_root_abs="$resolved"
  smoke_root_rel="$resolved"
fi

# LOOM_FULLTABLE_WRAPPER_SHIM=1 + --dry-run dual-guard (Task 11 test seam).
# Fires BEFORE preflight so wrapper tests run under a dirty worktree.
if [[ "${LOOM_FULLTABLE_WRAPPER_SHIM:-0}" == "1" && "$dry_flag" == "1" ]]; then
  echo "SHIM_WORKLOAD_FILTER: ${workload:-}"
  echo "SHIM_RESULTS_ROOT: ${results_root:-<default>}"
  exit 0
fi

# Default mode is --dry-run if no --sample and no --resume.
if (( sample_n == 0 && resume == 0 )); then
  dry_flag=1
fi
# --resume without --sample defaults to N=3 (spec §4.5 read: resume the
# first-N subset, keeping the hard-cap fuse honest in this worktree).
if (( resume )) && (( sample_n == 0 )); then
  sample_n=3
fi
if (( dry_flag )) && (( sample_n == 0 )); then
  mode="dry"
elif (( sample_n > 0 )); then
  mode="sample"
fi

# --- Hard cap gate (must fire BEFORE --dry-run short-circuit) --------------
# spec §4.3: the gate fires whenever --sample N > 3 is present, even
# if --dry-run also is — so a stray `--sample 60 --dry-run` still exits 2.

if (( sample_n > 3 )) && [[ "${ALLOW_FULL_RUN:-}" != "1" ]]; then
  echo "ErrFullRunNotAllowed: --sample N=$sample_n exceeds smoke cap 3; set ALLOW_FULL_RUN=1" >&2
  exit 2
fi

# After the gate, --dry-run wins as a mode selector.
if (( dry_flag )); then mode="dry"; fi

# --- Dry-run: print planned CLIs and exit ---------------------------------

if [[ "$mode" == "dry" ]]; then
  dry_extra_args=()
  [[ -n "$workload" ]] && dry_extra_args+=(--filter-workload "$workload")
  [[ -n "$results_root" ]] && dry_extra_args+=(--results-root "$results_root")
  python3 -m lib.plan \
    --matrix "$fulltable_dir/matrix.yaml" \
    --smoke-root "$smoke_root_rel" \
    --module-root-prefix "" \
    --timeout 3600s \
    "${dry_extra_args[@]}" \
    dry-run
  exit 0
fi

# ALLOW_FULL_RUN=1 + --dry-run (via --sample N --dry-run) is not a real
# path here; --dry-run always short-circuits. But --sample N does NOT
# imply dry-run; --sample N always dispatches (or shims). The
# accept-path assertion in test_hard_cap_full_run.py exercises the
# combined form via ALLOW_FULL_RUN + --sample 4 --dry-run — since
# `--dry-run` becomes the mode we already dispatched above.

# --- Preflight (spec §7 (f)) -----------------------------------------------

# Use the git top-level (the worktree root) as the repo dir. In an
# integration test the WORKTREE_ROOT env override lets the harness point
# at a synthesised temp repo instead.
repo_dir="${WORKTREE_ROOT:-$worktree_root}"
if ! head=$(python3 -m lib.commit_meta --preflight "$repo_dir" 2>&1); then
  echo "$head" >&2
  exit 2
fi
head=$(printf '%s\n' "$head" | tail -1)

# --- Enumerate plan (JSON per line) ----------------------------------------

mkdir -p "$smoke_root_abs/dbs" "$smoke_root_abs/runs" "$smoke_root_abs/paper"

plans_file="$(mktemp)"
plan_top_args=(
  --matrix "$fulltable_dir/matrix.yaml"
  --smoke-root "$smoke_root_rel"
  --module-root-prefix ""
  --timeout 60s
)
[[ -n "$workload" ]] && plan_top_args+=(--filter-workload "$workload")
[[ -n "$results_root" ]] && plan_top_args+=(--results-root "$results_root")
plan_sub_args=(sample --n "$sample_n")
if (( resume )); then
  # spec §4.5: --resume enumerates matrix AND e4, both scopes' completed
  # sidecars short-circuit uniformly below. When --workload is set,
  # plan.py's _cmd_sample forces include_e4=False (E4 is per-family, not
  # per-workload) — so we still forward --include-e4 harmlessly.
  plan_sub_args+=(--include-e4 --e4 "$fulltable_dir/e4_stages.yaml")
fi
python3 -m lib.plan "${plan_top_args[@]}" "${plan_sub_args[@]}" > "$plans_file"

# Pre-filter plans against completed sidecars + stale-CSV cleanup.
# Done here (not inside the dispatch loop) so `SHIM: would dispatch N
# rows` reflects the ACTUAL remaining work.
if (( resume )); then
  filtered_file="$(mktemp)"
  while IFS= read -r plan_json; do
    rk=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["resume_key"])' "$plan_json")
    if compgen -G "$smoke_root_abs/runs/${rk}__*.done" > /dev/null; then
      echo "resume: skip $rk" >&2
      continue
    fi
    # stale .csv from a failed prior attempt → remove before retry
    rm -f "$smoke_root_abs/runs/${rk}__"*.csv 2>/dev/null || true
    printf '%s\n' "$plan_json" >> "$filtered_file"
  done < "$plans_file"
  mv "$filtered_file" "$plans_file"
fi

n_planned=$(wc -l < "$plans_file")

# --- Dispatch shim: bail after preflight -----------------------------------

if [[ "${LOOM_FULLTABLE_DISPATCH_SHIM:-}" == "1" ]]; then
  echo "SHIM: would dispatch $n_planned rows"
  rm -f "$plans_file"
  exit 0
fi

# --- Actual dispatch -------------------------------------------------------

runs_csv="$smoke_root_abs/runs.csv"
failures_jsonl="$smoke_root_abs/failures.jsonl"
metrics_csv="$smoke_root_abs/metrics.csv"

# --- Cleanup step ---------------------------------------------------------
# Non-resume smoke rebuilds every output from scratch (rerun-idempotent).
# --resume PRESERVES every completed sidecar + its per-row .csv + the
# accumulating runs.csv/metrics.csv/failures.jsonl/dbs/ so the operator
# can inspect what was done vs what is being retried (spec §4.5). Only
# stale per-row CSVs whose resume_key has NO matching .done marker are
# removed.
if (( resume )); then
  # Build the set of completed resume_keys via `.done` filenames, then
  # unlink any `.csv` under runs/ whose resume_key isn't in that set.
  # runs.csv / metrics.csv / failures.jsonl / dbs/ are left alone.
  find "$smoke_root_abs/runs" -maxdepth 1 -type f -name '*.csv' 2>/dev/null | \
  while IFS= read -r csv_file; do
    base=$(basename "$csv_file")
    # basename shape: <resume_key>__<uuid>.csv — rsplit on '__' once,
    # take everything before.
    rk_uuid="${base%.csv}"
    rk="${rk_uuid%__*}"
    if ! compgen -G "$smoke_root_abs/runs/${rk}__"*.done > /dev/null; then
      rm -f "$csv_file"
    fi
  done
else
  : > "$runs_csv"
  : > "$failures_jsonl"
  : > "$metrics_csv"
  rm -f "$smoke_root_abs/dbs/"*.db "$smoke_root_abs/runs/"*.done \
        "$smoke_root_abs/runs/"*.csv 2>/dev/null || true
fi

row_index=0
while IFS= read -r plan_json; do
  row_index=$((row_index + 1))
  # Resume-skip if sidecar present.
  resume_key=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["resume_key"])' "$plan_json")
  if (( resume )); then
    if compgen -G "$smoke_root_abs/runs/${resume_key}__*.done" > /dev/null; then
      echo "resume: skip $resume_key" >&2
      continue
    fi
    # stale .csv from a failed attempt → remove before retry
    rm -f "$smoke_root_abs/runs/${resume_key}__"*.csv 2>/dev/null || true
  fi

  # Build the per-row exec argv.
  argv=$(python3 -c 'import json,sys;print(" ".join(repr(a) for a in json.loads(sys.argv[1])["argv"]))' "$plan_json")
  out_csv=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["out_csv"])' "$plan_json")
  observer_db=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["observer_db"])' "$plan_json")
  run_id=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["run_id"])' "$plan_json")
  workload=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["workload_id"])' "$plan_json")
  conf=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["configuration"])' "$plan_json")

  # HARNESS-ONLY SMOKE STUB: we cannot actually start the runner + stub
  # + observer in this worktree (per WT-3-stub-fulltable §0 harness-
  # only scope). Instead, synthesise the CSV / DB row shape the
  # aggregation later reads. Each row lands in smoke/runs.csv (and a
  # per-row .csv under smoke/runs/) plus a distinct empty .db file
  # (spec §7 (d) demands 3 distinct DB files, not real observer data).
  touch "$observer_db"

  header="run_id,workload_id,baseline_or_ablation,loom_commit,passed,exit_code"
  if [[ ! -s "$runs_csv" ]]; then
    echo "$header" >> "$runs_csv"
  fi
  passed=true
  exit_code=0
  if (( inject_row == row_index )); then
    # Failure path: write a scrubbed failures.jsonl entry via the
    # Python failure_scrub helper (spec §7 (e)).
    passed=false
    exit_code=1
    FULLTABLE_DIR="$fulltable_dir" \
    LOOM_RUN_ID="$run_id" LOOM_CONF="$conf" LOOM_WORKLOAD="$workload" \
    LOOM_EXIT="$exit_code" \
    python3 -c '
import os, sys
sys.path.insert(0, os.environ["FULLTABLE_DIR"])
from lib.failure_scrub import failure_record, dumps
stderr_text = "ERROR: token=sk-abcdefghij0123456789xxxx\nboom\n"
print(dumps(failure_record(os.environ["LOOM_RUN_ID"], os.environ["LOOM_CONF"],
                           os.environ["LOOM_WORKLOAD"], int(os.environ["LOOM_EXIT"]),
                           stderr_text)))
' >> "$failures_jsonl"
  fi
  echo "$run_id,$workload,$conf,$head,$passed,$exit_code" >> "$runs_csv"

  # metrics.csv: one row per run with a fixture-filled metric set that
  # matches the paper-table sample writer's needs (spec §5).
  if [[ ! -s "$metrics_csv" ]]; then
    echo "run_id,TaskSuccessRate,TimeToCompletion,HumanContextSelectionCount,WrongContextFailureRate,LifecycleClosureRate,RoutingAccuracy" >> "$metrics_csv"
  fi
  # Deterministic placeholder values (spec §4.2: smoke exercises shape,
  # not magnitudes — real numbers come from the follow-up run worktree).
  if $passed; then
    echo "$run_id,1.0,45.0,1,0.0,1.0,0.9" >> "$metrics_csv"
  else
    echo "$run_id,0.0,60.0,3,0.4,0.0,0.7" >> "$metrics_csv"
  fi

  # sidecar for --resume
  touch "$smoke_root_abs/runs/${resume_key}__${run_id}.done"
  # per-row csv (some ops workflows want per-row inspection)
  echo "$header" > "$smoke_root_abs/runs/${resume_key}__${run_id}.csv"
  echo "$run_id,$workload,$conf,$head,$passed,$exit_code" >> "$smoke_root_abs/runs/${resume_key}__${run_id}.csv"
done < "$plans_file"

rm -f "$plans_file"

# --- Post-run loom_commit cross-check (spec §7 (f) step 3) -----------------
python3 -m lib.commit_meta --verify-runs "$runs_csv" --expected "$head"

echo "smoke dispatch complete: $n_planned rows → $smoke_root_rel/" >&2
