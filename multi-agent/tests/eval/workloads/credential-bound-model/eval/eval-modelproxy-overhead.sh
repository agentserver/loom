#!/usr/bin/env bash
# eval-modelproxy-overhead.sh — WT-2-credential-workload spec §2.3, §7(e).
#
# 同 prompt 双跑 harness — Same-prompt double-run.
# Two runs per sample: mode=a (local proxy) and mode=b (upstream direct),
# using the SAME prompt fixture and the SAME provider stack (only the
# codex config differs). The codex CLI at this base ref does not expose
# a sampling seed, so "same seed" means "same everything we can pin";
# sampler noise is the measurement's known floor.
#
# Any drift (prompt edited, sleep shortened, provider stack changed)
# invalidates the ModelProxyOverhead measurement — that is why:
#   - the prompt fixture path is a fixed constant,
#   - the inter-run delay is a LITERAL `sleep 30` (see --self-test),
#   - the runner's recorded codex_config_path is asserted per iteration.
#
# EXIT codes:
#   0  success (either --dry-run or all samples collected + printed)
#   1  a real run failed (oracle / loopback check / csv drift)
#   2  pre-flight / usage error

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
WORKLOAD_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
WORKLOAD_ID="credential-bound-model"
PROMPT_FIXTURE="$WORKLOAD_DIR/fixtures/prompt.txt"

# Default config layout on operator machines.
# WORKLOAD_DIR = .../multi-agent/tests/eval/workloads/credential-bound-model
# ../../../..  = .../multi-agent   (four segments: credential-bound-model,
#                                    workloads, eval, tests)
# then tests/prod_test/driver-codex-local/{,.codex/}config.toml
# Overridable via env for tests / operators with a non-default layout.
: "${CONFIG_A_PATH:=$WORKLOAD_DIR/../../../../tests/prod_test/driver-codex-local/.codex/config.toml}"
: "${CONFIG_B_PATH:=$WORKLOAD_DIR/../../../../tests/prod_test/driver-codex-local/codex-config.toml}"
: "${RUNNER_BIN:=eval-runner}"
: "${STUB_LISTEN:=127.0.0.1:18080}"

# ---------------------------------------------------------------------------
# Sentinel-key rejection mirrored from Go codexconfig.go's sentinelOpenAIKeys
# so mode=b in shell fails the same way the runner does.
is_sentinel_openai_key() {
  local v="$1"
  case "$v" in
    ""|"sk-fake"|"test-key"|"changeme"|"x") return 0 ;;
  esac
  # sk-XXXXXX… placeholder: 6+ consecutive X after sk-
  if [[ "$v" =~ ^sk-X{6,}$ ]]; then
    return 0
  fi
  return 1
}

die() {
  printf 'eval-modelproxy-overhead: %s\n' "$*" >&2
  exit 2
}

fail_run() {
  printf 'eval-modelproxy-overhead: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
usage: eval-modelproxy-overhead.sh [flags]

  --mode {a|b|both}     which mode(s) to run; default "both"
  --samples N           samples per mode; default 3, minimum 3
  --dry-run             validate inputs, print planned runner command
                        lines, do NOT invoke the runner
  --self-test           run the in-file guard tests (sleep 30 literal +
                        sentinel-key rejector); do not invoke the runner
  --help                print this message

env overrides:
  CONFIG_A_PATH  path to mode=a codex config (default: prod_test)
  CONFIG_B_PATH  path to mode=b codex config (default: prod_test)
  RUNNER_BIN     eval-runner binary (default: eval-runner on PATH)
  STUB_LISTEN    --stub-listen value (default: 127.0.0.1:18080)

CI note: CI runs --dry-run only. Real ModelProxyOverhead measurements
require real credentials + real network and are a manual operator
step; CI asserting a delta ceiling would be measuring the internet,
not the paper's claim (spec §7(h)).
EOF
}

