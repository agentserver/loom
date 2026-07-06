#!/usr/bin/env bash
# End-to-end scaffold self-check:
#   collectors → 3 raw JSON per canonical key → aggregate → gen_provenance
#   (--dry-run) → replace_intro (--dry-run --allow-scaffold-smoke-input).
#
# Neither introduction_v3.md nor motivation_v3.md is mutated. See
# docs/specs/wt3-mini-case.spec.md §5 test_target_files_zero_changes.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
motivation_dir="$here"
ma_root="$(cd "$here/../../.." && pwd)"           # multi-agent module root
workload_dir="$ma_root/tests/eval/workloads/motivation-e2e"
trace_dir="$workload_dir/fixtures/traces"
paper_worktree="${PAPER_WORKTREE:-/root/paper_writing/.worktrees/p3-mini-case}"
multi_agent_worktree="$ma_root"                   # aggregate/raw live inside this repo

# tmpdir for the ephemeral raw JSON produced by the collectors.
tmpdir="$(mktemp -d -t p3-mini-case-smoke-XXXXXX)"
trap 'rm -rf "$tmpdir"' EXIT
raw_dir="$tmpdir/raw"
mkdir -p "$raw_dir"

# --- collectors: emit 3 raw JSON per canonical key -------------------------
# contexts_count: sqlite -> 1 base; jitter with 2 clones so we have N=3.
python "$motivation_dir/collect_contexts_count.py" \
  --route-reasons "$trace_dir/route_reasons.sqlite" \
  --workload-id motivation-e2e \
  --out "$raw_dir/contexts_count.rep1.json"
# rep2/rep3: reuse same result to satisfy N>=3 (fixture is deterministic;
# the smoke chain proves the aggregate math, not statistical noise).
cp "$raw_dir/contexts_count.rep1.json" "$raw_dir/contexts_count.rep2.json"
cp "$raw_dir/contexts_count.rep1.json" "$raw_dir/contexts_count.rep3.json"

python "$motivation_dir/collect_wrong_context_failure.py" \
  --runs-csv "$trace_dir/runs.csv" \
  --workload-id motivation-e2e \
  --baseline manual_ssh \
  --spec-yaml "$workload_dir/spec.yaml" \
  --out "$raw_dir/wcf.rep1.json"
cp "$raw_dir/wcf.rep1.json" "$raw_dir/wcf.rep2.json"
cp "$raw_dir/wcf.rep1.json" "$raw_dir/wcf.rep3.json"

python "$motivation_dir/collect_manual_steps.py" \
  --runs-csv "$trace_dir/runs.csv" \
  --workload-id motivation-e2e \
  --baseline manual_ssh \
  --steps-log "$trace_dir/steps.log" \
  --out "$raw_dir/manual_steps.rep1.json"
cp "$raw_dir/manual_steps.rep1.json" "$raw_dir/manual_steps.rep2.json"
cp "$raw_dir/manual_steps.rep1.json" "$raw_dir/manual_steps.rep3.json"

# reuse_time_savings: emits one JSON per (workload, family); e4_stages.csv
# has 3 families → 3 JSON, satisfies N>=3 organically.
python "$motivation_dir/collect_reuse_time_savings.py" \
  --e4-stages-csv "$trace_dir/e4_stages.csv" \
  --workload-id motivation-e2e \
  --out-dir "$raw_dir/"

# --- aggregate ------------------------------------------------------------
agg_dir="$motivation_dir/results/dry_run_smoke"
mkdir -p "$agg_dir"
for key in contexts_count wrong_context_failure_manual_baseline manual_steps_ssh reuse_time_savings; do
  python "$motivation_dir/aggregate.py" \
    --canonical-key "$key" \
    --raw-dir "$raw_dir" \
    --out "$agg_dir/${key}.json"
done

# --- gen_provenance (--dry-run) -------------------------------------------
template="$paper_worktree/paper_outputs/motivation_data_provenance.md.template"
python "$motivation_dir/gen_provenance.py" \
  --template "$template" \
  --aggregate-dir "$agg_dir" \
  --raw-dir "$raw_dir" \
  --spec-yaml "$workload_dir/spec.yaml" \
  --paper-worktree "$paper_worktree" \
  --multi-agent-worktree "$multi_agent_worktree" \
  --out /dev/null \
  --dry-run

# --- replace_intro (--dry-run --allow-scaffold-smoke-input) ---------------
python "$motivation_dir/replace_intro.py" \
  --dry-run \
  --allow-scaffold-smoke-input \
  --numbers "$agg_dir" \
  --provenance-path /tmp/motivation_data_provenance.md.smoke \
  --target "$paper_worktree/paper_outputs/introduction_v3.md" \
  --target "$paper_worktree/paper_outputs/motivation_v3.md" \
  > /tmp/replace_intro.diff

# --- assertions ------------------------------------------------------------
for tgt in introduction_v3.md motivation_v3.md; do
  if [[ -n "$(git -C "$paper_worktree" diff -- "paper_outputs/$tgt")" ]]; then
    echo "FAIL: $tgt was mutated" >&2
    exit 1
  fi
done
test -s /tmp/replace_intro.diff
grep -q "introduction_v3.md" /tmp/replace_intro.diff
grep -q "motivation_v3.md" /tmp/replace_intro.diff

echo "dry_run_smoke OK"
echo "  aggregate dir: $agg_dir"
echo "  diff: /tmp/replace_intro.diff ($(wc -l < /tmp/replace_intro.diff) lines)"
