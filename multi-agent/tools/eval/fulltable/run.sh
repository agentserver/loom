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

usage() {
  cat <<'EOF' >&2
Usage:
  run.sh --dry-run
  run.sh --sample N               (N ≤ 3; > 3 requires ALLOW_FULL_RUN=1)
  run.sh --resume
  run.sh --parallel N
  run.sh --inject-fake-failure-on-row N   (smoke path only; injects
                                          sk-abc… into that row's stderr)

Env:
  ALLOW_FULL_RUN=1                allow --sample N > 3
  LOOM_FULLTABLE_DISPATCH_SHIM=1  preflight + print SHIM line; do NOT
                                  exec runner/baseline. Test seam.
EOF
}

mode=""
sample_n=0
parallel=1
inject_row=0
resume=0
dry_flag=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_flag=1; shift ;;
    --sample) sample_n="$2"; shift 2 ;;
    --sample=*) sample_n="${1#--sample=}"; shift ;;
    --resume) resume=1; shift ;;
    --parallel) parallel="$2"; shift 2 ;;
    --parallel=*) parallel="${1#--parallel=}"; shift ;;
    --inject-fake-failure-on-row) inject_row="$2"; shift 2 ;;
    --inject-fake-failure-on-row=*) inject_row="${1#--inject-fake-failure-on-row=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "run.sh: unknown flag $1" >&2; usage; exit 2 ;;
  esac
done
# Default mode is --dry-run if no --sample and no --resume.
if (( sample_n == 0 && resume == 0 )); then
  dry_flag=1
fi
if (( dry_flag )) && (( sample_n == 0 )); then
  mode="dry"
elif (( sample_n > 0 )); then
  mode="sample"
elif (( resume )); then
  mode="resume"
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
  python3 -m lib.plan \
    --matrix "$fulltable_dir/matrix.yaml" \
    --smoke-root "$smoke_root_rel" \
    --module-root-prefix "" \
    --timeout 3600s \
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
python3 -m lib.plan \
  --matrix "$fulltable_dir/matrix.yaml" \
  --smoke-root "$smoke_root_rel" \
  --module-root-prefix "" \
  --timeout 60s \
  sample --n "$sample_n" > "$plans_file"

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

# Fresh files on each smoke run.
: > "$runs_csv"
: > "$failures_jsonl"
: > "$metrics_csv"

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