# ---------------------------------------------------------------------------
# --self-test — invariant guards. This is what CI runs alongside
# --dry-run to defend against a drive-by edit that weakens the harness
# without also being caught by go test.
self_test() {
  local failed=0

  # Guard 1: the LITERAL `sleep 30` statement must be present on its
  # own line. A refactor that hides it behind a variable or shortens it
  # to `sleep 10` invalidates every historical ModelProxyOverhead
  # measurement (spec §7(e) guard 3).
  if grep -qE '^[[:space:]]*sleep 30[[:space:]]*$' "$0"; then
    printf 'OK   sleep_30_literal_present\n'
  else
    printf 'FAIL sleep_30_literal_present\n' >&2
    failed=$((failed + 1))
  fi

  # Guard 2: sentinel-key rejector agrees with the Go list.
  local key
  for key in "" "sk-fake" "test-key" "changeme" "x" "sk-XXXXXX" "sk-XXXXXXXXXX"; do
    if is_sentinel_openai_key "$key"; then
      printf 'OK   sentinel_reject: %q\n' "$key"
    else
      printf 'FAIL sentinel_reject: %q\n' "$key" >&2
      failed=$((failed + 1))
    fi
  done
  # Non-sentinel values must NOT be flagged.
  for key in "sk-real1234567890abcdef" "sk-Xrealkey" "sk-XXXXX-realkey"; do
    if is_sentinel_openai_key "$key"; then
      printf 'FAIL sentinel_accept_real: %q\n' "$key" >&2
      failed=$((failed + 1))
    else
      printf 'OK   sentinel_accept_real: %q\n' "$key"
    fi
  done

  # Guard 3: header comment mentions "同 prompt 双跑" so a reviewer
  # skimming the harness in five seconds sees the invariant.
  if grep -q '同 prompt 双跑' "$0"; then
    printf 'OK   header_marker_present\n'
  else
    printf 'FAIL header_marker_present\n' >&2
    failed=$((failed + 1))
  fi

  printf 'eval-modelproxy-overhead --self-test: %d failures\n' "$failed"
  [[ "$failed" -eq 0 ]]
}

# ---------------------------------------------------------------------------
# Pre-flight validation. Runs before both --dry-run planning and real runs.
preflight() {
  local mode="$1"

  # Prompt fixture (spec §7(e) guard 1).
  if [[ ! -s "$PROMPT_FIXTURE" ]]; then
    die "prompt fixture missing or empty: $PROMPT_FIXTURE"
  fi

  # Runner + oracle executable.
  local oracle="$WORKLOAD_DIR/oracle.sh"
  if ! command -v "$RUNNER_BIN" >/dev/null 2>&1 && [[ ! -x "$RUNNER_BIN" ]]; then
    die "runner binary not executable: $RUNNER_BIN (set RUNNER_BIN)"
  fi
  if [[ ! -x "$oracle" ]]; then
    die "oracle.sh not executable: $oracle"
  fi
  # CSV parsing during run_one requires python3's csv module (stdlib);
  # awk -F, would miscount columns because oracle_details_json /
  # oracle_metrics_json carry embedded commas inside quoted values.
  if ! command -v python3 >/dev/null 2>&1; then
    die "python3 not on PATH — required for CSV column extraction (stdlib csv module)"
  fi

  # Resolved configs exist AND are regular files.
  if [[ "$mode" == "a" || "$mode" == "both" ]]; then
    [[ -f "$CONFIG_A_PATH" ]] || die "mode=a config missing: $CONFIG_A_PATH (override with CONFIG_A_PATH env)"
  fi
  if [[ "$mode" == "b" || "$mode" == "both" ]]; then
    [[ -f "$CONFIG_B_PATH" ]] || die "mode=b config missing: $CONFIG_B_PATH (override with CONFIG_B_PATH env)"
    # Sentinel env check for mode=b (spec §7(d)).
    if is_sentinel_openai_key "${OPENAI_API_KEY:-}"; then
      die 'mode=b requires a non-sentinel OPENAI_API_KEY in the environment'
    fi
  fi
}

# ---------------------------------------------------------------------------
# Command-line construction (used by both --dry-run and real runs).
build_runner_cmd() {
  local mode="$1"
  local i="$2"
  local out="/tmp/wt2-modelproxy-${mode}-${i}.csv"
  local cfg
  if [[ "$mode" == "a" ]]; then
    cfg="$CONFIG_A_PATH"
  else
    cfg="$CONFIG_B_PATH"
  fi
  printf '%s run --workload %s --codex-config-mode %s --codex-config-path %s --keep-tempdir --out %s --stub-listen %s --run-id wt2-modelproxy-%s-%d-%s' \
    "$RUNNER_BIN" "$WORKLOAD_ID" "$mode" "$cfg" "$out" "$STUB_LISTEN" \
    "$mode" "$i" "$(date +%s 2>/dev/null || echo epoch)"
}

# ---------------------------------------------------------------------------
# Dry-run: preflight + print the planned commands, exit 0.
do_dry_run() {
  local mode="$1"
  local samples="$2"
  preflight "$mode"

  printf 'eval-modelproxy-overhead: dry-run\n'
  printf '  workload=%s\n' "$WORKLOAD_ID"
  printf '  prompt_fixture=%s\n' "$PROMPT_FIXTURE"
  if [[ "$mode" == "a" || "$mode" == "both" ]]; then
    printf '  mode=a  resolved_config=%s\n' "$CONFIG_A_PATH"
  fi
  if [[ "$mode" == "b" || "$mode" == "both" ]]; then
    printf '  mode=b  resolved_config=%s\n' "$CONFIG_B_PATH"
  fi
  printf '\n  planned invocations:\n'
  local m i
  for m in $( [[ "$mode" == "both" ]] && printf 'a\nb\n' || printf '%s\n' "$mode" ); do
    for ((i = 1; i <= samples; i++)); do
      printf '    %s\n' "$(build_runner_cmd "$m" "$i")"
    done
  done
  local total
  if [[ "$mode" == "both" ]]; then
    total=$((samples * 2))
  else
    total=$samples
  fi
  printf '\n  sleep 30 between adjacent runs (total runs: %d)\n' "$total"
}

# ---------------------------------------------------------------------------
# Real invocation: run + oracle + loopback check + CSV drift assertion.
# Exits 1 on any failure; on success accumulates duration_ms into
# $DURATIONS_A / $DURATIONS_B and returns.
DURATIONS_A=()
DURATIONS_B=()

run_one() {
  local mode="$1"
  local i="$2"
  local out="/tmp/wt2-modelproxy-${mode}-${i}.csv"
  local err="/tmp/wt2-modelproxy-${mode}-${i}.err"
  # Avoid stale CSV colliding with runner's --out ErrCSVExists.
  rm -f -- "$out" "$err"

  # Build the runner argv as an ARRAY. eval'ing a joined string would
  # split on any shell metachar hidden in env-controlled paths
  # (CONFIG_A_PATH containing a `;`, a `$(...)`, or an embedded space).
  local cfg run_id_epoch
  if [[ "$mode" == "a" ]]; then cfg="$CONFIG_A_PATH"; else cfg="$CONFIG_B_PATH"; fi
  run_id_epoch=$(date +%s 2>/dev/null || printf 'epoch')
  local -a runner_argv=(
    "$RUNNER_BIN" run
    --workload "$WORKLOAD_ID"
    --codex-config-mode "$mode"
    --codex-config-path "$cfg"
    --keep-tempdir
    --out "$out"
    --stub-listen "$STUB_LISTEN"
    --run-id "wt2-modelproxy-${mode}-${i}-${run_id_epoch}"
  )
  printf 'eval-modelproxy-overhead: [%s#%d] %s\n' "$mode" "$i" "${runner_argv[*]}"

  if ! "${runner_argv[@]}" 2>"$err"; then
    printf '%s\n' "$(<"$err")" >&2
    fail_run "runner exited non-zero for mode=$mode iter=$i (stderr saved at $err, csv at $out)"
  fi

  # Extract the workspace path from the --keep-tempdir diagnostic
  # (fixtures.go:85: "eval-runner: --keep-tempdir set; workspace at <path>").
  local ws
  ws=$(sed -nE 's/^eval-runner: --keep-tempdir set; workspace at (.*)$/\1/p' "$err" | head -n1)
  if [[ -z "$ws" || ! -d "$ws" ]]; then
    fail_run "could not extract workspace path from runner stderr (expected --keep-tempdir diagnostic in $err)"
  fi

  # Loopback check against the extracted workspace.
  if ! "$SCRIPT_DIR/hops_loopback_check.sh" "$ws/route.json" "$mode"; then
    fail_run "hops loopback check failed for mode=$mode iter=$i workspace=$ws"
  fi

  # CSV drift assertion (spec §7(e) guard 2 / plan §3.4).
  #
  # DO NOT use `awk -F,` here — the CSV has quoted JSON columns
  # (oracle_details_json, oracle_metrics_json) that carry embedded
  # commas, and a naïve split shifts every column that follows. Use
  # Python's csv module (stdlib, universally available) which parses
  # CSV quoting per RFC 4180 exactly like Go's encoding/csv reader.
  # The column lookup is BY NAME rather than by index so a future
  # schema-append in writer.go (an approved, additive change per the
  # CSVColumns() docstring) does NOT silently break this assertion.
  local parsed
  parsed=$(python3 - "$out" <<'PY' 2>/dev/null || true
import csv, sys
p = sys.argv[1]
with open(p, newline="") as f:
    r = csv.DictReader(f)
    row = next(r, None)
    if row is None:
        print("", "")
    else:
        # tab-separated so the shell can split easily even if a value
        # contains spaces.
        print(row.get("duration_ms", ""), row.get("codex_config_path", ""), sep="\t")
PY
)
  local dur got_path
  dur=$(printf '%s' "$parsed" | cut -f1)
  got_path=$(printf '%s' "$parsed" | cut -f2)

  local want_path
  if [[ "$mode" == "a" ]]; then
    want_path="$CONFIG_A_PATH"
  else
    want_path="$CONFIG_B_PATH"
  fi
  if [[ "$got_path" != "$want_path" ]]; then
    fail_run "csv codex_config_path drift: mode=$mode got=$got_path want=$want_path"
  fi
  if ! [[ "$dur" =~ ^[0-9]+$ ]]; then
    fail_run "csv duration_ms not an integer: got=$dur mode=$mode iter=$i"
  fi

  if [[ "$mode" == "a" ]]; then
    DURATIONS_A+=("$dur")
  else
    DURATIONS_B+=("$dur")
  fi

  # Harness-owned tempdir lifecycle: we opted the runner out of cleanup,
  # so we clean up here regardless of pass/fail. On the failure path
  # `fail_run` returned before we reached this line, leaving both the
  # workspace and the stderr file for post-mortem inspection.
  rm -rf -- "$ws"
  rm -f -- "$err"
}

# mean_ms <arr...> → prints integer mean, or "-" if empty
mean_ms() {
  local sum=0 count=0
  local v
  for v in "$@"; do
    sum=$((sum + v))
    count=$((count + 1))
  done
  if [[ "$count" -eq 0 ]]; then
    printf -- '-'
    return
  fi
  printf '%d' $((sum / count))
}

# ---------------------------------------------------------------------------
# main

MODE="both"
SAMPLES=3
DO_DRY=0
DO_SELF=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      shift
      [[ $# -ge 1 ]] || die '--mode needs a value'
      MODE="$1"
      shift
      ;;
    --samples)
      shift
      [[ $# -ge 1 ]] || die '--samples needs a value'
      SAMPLES="$1"
      shift
      ;;
    --dry-run) DO_DRY=1; shift ;;
    --self-test) DO_SELF=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown flag: $1" ;;
  esac
done

case "$MODE" in a|b|both) ;; *) die "invalid --mode: $MODE" ;; esac
if ! [[ "$SAMPLES" =~ ^[0-9]+$ ]]; then
  die "--samples must be an integer, got: $SAMPLES"
fi
if [[ "$SAMPLES" -lt 3 ]]; then
  die "--samples must be ≥ 3 (got $SAMPLES); ModelProxyOverhead needs at least 3 samples per path"
fi

if [[ "$DO_SELF" -eq 1 ]]; then
  self_test
  exit $?
fi

if [[ "$DO_DRY" -eq 1 ]]; then
  do_dry_run "$MODE" "$SAMPLES"
  exit 0
fi

# Real invocation path.
preflight "$MODE"

MODES_TO_RUN=()
if [[ "$MODE" == "a" || "$MODE" == "both" ]]; then MODES_TO_RUN+=("a"); fi
if [[ "$MODE" == "b" || "$MODE" == "both" ]]; then MODES_TO_RUN+=("b"); fi

# Interleave: for each iteration i, run every mode in order, with 30 s
# gaps between adjacent runs.
FIRST=1
for ((i = 1; i <= SAMPLES; i++)); do
  for m in "${MODES_TO_RUN[@]}"; do
    if [[ "$FIRST" -eq 0 ]]; then
      sleep 30
    fi
    FIRST=0
    run_one "$m" "$i"
  done
done

LATENCY_A="$(mean_ms "${DURATIONS_A[@]}")"
LATENCY_B="$(mean_ms "${DURATIONS_B[@]}")"

if [[ "$LATENCY_A" == "-" || "$LATENCY_B" == "-" ]]; then
  # Single-mode run: no delta to compute.
  printf 'ModelProxyOverhead: latency_a=%s latency_b=%s delta=- (n_a=%d n_b=%d)\n' \
    "$LATENCY_A" "$LATENCY_B" "${#DURATIONS_A[@]}" "${#DURATIONS_B[@]}"
  exit 0
fi

DELTA=$((LATENCY_A - LATENCY_B))
printf 'ModelProxyOverhead: latency_a=%d latency_b=%d delta=%d (n_a=%d n_b=%d)\n' \
  "$LATENCY_A" "$LATENCY_B" "$DELTA" "${#DURATIONS_A[@]}" "${#DURATIONS_B[@]}"
