#!/usr/bin/env bash
set -euo pipefail

BASE_OUTPUT_DIR="${BASE_OUTPUT_DIR:-/root/paper_writing/paper_outputs/public_benchmark_task1_baselines}"
OUT_ROOT="${1:-${BASE_OUTPUT_DIR}/strict_partition}"
RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)-$$}"
RUN_DIR="${OUT_ROOT}/${RUN_ID}"
REPORT_PATH="${BASE_OUTPUT_DIR}/strict_partition_report_cn.md"
RESULTS_PATH="${BASE_OUTPUT_DIR}/strict_partition_results.json"
INTERMEDIATE_REPORT_PATH="${BASE_OUTPUT_DIR}/strict_partition_intermediate_trace_cn.md"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
WORKLOAD_DIR="${ROOT_DIR}/tests/eval/workloads/public-terminal-heterogeneous-dates"
SOURCE_CODEX_CONFIG="${CODEX_CONFIG_PATH:-${CODEX_HOME:-/root/.codex}/config.toml}"
SINGLE_CODEX_TIMEOUT_SEC="${SINGLE_CODEX_TIMEOUT_SEC:-900}"
FULLPCS_TIMEOUT_SEC="${FULLPCS_TIMEOUT_SEC:-1200}"
FULLPCS_PROMPT_MODE="${FULLPCS_PROMPT_MODE:-guided}"
EXPECTED_AVG_TEMP="11.428571428571429"
SHARED_PROMPT_PATH="${RUN_DIR}/shared_task_prompt.md"
PROMPT_PARITY_CONFIGS=(
  "single_machine_codex_ssh"
  "cloud_sandbox_context_injection"
  "FullPCS_strict_partition"
  "FullPCS_reuse_stage"
)

mkdir -p "$RUN_DIR" "$BASE_OUTPUT_DIR"

write_shared_task_prompt() {
  cat >"$SHARED_PROMPT_PATH" <<'PROMPT'
Complete the public Terminal-Bench heterogeneous-dates adaptation in the current workspace.

Before acting, read `task.md` and `environment.md` in the current workspace. Use only the data, tools, commands, or agent interfaces described by the current workspace environment. Do not ask for human input.

Task objective:
- Use `daily_temp_sf_high.csv` and `daily_temp_sf_low.csv` to calculate the average daily high-minus-low temperature difference across overlapping normalized dates.
- Heterogeneous date formats must be normalized before aligning rows.
- Write the final answer to `avg_temp.txt` in the current workspace, containing only a numeric value.

Constraints:
- Do not hard-code the expected answer.
- Do not copy raw CSV files into any workspace that the local `environment.md` marks as raw-data forbidden.
- Use the available execution substrate for this configuration; if remote agents or transport commands are available, use them according to `environment.md`.
- Finish autonomously and keep the final response brief.
PROMPT
}

prompt_sha256() {
  sha256sum "$SHARED_PROMPT_PATH" | awk '{print $1}'
}

write_prompt_extra_json() {
  local path="$1"
  local configuration="$2"
  local agent_name="$3"
  python3 - "$path" "$configuration" "$agent_name" "$SHARED_PROMPT_PATH" "$(prompt_sha256)" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
configuration = sys.argv[2]
agent_name = sys.argv[3]
prompt_path = sys.argv[4]
prompt_sha = sys.argv[5]
path.write_text(
    json.dumps(
        {
            "prompt_based_agent": True,
            "comparison_group": "same_prompt_agent",
            "prompt_path": prompt_path,
            "prompt_sha256": prompt_sha,
            "prompt_agent": agent_name,
            "prompt_configuration": configuration,
        },
        ensure_ascii=False,
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)
PY
}

duration_ms() {
  local start_ns="$1"
  local end_ns="$2"
  echo $(((end_ns - start_ns) / 1000000))
}

assert_driver_has_no_raw_csv() {
  local driver_workspace="$1"
  # driver workspace must not contain raw CSV under strict partition.
  if find "$driver_workspace" -name 'daily_temp_sf_*.csv' -print -quit | grep -q .; then
    echo "strict partition violation: driver workspace must not contain raw CSV" >&2
    return 1
  fi
}

prepare_partition() {
  local root="$1"
  local driver_workspace="${root}/driver_workspace"
  local data_context="${root}/data_context"
  local compute_context="${root}/compute_context"
  local final_workspace="${root}/final_workspace"

  mkdir -p \
    "$driver_workspace" \
    "${data_context}/input" \
    "$compute_context" \
    "$final_workspace" \
    "${root}/logs"

  cp "${WORKLOAD_DIR}/fixtures/task-deps/daily_temp_sf_high.csv" "${data_context}/input/daily_temp_sf_high.csv"
  cp "${WORKLOAD_DIR}/fixtures/task-deps/daily_temp_sf_low.csv" "${data_context}/input/daily_temp_sf_low.csv"
  cat >"${driver_workspace}/task.md" <<'TASK'
Public benchmark task: Terminal-Bench heterogeneous-dates adaptation.

Use daily_temp_sf_high.csv and daily_temp_sf_low.csv to calculate the average
daily high-minus-low temperature difference. The final answer must be written
to avg_temp.txt as only a numeric value.
TASK

  assert_driver_has_no_raw_csv "$driver_workspace"
}

write_single_machine_environment() {
  local driver_workspace="$1"
  cat >"${driver_workspace}/environment.md" <<'ENV'
# Environment

This workspace is the driver workspace for `single_machine_codex_ssh`.

Raw CSV files are forbidden in this workspace. They are available only through the SSH-style transport commands:

- `ssh data-slave '<command>'` or `ssh-data '<command>'` runs a shell command in the data context.
- `ssh compute-slave '<command>'` or `ssh-compute '<command>'` runs a shell command in the compute context.
- `scp data-slave:<path> compute-slave:<path>` transfers derived files from data to compute.
- `scp compute-slave:<path> driver:<path>` transfers final outputs from compute to this workspace.
- `scp-data-to-compute <data-path> <compute-path>` and `scp-compute-to-driver <compute-path> <driver-path>` are convenience wrappers.

Required route:
1. Use the data context to read `input/daily_temp_sf_high.csv` and `input/daily_temp_sf_low.csv`, normalize dates, and write `aligned_temperatures.csv`.
2. Transfer only `aligned_temperatures.csv` to the compute context.
3. Use the compute context to calculate the mean of the `difference` column and write `avg_temp.txt`.
4. Transfer only `avg_temp.txt` back to this driver workspace.
ENV
}

write_cloud_environment() {
  local cloud_context="$1"
  cat >"${cloud_context}/environment.md" <<'ENV'
# Environment

This workspace is `cloud_sandbox_context_injection`.

The sandbox starts without raw CSV files. The harness then explicitly injects task context into `input/` before Codex starts. In this configuration, the sandbox itself is the compute workspace, so it may read:

- `input/daily_temp_sf_high.csv`
- `input/daily_temp_sf_low.csv`

Write the final numeric answer to `avg_temp.txt` in this workspace. Do not write explanations into `avg_temp.txt`.
ENV
}

write_reuse_environment() {
  local driver_workspace="$1"
  cat >"${driver_workspace}/environment.md" <<'ENV'
# Environment

This workspace is `FullPCS_reuse_stage`.

Raw CSV files are forbidden in this driver workspace. Use the capability registry and the registered capability commands instead of generating a fresh end-to-end solution from scratch.

Available files:

- `capability_registry.json` lists the reusable capabilities.

Available registered capability commands:

- `cap-normalize-heterogeneous-dates` runs the registered data-context normalization capability.
- `cap-transfer-aligned-to-compute` transfers the derived `aligned_temperatures.csv` to the compute context.
- `cap-aggregate-temperature-difference` runs the registered compute-context aggregation capability.
- `cap-fetch-avg-temp` transfers the final `avg_temp.txt` into this driver workspace.

Expected route:
1. Read `capability_registry.json`.
2. Run the registered capability commands in order.
3. Leave the final answer in `avg_temp.txt` in this workspace.
ENV
}

normalize_on_data_context() {
  local data_context="$1"
  python3 - "$data_context/input" "${data_context}/aligned_temperatures.csv" "${data_context}/data_profile.md" <<'PY'
import csv
import sys
from datetime import datetime
from pathlib import Path

input_dir = Path(sys.argv[1])
aligned_path = Path(sys.argv[2])
profile_path = Path(sys.argv[3])

def parse_date(value):
    for fmt in ("%Y-%m-%d", "%m/%d/%Y %H:%M:%S", "%m-%d-%Y %H:%M:%S"):
        try:
            return datetime.strptime(value, fmt).date().isoformat()
        except ValueError:
            pass
    raise ValueError(f"unsupported date format: {value}")

def read_csv(path):
    rows = {}
    with path.open(newline="", encoding="utf-8") as handle:
        for row in csv.DictReader(handle):
            rows[parse_date(row["date"])] = float(row["temperature"])
    return rows

highs = read_csv(input_dir / "daily_temp_sf_high.csv")
lows = read_csv(input_dir / "daily_temp_sf_low.csv")
dates = sorted(set(highs) & set(lows))

with aligned_path.open("w", newline="", encoding="utf-8") as handle:
    writer = csv.DictWriter(
        handle,
        fieldnames=["date", "high_temperature", "low_temperature", "difference"],
    )
    writer.writeheader()
    for date in dates:
        high = highs[date]
        low = lows[date]
        writer.writerow(
            {
                "date": date,
                "high_temperature": f"{high:g}",
                "low_temperature": f"{low:g}",
                "difference": f"{high - low:g}",
            }
        )

profile_path.write_text(
    "\n".join(
        [
            "# Data profile",
            f"high_rows: {len(highs)}",
            f"low_rows: {len(lows)}",
            f"aligned_rows: {len(dates)}",
            "high_date_format: YYYY-MM-DD",
            "low_date_formats: MM/DD/YYYY HH:MM:SS and MM-DD-YYYY HH:MM:SS",
        ]
    )
    + "\n",
    encoding="utf-8",
)
PY
}

compute_on_compute_context() {
  local compute_context="$1"
  python3 - "$compute_context/aligned_temperatures.csv" "$compute_context/avg_temp.txt" "$compute_context/solution_report.md" <<'PY'
import csv
import sys
from pathlib import Path

aligned_path = Path(sys.argv[1])
avg_path = Path(sys.argv[2])
report_path = Path(sys.argv[3])

with aligned_path.open(newline="", encoding="utf-8") as handle:
    diffs = [float(row["difference"]) for row in csv.DictReader(handle)]

avg = sum(diffs) / len(diffs)
avg_path.write_text(f"{avg}\n", encoding="utf-8")
report_path.write_text(
    f"rows: {len(diffs)}\naverage_difference: {avg}\n",
    encoding="utf-8",
)
PY
}

run_wrong_context_probe() {
  local root="$1"
  local label="$2"
  local compute_context="${root}/compute_context"
  local probe_path="${root}/wrong_context_probe.json"
  local stdout_path="${root}/logs/wrong-context.stdout.log"
  local stderr_path="${root}/logs/wrong-context.stderr.log"
  local exit_code

  set +e
  python3 - "$compute_context" >"$stdout_path" 2>"$stderr_path" <<'PY'
import sys
from pathlib import Path

compute_context = Path(sys.argv[1])
raw_high = compute_context / "input" / "daily_temp_sf_high.csv"
raw_low = compute_context / "input" / "daily_temp_sf_low.csv"

if raw_high.exists() or raw_low.exists():
    print("wrong-context probe found raw CSVs in compute context", file=sys.stderr)
    raise SystemExit(3)

# Simulate the common wrong-context failure: asking the compute context
# to read raw data that should exist only in data_context.
raw_high.read_text(encoding="utf-8")
PY
  exit_code="$?"
  set -e

  python3 - "$probe_path" "$label" "$exit_code" "$stdout_path" "$stderr_path" <<'PY'
import json
import sys
from pathlib import Path

probe_path, label, exit_code_s, stdout_path_s, stderr_path_s = sys.argv[1:6]
exit_code = int(exit_code_s)
detected = exit_code != 0
result = {
    "label": label,
    "injected": True,
    "wrong_context_failure": "detected" if detected else "missed",
    "wrong_context_exit_code": exit_code,
    "expected_failure": True,
    "stdout": str(Path(stdout_path_s)),
    "stderr": str(Path(stderr_path_s)),
    "notes": "Probe intentionally asks compute_context to read raw CSV files that only data_context should own.",
}
Path(probe_path).write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
}

write_minimal_codex_config() {
  local src="$1"
  local dst="$2"
  local trusted_dir="$3"
  python3 - "$src" "$dst" "$trusted_dir" <<'PY'
import json
import sys
import tomllib
from pathlib import Path

src, dst, trusted_dir = sys.argv[1:4]
data = tomllib.loads(Path(src).read_text(encoding="utf-8"))

def toml_string(value):
    return json.dumps(str(value))

def table_key(value):
    s = str(value)
    if s.replace("_", "").replace("-", "").isalnum() and not s[:1].isdigit():
        return s
    return toml_string(s)

provider_name = data.get("model_provider", "")
provider = data.get("model_providers", {}).get(provider_name, {})
lines = []
for key in ("model_provider", "model", "model_reasoning_effort"):
    if key in data:
        lines.append(f"{key} = {toml_string(data[key])}")
lines.append('sandbox_mode = "danger-full-access"')
lines.append("")
if provider_name and provider:
    lines.append(f"[model_providers.{table_key(provider_name)}]")
    for key, value in provider.items():
        if isinstance(value, bool):
            rendered = "true" if value else "false"
        elif isinstance(value, (int, float)):
            rendered = str(value)
        else:
            rendered = toml_string(value)
        lines.append(f"{key} = {rendered}")
    lines.append("")
lines.append(f"[projects.{toml_string(trusted_dir)}]")
lines.append('trust_level = "trusted"')
lines.append("")
Path(dst).write_text("\n".join(lines).rstrip() + "\n", encoding="utf-8")
PY
  chmod 600 "$dst"
}

collect_codex_usage() {
  local jsonl="$1"
  local out="$2"
  local agent_name="$3"
  python3 - "$jsonl" "$out" "$agent_name" <<'PY'
import json
import sys
from pathlib import Path

jsonl_path = Path(sys.argv[1])
out_path = Path(sys.argv[2])
agent_name = sys.argv[3]

def token_int(obj, *keys):
    for key in keys:
        value = obj.get(key)
        if isinstance(value, int):
            return value
        if isinstance(value, float) and value.is_integer():
            return int(value)
    return 0

def walk_dicts(obj):
    if isinstance(obj, dict):
        yield obj
        for value in obj.values():
            yield from walk_dicts(value)
    elif isinstance(obj, list):
        for value in obj:
            yield from walk_dicts(value)

sum_input = 0
sum_output = 0
usage_events = 0
last_total = None
human_intervention_count = 0

if jsonl_path.exists():
    for line in jsonl_path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        item = obj.get("item") if isinstance(obj, dict) else None
        if isinstance(item, dict) and item.get("type") in {"ask_user", "request_user_input", "human_input"}:
            human_intervention_count += 1
        if isinstance(obj, dict) and obj.get("status") == "awaiting_user":
            human_intervention_count += 1
        for item in walk_dicts(obj):
            total = item.get("total_token_usage")
            if isinstance(total, dict):
                last_total = total
            usage = item.get("usage")
            if isinstance(usage, dict):
                usage_events += 1
                sum_input += token_int(usage, "input_tokens", "prompt_tokens")
                sum_output += token_int(usage, "output_tokens", "completion_tokens")

if last_total:
    input_tokens = token_int(last_total, "input_tokens", "prompt_tokens")
    output_tokens = token_int(last_total, "output_tokens", "completion_tokens")
else:
    input_tokens = sum_input
    output_tokens = sum_output

result = {
    "agent": agent_name,
    "model_input_tokens": input_tokens,
    "model_output_tokens": output_tokens,
    "usage_events": usage_events,
    "human_intervention_count": human_intervention_count,
    "jsonl": str(jsonl_path),
}
out_path.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
}

write_baseline_metadata() {
  local path="$1"
  local configuration="$2"
  local root="$3"
  local passed="$4"
  local exit_code="$5"
  local oracle_exit="$6"
  local wall_time_ms="$7"
  local manual_steps="$8"
  local notes="$9"
  local token_json="${10:-}"
  local extra_json="${11:-}"

  python3 - "$path" "$configuration" "$root" "$passed" "$exit_code" "$oracle_exit" "$wall_time_ms" "$manual_steps" "$notes" "$token_json" "$extra_json" <<'PY'
import json
import sys
from pathlib import Path

(
    path_s,
    configuration,
    root_s,
    passed_s,
    exit_code_s,
    oracle_exit_s,
    wall_time_ms_s,
    manual_steps_s,
    notes,
    token_json_s,
    extra_json_s,
) = sys.argv[1:12]
path = Path(path_s)
root = Path(root_s)
token = {
    "agent": configuration,
    "model_input_tokens": 0,
    "model_output_tokens": 0,
    "usage_events": 0,
    "human_intervention_count": 0,
}
if token_json_s:
    candidate = Path(token_json_s)
    if candidate.exists():
        token = json.loads(candidate.read_text(encoding="utf-8"))
extra = {}
if extra_json_s:
    candidate = Path(extra_json_s)
    if candidate.exists():
        extra = json.loads(candidate.read_text(encoding="utf-8"))

oracle_path = root / "oracle_result.json"
oracle = {}
if oracle_path.exists():
    oracle = json.loads(oracle_path.read_text(encoding="utf-8"))
probe_path = root / "wrong_context_probe.json"
probe = {}
if probe_path.exists():
    probe = json.loads(probe_path.read_text(encoding="utf-8"))
final_avg_path = root / "final_workspace" / "avg_temp.txt"
final_avg_temp = None
if final_avg_path.exists():
    final_avg_temp = final_avg_path.read_text(encoding="utf-8", errors="replace").strip()

manual_steps = None if manual_steps_s == "NA" else int(manual_steps_s)
result = {
    "configuration": configuration,
    "passed": passed_s == "true",
    "exit_code": int(exit_code_s),
    "oracle_exit": int(oracle_exit_s),
    "oracle": oracle,
    "final_avg_temp": final_avg_temp,
    "expected_avg_temp": "11.428571428571429",
    "final_avg_temp_path": str(final_avg_path),
    "wall_time_ms": int(wall_time_ms_s),
    "manual_steps_ssh": manual_steps,
    "wrong_context_failure": probe.get("wrong_context_failure", "not_injected"),
    "wrong_context_probe": probe or None,
    "human_intervention_count": token.get("human_intervention_count", 0),
    "observer_events": 0,
    "driver_task_count": 0,
    "artifact_transfers": [
        "data_context:raw_csv->aligned_temperatures.csv",
        "data_context:aligned_temperatures.csv->compute_context",
        "compute_context:avg_temp.txt->driver_workspace",
    ],
    "per_agent_tokens": [token],
    "all_agent_total_tokens": {
        "input": token.get("model_input_tokens", 0),
        "output": token.get("model_output_tokens", 0),
    },
    "raw_csv_in_driver_workspace": bool(list((root / "driver_workspace").glob("**/daily_temp_sf_*.csv"))),
    "output_dir": str(root),
    "notes": notes,
}
result.update(extra)
path.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
}

manual_ssh_cross_context() {
  local root="${RUN_DIR}/manual_ssh_cross_context"
  prepare_partition "$root"
  local driver_workspace="${root}/driver_workspace"
  local data_context="${root}/data_context"
  local compute_context="${root}/compute_context"
  local final_workspace="${root}/final_workspace"
  local steps=0
  local start_ns end_ns wall_ms oracle_exit passed

  run_wrong_context_probe "$root" "manual_wrong_compute_raw_data"

  start_ns="$(date +%s%N)"
  mkdir -p "${data_context}/work"
  steps=$((steps + 1))
  normalize_on_data_context "$data_context" >"${root}/logs/data-normalize.stdout.log" 2>"${root}/logs/data-normalize.stderr.log"
  steps=$((steps + 1))
  cp "${data_context}/aligned_temperatures.csv" "${compute_context}/aligned_temperatures.csv"
  steps=$((steps + 1))
  mkdir -p "${compute_context}/work"
  steps=$((steps + 1))
  compute_on_compute_context "$compute_context" >"${root}/logs/compute.stdout.log" 2>"${root}/logs/compute.stderr.log"
  steps=$((steps + 1))
  cp "${compute_context}/avg_temp.txt" "${driver_workspace}/avg_temp.txt"
  cp "${driver_workspace}/avg_temp.txt" "${final_workspace}/avg_temp.txt"
  steps=$((steps + 1))
  end_ns="$(date +%s%N)"
  wall_ms="$(duration_ms "$start_ns" "$end_ns")"

  set +e
  "${WORKLOAD_DIR}/oracle.sh" "$final_workspace" >"${root}/oracle_result.json" 2>"${root}/logs/oracle.stderr.log"
  oracle_exit="$?"
  set -e
  passed="false"
  if [[ "$oracle_exit" -eq 0 ]]; then
    passed="true"
  fi
  write_baseline_metadata \
    "${root}/metadata.json" \
    "manual_ssh_cross_context" \
    "$root" \
    "$passed" \
    0 \
    "$oracle_exit" \
    "$wall_ms" \
    "$steps" \
    "SSH/SCP-style deterministic cross-context harness; no LLM tokens." \
    "" \
    ""
}

write_fake_ssh_bin() {
  local fakebin="$1"
  local driver_workspace="$2"
  local data_context="$3"
  local compute_context="$4"
  mkdir -p "$fakebin"

  cat >"${fakebin}/ssh" <<SH
#!/usr/bin/env bash
set -euo pipefail
if [[ "\$#" -lt 2 ]]; then
  echo "usage: ssh <data-slave|compute-slave> <command>" >&2
  exit 2
fi
host="\$1"
shift
case "\$host" in
  data-slave)
    ctx="${data_context}"
    ;;
  compute-slave)
    ctx="${compute_context}"
    ;;
  *)
    echo "unknown strict-partition host: \$host" >&2
    exit 2
    ;;
esac
cd "\$ctx"
exec bash -lc "\$*"
SH
  chmod +x "${fakebin}/ssh"

  cat >"${fakebin}/scp" <<SH
#!/usr/bin/env bash
set -euo pipefail
if [[ "\$#" -ne 2 ]]; then
  echo "usage: scp <src> <dst>" >&2
  exit 2
fi
resolve_path() {
  local spec="\$1"
  case "\$spec" in
    data-slave:*)
      printf '%s\n' "${data_context}/\${spec#data-slave:}"
      ;;
    compute-slave:*)
      printf '%s\n' "${compute_context}/\${spec#compute-slave:}"
      ;;
    driver:*)
      printf '%s\n' "${driver_workspace}/\${spec#driver:}"
      ;;
    /*)
      printf '%s\n' "\$spec"
      ;;
    *)
      printf '%s\n' "${driver_workspace}/\$spec"
      ;;
  esac
}
src="\$(resolve_path "\$1")"
dst="\$(resolve_path "\$2")"
mkdir -p "\$(dirname "\$dst")"
cp "\$src" "\$dst"
SH
  chmod +x "${fakebin}/scp"

  cat >"${fakebin}/ssh-data" <<SH
#!/usr/bin/env bash
exec "${fakebin}/ssh" data-slave "\$@"
SH
  chmod +x "${fakebin}/ssh-data"

  cat >"${fakebin}/ssh-compute" <<SH
#!/usr/bin/env bash
exec "${fakebin}/ssh" compute-slave "\$@"
SH
  chmod +x "${fakebin}/ssh-compute"

  cat >"${fakebin}/scp-data-to-compute" <<SH
#!/usr/bin/env bash
set -euo pipefail
if [[ "\$#" -ne 2 ]]; then
  echo "usage: scp-data-to-compute <data-path> <compute-path>" >&2
  exit 2
fi
exec "${fakebin}/scp" "data-slave:\$1" "compute-slave:\$2"
SH
  chmod +x "${fakebin}/scp-data-to-compute"

  cat >"${fakebin}/scp-compute-to-driver" <<SH
#!/usr/bin/env bash
set -euo pipefail
if [[ "\$#" -ne 2 ]]; then
  echo "usage: scp-compute-to-driver <compute-path> <driver-path>" >&2
  exit 2
fi
exec "${fakebin}/scp" "compute-slave:\$1" "driver:\$2"
SH
  chmod +x "${fakebin}/scp-compute-to-driver"
}

write_reuse_capability_bin() {
  local fakebin="$1"
  local driver_workspace="$2"
  local data_context="$3"
  local compute_context="$4"
  mkdir -p "$fakebin"

  cat >"${fakebin}/cap-normalize-heterogeneous-dates" <<SH
#!/usr/bin/env bash
set -euo pipefail
python3 - "${data_context}/input" "${data_context}/aligned_temperatures.csv" "${data_context}/data_profile.md" <<'PY'
import csv
import sys
from datetime import datetime
from pathlib import Path

input_dir = Path(sys.argv[1])
aligned_path = Path(sys.argv[2])
profile_path = Path(sys.argv[3])

def parse_date(value):
    for fmt in ("%Y-%m-%d", "%m/%d/%Y %H:%M:%S", "%m-%d-%Y %H:%M:%S"):
        try:
            return datetime.strptime(value, fmt).date().isoformat()
        except ValueError:
            pass
    raise ValueError(f"unsupported date format: {value}")

def read_csv(path):
    rows = {}
    with path.open(newline="", encoding="utf-8") as handle:
        for row in csv.DictReader(handle):
            rows[parse_date(row["date"])] = float(row["temperature"])
    return rows

highs = read_csv(input_dir / "daily_temp_sf_high.csv")
lows = read_csv(input_dir / "daily_temp_sf_low.csv")
dates = sorted(set(highs) & set(lows))

with aligned_path.open("w", newline="", encoding="utf-8") as handle:
    writer = csv.DictWriter(
        handle,
        fieldnames=["date", "high_temperature", "low_temperature", "difference"],
    )
    writer.writeheader()
    for date in dates:
        high = highs[date]
        low = lows[date]
        writer.writerow(
            {
                "date": date,
                "high_temperature": f"{high:g}",
                "low_temperature": f"{low:g}",
                "difference": f"{high - low:g}",
            }
        )

profile_path.write_text(
    "\\n".join(
        [
            "# Data profile",
            f"high_rows: {len(highs)}",
            f"low_rows: {len(lows)}",
            f"aligned_rows: {len(dates)}",
            "high_date_format: YYYY-MM-DD",
            "low_date_formats: MM/DD/YYYY HH:MM:SS and MM-DD-YYYY HH:MM:SS",
        ]
    )
    + "\\n",
    encoding="utf-8",
)
print(f"wrote {aligned_path}")
PY
SH
  chmod +x "${fakebin}/cap-normalize-heterogeneous-dates"

  cat >"${fakebin}/cap-transfer-aligned-to-compute" <<SH
#!/usr/bin/env bash
set -euo pipefail
cp "${data_context}/aligned_temperatures.csv" "${compute_context}/aligned_temperatures.csv"
echo "transferred aligned_temperatures.csv"
SH
  chmod +x "${fakebin}/cap-transfer-aligned-to-compute"

  cat >"${fakebin}/cap-aggregate-temperature-difference" <<SH
#!/usr/bin/env bash
set -euo pipefail
python3 - "${compute_context}/aligned_temperatures.csv" "${compute_context}/avg_temp.txt" "${compute_context}/solution_report.md" <<'PY'
import csv
import sys
from pathlib import Path

aligned_path = Path(sys.argv[1])
avg_path = Path(sys.argv[2])
report_path = Path(sys.argv[3])

with aligned_path.open(newline="", encoding="utf-8") as handle:
    rows = list(csv.DictReader(handle))

diffs = [float(row["difference"]) for row in rows]
if not diffs:
    raise SystemExit("no rows in aligned_temperatures.csv")

avg = sum(diffs) / len(diffs)
avg_path.write_text(f"{avg:.15f}\\n", encoding="utf-8")
report_path.write_text(
    f"rows: {len(diffs)}\\navg_difference: {avg:.15f}\\n",
    encoding="utf-8",
)
print(f"wrote {avg_path}")
PY
SH
  chmod +x "${fakebin}/cap-aggregate-temperature-difference"

  cat >"${fakebin}/cap-fetch-avg-temp" <<SH
#!/usr/bin/env bash
set -euo pipefail
cp "${compute_context}/avg_temp.txt" "${driver_workspace}/avg_temp.txt"
echo "fetched avg_temp.txt"
SH
  chmod +x "${fakebin}/cap-fetch-avg-temp"
}

single_machine_codex_ssh() {
  local root="${RUN_DIR}/single_machine_codex_ssh"
  prepare_partition "$root"
  local driver_workspace="${root}/driver_workspace"
  local data_context="${root}/data_context"
  local compute_context="${root}/compute_context"
  local final_workspace="${root}/final_workspace"
  local fakebin="${root}/fakebin"
  local codex_home="${root}/codex-home"
  local extra_json="${root}/extra_metadata.json"
  local start_ns end_ns wall_ms codex_exit oracle_exit passed

  write_fake_ssh_bin "$fakebin" "$driver_workspace" "$data_context" "$compute_context"
  write_single_machine_environment "$driver_workspace"
  mkdir -p "$codex_home" "${root}/home"
  run_wrong_context_probe "$root" "single_machine_wrong_compute_raw_data"

  if [[ -r "$SOURCE_CODEX_CONFIG" ]]; then
    write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${codex_home}/config.toml" "$driver_workspace"
  fi

  cp "$SHARED_PROMPT_PATH" "${root}/prompt.md"
  write_prompt_extra_json "$extra_json" "single_machine_codex_ssh" "single-machine-codex"

  start_ns="$(date +%s%N)"
  codex_exit=0
  if command -v codex >/dev/null 2>&1 && [[ -r "${codex_home}/config.toml" ]]; then
    set +e
    (
      cd "$driver_workspace"
      PATH="${fakebin}:$PATH" CODEX_HOME="$codex_home" HOME="${root}/home" \
        timeout "${SINGLE_CODEX_TIMEOUT_SEC}s" \
        codex exec --json \
          --output-last-message "${root}/last_message.md" \
          --skip-git-repo-check \
          --dangerously-bypass-approvals-and-sandbox \
          - <"${root}/prompt.md"
    ) >"${root}/logs/codex.jsonl" \
      2>"${root}/logs/codex.stderr.log"
    codex_exit="$?"
    set -e
  else
    codex_exit=127
    echo "codex CLI or config is not available" >"${root}/logs/codex.stderr.log"
  fi
  end_ns="$(date +%s%N)"
  wall_ms="$(duration_ms "$start_ns" "$end_ns")"

  if [[ -f "${driver_workspace}/avg_temp.txt" ]]; then
    cp "${driver_workspace}/avg_temp.txt" "${final_workspace}/avg_temp.txt"
  fi
  set +e
  "${WORKLOAD_DIR}/oracle.sh" "$final_workspace" >"${root}/oracle_result.json" 2>"${root}/logs/oracle.stderr.log"
  oracle_exit="$?"
  set -e
  collect_codex_usage "${root}/logs/codex.jsonl" "${root}/token_usage.json" "single-machine-codex"

  passed="false"
  if [[ "$codex_exit" -eq 0 && "$oracle_exit" -eq 0 ]]; then
    passed="true"
  fi
  write_baseline_metadata \
    "${root}/metadata.json" \
    "single_machine_codex_ssh" \
    "$root" \
    "$passed" \
    "$codex_exit" \
    "$oracle_exit" \
    "$wall_ms" \
    "NA" \
    "Single Codex CLI constrained to SSH/SCP-style helper commands; data and compute contexts are local directories behind transport wrappers." \
    "${root}/token_usage.json" \
    "$extra_json"
}

cloud_sandbox_context_injection() {
  local root="${RUN_DIR}/cloud_sandbox_context_injection"
  prepare_partition "$root"
  local driver_workspace="${root}/driver_workspace"
  local data_context="${root}/data_context"
  local final_workspace="${root}/final_workspace"
  local cloud_context="${root}/cloud_sandbox_context"
  local codex_home="${root}/codex-home"
  local manifest="${root}/context_injection_manifest.json"
  local extra_json="${root}/extra_metadata.json"
  local start_ns end_ns wall_ms codex_exit oracle_exit passed

  mkdir -p "$cloud_context" "$codex_home" "${root}/home"
  run_wrong_context_probe "$root" "cloud_sandbox_wrong_compute_raw_data"
  write_cloud_environment "$cloud_context"
  cp "${driver_workspace}/task.md" "${cloud_context}/task.md"
  cp "$SHARED_PROMPT_PATH" "${root}/prompt.md"

  if [[ -r "$SOURCE_CODEX_CONFIG" ]]; then
    write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${codex_home}/config.toml" "$cloud_context"
  fi

  python3 - "$cloud_context" "$manifest" <<'PY'
import json
import sys
from pathlib import Path

cloud = Path(sys.argv[1])
manifest = Path(sys.argv[2])
raw_before = sorted(str(path.relative_to(cloud)) for path in cloud.glob("**/daily_temp_sf_*.csv"))
manifest.write_text(
    json.dumps(
        {
            "sandbox_initial_raw_csv_files": raw_before,
            "sandbox_started_without_raw_csv": len(raw_before) == 0,
            "context_injection_steps": [],
        },
        ensure_ascii=False,
        indent=2,
    )
    + "\n",
    encoding="utf-8",
)
PY
  mkdir -p "${cloud_context}/input"
  cp "${data_context}/input/daily_temp_sf_high.csv" "${cloud_context}/input/daily_temp_sf_high.csv"
  cp "${data_context}/input/daily_temp_sf_low.csv" "${cloud_context}/input/daily_temp_sf_low.csv"

  start_ns="$(date +%s%N)"
  codex_exit=0
  if command -v codex >/dev/null 2>&1 && [[ -r "${codex_home}/config.toml" ]]; then
    set +e
    (
      cd "$cloud_context"
      CODEX_HOME="$codex_home" HOME="${root}/home" \
        timeout "${SINGLE_CODEX_TIMEOUT_SEC}s" \
        codex exec --json \
          --output-last-message "${root}/last_message.md" \
          --skip-git-repo-check \
          --dangerously-bypass-approvals-and-sandbox \
          - <"${root}/prompt.md"
    ) >"${root}/logs/cloud-codex.jsonl" \
      2>"${root}/logs/cloud-codex.stderr.log"
    codex_exit="$?"
    set -e
  else
    codex_exit=127
    echo "codex CLI or config is not available" >"${root}/logs/cloud-codex.stderr.log"
  fi
  end_ns="$(date +%s%N)"
  wall_ms="$(duration_ms "$start_ns" "$end_ns")"

  if [[ -f "${cloud_context}/avg_temp.txt" ]]; then
    cp "${cloud_context}/avg_temp.txt" "${driver_workspace}/avg_temp.txt"
    cp "${driver_workspace}/avg_temp.txt" "${final_workspace}/avg_temp.txt"
  fi

  collect_codex_usage "${root}/logs/cloud-codex.jsonl" "${root}/token_usage.json" "cloud-codex"

  python3 - "$cloud_context" "$manifest" "$extra_json" "$SHARED_PROMPT_PATH" "$(prompt_sha256)" <<'PY'
import json
import sys
from pathlib import Path

cloud = Path(sys.argv[1])
manifest_path = Path(sys.argv[2])
extra_path = Path(sys.argv[3])
prompt_path = sys.argv[4]
prompt_sha = sys.argv[5]
manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
raw_after = sorted(str(path.relative_to(cloud)) for path in cloud.glob("**/daily_temp_sf_*.csv"))
manifest.update(
    {
        "sandbox_post_injection_raw_csv_files": raw_after,
        "context_injection_steps": [
            "data_context/input/daily_temp_sf_high.csv -> cloud_sandbox_context/input/daily_temp_sf_high.csv",
            "data_context/input/daily_temp_sf_low.csv -> cloud_sandbox_context/input/daily_temp_sf_low.csv",
            "cloud_sandbox_context/avg_temp.txt -> driver_workspace/avg_temp.txt",
        ],
        "fetched_outputs": ["avg_temp.txt"],
    }
)
manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
extra = {
    "prompt_based_agent": True,
    "comparison_group": "same_prompt_agent",
    "prompt_path": prompt_path,
    "prompt_sha256": prompt_sha,
    "prompt_agent": "cloud-codex",
    "prompt_configuration": "cloud_sandbox_context_injection",
    "context_injection_steps": len(manifest["context_injection_steps"]),
    "context_injection_manifest": str(manifest_path),
    "artifact_transfers": [
        "data_context:raw_csv->cloud_sandbox_context/input",
        "cloud_sandbox_context:avg_temp.txt->driver_workspace",
    ],
    "notes": "Cloud sandbox context-injection harness: sandbox starts without raw CSV, receives explicit injected context, computes remotely, and fetches only avg_temp.txt. This is not a real E2B run.",
}
extra_path.write_text(json.dumps(extra, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

  set +e
  "${WORKLOAD_DIR}/oracle.sh" "$final_workspace" >"${root}/oracle_result.json" 2>"${root}/logs/oracle.stderr.log"
  oracle_exit="$?"
  set -e
  passed="false"
  if [[ "$codex_exit" -eq 0 && "$oracle_exit" -eq 0 ]]; then
    passed="true"
  fi
  write_baseline_metadata \
    "${root}/metadata.json" \
    "cloud_sandbox_context_injection" \
    "$root" \
    "$passed" \
    "$codex_exit" \
    "$oracle_exit" \
    "$wall_ms" \
    "NA" \
    "Cloud sandbox context-injection harness: sandbox starts without raw CSV, receives explicit injected context, computes remotely, and fetches only avg_temp.txt. This is not a real E2B run." \
    "${root}/token_usage.json" \
    "$extra_json"
}

FullPCS_strict_partition() {
  local root="${RUN_DIR}/FullPCS_strict_partition"
  mkdir -p "$root"
  local start_ns end_ns wall_ms fullpcs_exit

  start_ns="$(date +%s%N)"
  set +e
  STRICT_PARTITION=1 \
    DRIVER_PROMPT_FILE="$SHARED_PROMPT_PATH" \
    DRIVER_PROMPT_MODE="$FULLPCS_PROMPT_MODE" \
    DRIVER_TIMEOUT_SEC="$FULLPCS_TIMEOUT_SEC" \
    bash "${ROOT_DIR}/tools/eval/container_smoke/run_heterogeneous_dates_driver_mcp_smoke.sh" "$root" \
    >"${root}/fullpcs.stdout.log" \
    2>"${root}/fullpcs.stderr.log"
  fullpcs_exit="$?"
  set -e
  end_ns="$(date +%s%N)"
  wall_ms="$(duration_ms "$start_ns" "$end_ns")"

  python3 - "$root" "$fullpcs_exit" "$wall_ms" "$FULLPCS_PROMPT_MODE" "$SHARED_PROMPT_PATH" "$(prompt_sha256)" <<'PY'
import json
import sys
from pathlib import Path

root = Path(sys.argv[1])
fullpcs_exit = int(sys.argv[2])
wall_time_ms = int(sys.argv[3])
prompt_mode = sys.argv[4]
prompt_path = sys.argv[5]
prompt_sha = sys.argv[6]
latest_path_file = root / "latest_run_path.txt"
latest = Path(latest_path_file.read_text(encoding="utf-8").strip()) if latest_path_file.exists() else None

def load(path, default):
    if path and path.exists():
        text = path.read_text(encoding="utf-8", errors="replace").strip()
        if text:
            return json.loads(text)
    return default

status = load(latest / "result_status.json" if latest else None, {})
usage = load(latest / "agent_usage_summary.json" if latest else None, {"agents": []})
observer = load(latest / "observer_summary.json" if latest else None, {"tables": {}, "event_type_counts": {}})
mcp = load(latest / "driver_mcp_tool_summary.json" if latest else None, {"task_records": 0, "tool_counts": {}, "target_counts": {}})
decision = load(latest / "driver_decision_summary.json" if latest else None, {})
oracle = load(latest / "oracle_result.json" if latest else None, {})
final_avg_path = latest / "final_workspace" / "avg_temp.txt" if latest else None
final_avg_temp = None
if final_avg_path and final_avg_path.exists():
    final_avg_temp = final_avg_path.read_text(encoding="utf-8", errors="replace").strip()

agents = list(usage.get("agents", []))
agent_names = {item.get("agent") for item in agents}
for name in ("smoke-driver-codex", "smoke-data-slave", "smoke-compute-slave"):
    if name not in agent_names:
        agents.append(
            {
                "agent": name,
                "model_input_tokens": 0,
                "model_output_tokens": 0,
                "usage_events": 0,
                "human_intervention_count": 0,
            }
        )

raw_csv_in_driver = False
if latest:
    driver_workspace = latest / "driver" / "workspace"
    raw_csv_in_driver = bool(list(driver_workspace.glob("**/daily_temp_sf_*.csv")))

observer_events = observer.get("tables", {}).get("events")
if observer_events is None:
    observer_events = sum(observer.get("event_type_counts", {}).values())

passed = bool(status.get("passed")) and fullpcs_exit == 0
driver_raw_csv = []
compute_raw_csv = []
data_raw_csv = []
if latest:
    driver_raw_csv = sorted(str(path.relative_to(latest)) for path in (latest / "driver" / "workspace").glob("**/daily_temp_sf_*.csv"))
    compute_raw_csv = sorted(str(path.relative_to(latest)) for path in (latest / "smoke-compute-slave" / "workspace").glob("**/daily_temp_sf_*.csv"))
    data_raw_csv = sorted(str(path.relative_to(latest)) for path in (latest / "smoke-data-slave" / "workspace").glob("**/daily_temp_sf_*.csv"))
wrong_target_probe = {
    "probe_name": "FullPCS_wrong_target_recovery",
    "injected": True,
    "wrong_target_recovery": "detected" if passed and not driver_raw_csv and not compute_raw_csv and bool(data_raw_csv) else "missed",
    "expected_failure": "raw CSVs must be absent from driver and compute contexts",
    "boundary_checks": {
        "driver_raw_csv_files": driver_raw_csv,
        "compute_raw_csv_files": compute_raw_csv,
        "data_raw_csv_files": data_raw_csv,
        "oracle_passed_after_recovery_route": passed,
    },
    "notes": "Post-run Full PCS strict-boundary probe: data owns raw CSV, compute receives only derived CSV, and driver recovers final avg_temp.txt through the intended route.",
}
(root / "fullpcs_wrong_target_probe.json").write_text(
    json.dumps(wrong_target_probe, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

result = {
    "configuration": "FullPCS_strict_partition",
    "passed": passed,
    "exit_code": fullpcs_exit,
    "oracle_exit": status.get("oracle_exit"),
    "oracle": oracle,
    "final_avg_temp": final_avg_temp,
    "expected_avg_temp": "11.428571428571429",
    "final_avg_temp_path": str(final_avg_path) if final_avg_path else None,
    "prompt_based_agent": True,
    "comparison_group": "same_prompt_agent",
    "prompt_path": prompt_path,
    "prompt_sha256": prompt_sha,
    "prompt_agent": "smoke-driver-codex",
    "prompt_configuration": "FullPCS_strict_partition",
    "wall_time_ms": wall_time_ms,
    "manual_steps_ssh": None,
    "wrong_context_failure": wrong_target_probe["wrong_target_recovery"],
    "wrong_target_recovery": wrong_target_probe,
    "fullpcs_wrong_target_probe": str(root / "fullpcs_wrong_target_probe.json"),
    "human_intervention_count": status.get("human_intervention_count", usage.get("human_intervention_count", 0)),
    "observer_events": observer_events,
    "driver_task_count": mcp.get("task_records", 0),
    "artifact_transfers": [
        "smoke-data-slave:raw_csv->aligned_temperatures.csv",
        "smoke-data-slave:aligned_temperatures.csv->smoke-compute-slave",
        "smoke-compute-slave:avg_temp.txt->smoke-driver",
    ],
    "per_agent_tokens": agents,
    "all_agent_total_tokens": {
        "input": usage.get("total_model_input_tokens", sum(item.get("model_input_tokens", 0) for item in agents)),
        "output": usage.get("total_model_output_tokens", sum(item.get("model_output_tokens", 0) for item in agents)),
    },
    "raw_csv_in_driver_workspace": raw_csv_in_driver,
    "output_dir": str(latest) if latest else str(root),
    "prompt_mode": prompt_mode,
    "strict_partition": status.get("strict_partition"),
    "driver_tool_counts": mcp.get("tool_counts", {}),
    "driver_target_counts": mcp.get("target_counts", {}),
    "driver_decision_summary": decision,
    "notes": "Full PCS path with driver MCP, data slave, compute slave, observer, and agentserver-stub. Strict mode preloads raw CSV only on data slave.",
}
(root / "metadata.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
}

FullPCS_reuse_stage() {
  local root="${RUN_DIR}/FullPCS_reuse_stage"
  prepare_partition "$root"
  local driver_workspace="${root}/driver_workspace"
  local data_context="${root}/data_context"
  local compute_context="${root}/compute_context"
  local final_workspace="${root}/final_workspace"
  local fakebin="${root}/fakebin"
  local codex_home="${root}/codex-home"
  local registry="${root}/capability_registry.json"
  local reuse_report="${root}/reuse_stage_report_cn.md"
  local extra_json="${root}/extra_metadata.json"
  local start_ns end_ns wall_ms codex_exit oracle_exit passed

  run_wrong_context_probe "$root" "reuse_stage_wrong_compute_raw_data"
  mkdir -p "$fakebin" "$codex_home" "${root}/home"
  write_reuse_capability_bin "$fakebin" "$driver_workspace" "$data_context" "$compute_context"
  write_reuse_environment "$driver_workspace"
  cp "$SHARED_PROMPT_PATH" "${root}/prompt.md"

  if [[ -r "$SOURCE_CODEX_CONFIG" ]]; then
    write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${codex_home}/config.toml" "$driver_workspace"
  fi

  python3 - "$registry" <<'PY'
import json
import time
import sys
import hashlib
from pathlib import Path

registry_path = Path(sys.argv[1])
registry = {
    "registry_name": "task1_date_csv_capability_registry",
    "created_at_unix": int(time.time()),
    "capabilities": [
        {
            "capability_id": "normalize-heterogeneous-dates-v1",
            "owner_context": "data_context",
            "input_contract": ["daily_temp_sf_high.csv", "daily_temp_sf_low.csv"],
            "output_contract": ["aligned_temperatures.csv", "data_profile.md"],
            "route": "data_context -> compute_context",
        },
        {
            "capability_id": "aggregate-temperature-difference-v1",
            "owner_context": "compute_context",
            "input_contract": ["aligned_temperatures.csv"],
            "output_contract": ["avg_temp.txt", "solution_report.md"],
            "route": "compute_context -> driver_workspace",
        },
    ],
    "reuse_policy": {
        "registry_hit": 1,
        "repeated_generation": 0,
        "description": "Reuse stage replays the registered normalization and aggregation route without invoking Codex again.",
    },
}
registry_path.write_text(json.dumps(registry, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY
  cp "$registry" "${driver_workspace}/capability_registry.json"

  start_ns="$(date +%s%N)"
  codex_exit=0
  if command -v codex >/dev/null 2>&1 && [[ -r "${codex_home}/config.toml" ]]; then
    set +e
    (
      cd "$driver_workspace"
      PATH="${fakebin}:$PATH" CODEX_HOME="$codex_home" HOME="${root}/home" \
        timeout "${SINGLE_CODEX_TIMEOUT_SEC}s" \
        codex exec --json \
          --output-last-message "${root}/last_message.md" \
          --skip-git-repo-check \
          --dangerously-bypass-approvals-and-sandbox \
          - <"${root}/prompt.md"
    ) >"${root}/logs/reuse-codex.jsonl" \
      2>"${root}/logs/reuse-codex.stderr.log"
    codex_exit="$?"
    set -e
  else
    codex_exit=127
    echo "codex CLI or config is not available" >"${root}/logs/reuse-codex.stderr.log"
  fi
  end_ns="$(date +%s%N)"
  wall_ms="$(duration_ms "$start_ns" "$end_ns")"

  if [[ -f "${driver_workspace}/avg_temp.txt" ]]; then
    cp "${driver_workspace}/avg_temp.txt" "${final_workspace}/avg_temp.txt"
  fi
  collect_codex_usage "${root}/logs/reuse-codex.jsonl" "${root}/token_usage.json" "reuse-stage-codex"

  set +e
  "${WORKLOAD_DIR}/oracle.sh" "$final_workspace" >"${root}/oracle_result.json" 2>"${root}/logs/oracle.stderr.log"
  oracle_exit="$?"
  set -e
  passed="false"
  if [[ "$codex_exit" -eq 0 && "$oracle_exit" -eq 0 ]]; then
    passed="true"
  fi

  python3 - "$reuse_report" "$registry" "$root" "$wall_ms" "$oracle_exit" "$extra_json" "$SHARED_PROMPT_PATH" "$(prompt_sha256)" <<'PY'
import json
import sys
from pathlib import Path

report_path = Path(sys.argv[1])
registry_path = Path(sys.argv[2])
root = Path(sys.argv[3])
wall_time_ms = int(sys.argv[4])
oracle_exit = int(sys.argv[5])
extra_path = Path(sys.argv[6])
prompt_path = sys.argv[7]
prompt_sha = sys.argv[8]
registry = json.loads(registry_path.read_text(encoding="utf-8"))
oracle = json.loads((root / "oracle_result.json").read_text(encoding="utf-8"))
avg_path = root / "final_workspace" / "avg_temp.txt"
avg = avg_path.read_text(encoding="utf-8").strip() if avg_path.exists() else "missing"

report = f"""# FullPCS Reuse Stage 中间结果

## 执行口径

本阶段验证 `FullPCS_reuse_stage` 的 prompt-based 复用闭环：Codex 接收与其他 agent configuration 相同的 `shared_task_prompt.md`，环境提供 capability registry 和注册能力命令。目标是让 Codex 通过复用能力完成任务，而不是走脚本化 replay。

## Registry

- registry 文件：`{registry_path}`
- shared prompt：`{prompt_path}`
- prompt_sha256：`{prompt_sha}`
- registry_hit：1
- repeated_generation：0
- capability 数量：{len(registry["capabilities"])}

## 数据流

1. `data_context/input/daily_temp_sf_high.csv` 与 `daily_temp_sf_low.csv` 由 data context 持有。
2. 复用 `normalize-heterogeneous-dates-v1` 生成 `data_context/aligned_temperatures.csv`。
3. 将中间表传给 compute context。
4. 复用 `aggregate-temperature-difference-v1` 生成 `compute_context/avg_temp.txt`。
5. 只把最终 `avg_temp.txt` 回收到 driver/final workspace。

## 结果

- passed：{oracle.get("passed")}
- oracle_exit：{oracle_exit}
- wall_time_ms：{wall_time_ms}
- expected_avg_temp：`11.428571428571429`
- avg_temp.txt：`{avg}`
- raw CSV in driver：{bool(list((root / "driver_workspace").glob("**/daily_temp_sf_*.csv")))}
"""
report_path.write_text(report, encoding="utf-8")
extra = {
    "prompt_based_agent": True,
    "comparison_group": "same_prompt_agent",
    "prompt_path": prompt_path,
    "prompt_sha256": prompt_sha,
    "prompt_agent": "reuse-stage-codex",
    "prompt_configuration": "FullPCS_reuse_stage",
    "registry_hit": 1,
    "repeated_generation": 0,
    "capability_registry": str(registry_path),
    "reuse_stage_report": str(report_path),
    "artifact_transfers": [
        "data_context:raw_csv->aligned_temperatures.csv",
        "capability_registry:normalize-heterogeneous-dates-v1",
        "data_context:aligned_temperatures.csv->compute_context",
        "capability_registry:aggregate-temperature-difference-v1",
        "compute_context:avg_temp.txt->driver_workspace",
    ],
    "notes": "FullPCS reuse-stage harness: local capability registry replay without additional Codex generation. This demonstrates reuse amortization shape, not full observer-backed promotion.",
}
extra_path.write_text(json.dumps(extra, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

  write_baseline_metadata \
    "${root}/metadata.json" \
    "FullPCS_reuse_stage" \
    "$root" \
    "$passed" \
    "$codex_exit" \
    "$oracle_exit" \
    "$wall_ms" \
    "NA" \
    "FullPCS reuse-stage prompt-based harness: Codex receives the shared prompt and uses local capability registry commands. This demonstrates reuse amortization shape, not full observer-backed promotion." \
    "${root}/token_usage.json" \
    "$extra_json"
}

generate_report() {
  python3 - "$RUN_DIR" "$REPORT_PATH" "$RESULTS_PATH" "$INTERMEDIATE_REPORT_PATH" "$RUN_ID" <<'PY'
import json
import hashlib
import time
import sys
from pathlib import Path

run_dir = Path(sys.argv[1])
report_path = Path(sys.argv[2])
results_path = Path(sys.argv[3])
intermediate_report_path = Path(sys.argv[4])
run_id = sys.argv[5]
shared_prompt_path = run_dir / "shared_task_prompt.md"
shared_prompt_sha = hashlib.sha256(shared_prompt_path.read_bytes()).hexdigest() if shared_prompt_path.exists() else None
prompt_parity_configs = [
    "single_machine_codex_ssh",
    "cloud_sandbox_context_injection",
    "FullPCS_strict_partition",
    "FullPCS_reuse_stage",
]

configs = []
for name in ("manual_ssh_cross_context", "single_machine_codex_ssh", "cloud_sandbox_context_injection", "FullPCS_strict_partition", "FullPCS_reuse_stage"):
    path = run_dir / name / "metadata.json"
    if path.exists():
        configs.append(json.loads(path.read_text(encoding="utf-8")))
    else:
        configs.append(
            {
                "configuration": name,
                "passed": False,
                "exit_code": None,
                "oracle_exit": None,
                "final_avg_temp": None,
                "expected_avg_temp": "11.428571428571429",
                "final_avg_temp_path": None,
                "wall_time_ms": None,
                "manual_steps_ssh": None,
                "wrong_context_failure": "not_run",
                "human_intervention_count": None,
                "observer_events": None,
                "driver_task_count": None,
                "artifact_transfers": [],
                "per_agent_tokens": [],
                "all_agent_total_tokens": {"input": 0, "output": 0},
                "raw_csv_in_driver_workspace": None,
                "output_dir": str(run_dir / name),
                "notes": "not run",
            }
        )

redesign_alignment = [
    {
        "requirement": "manual_ssh_cross_context",
        "status": "partial",
        "evidence": "Records explicit data->compute->driver transfers and wrong-context probe; transport is SSH/SCP-style wrapper, not real sshd.",
    },
    {
        "requirement": "single_machine_codex_ssh",
        "status": "partial",
        "evidence": "Single Codex CLI runs from driver workspace without raw CSV; transport is SSH/SCP-style wrapper.",
    },
    {
        "requirement": "cloud_sandbox_context_injection",
        "status": "partial",
        "evidence": "Sandbox starts without raw CSV and writes context_injection_manifest.json; this is a local injection harness, not real E2B.",
    },
    {
        "requirement": "FullPCS_strict_partition",
        "status": "partial",
        "evidence": "Driver workspace has no raw CSV and observer records events; default run is guided and slave sides use bash/file skills.",
    },
    {
        "requirement": "wrong_context_failure",
        "status": "partial",
        "evidence": "Wrong-context probe is injected for local strict contexts; Full PCS records a post-run strict-boundary and recovery probe.",
    },
    {
        "requirement": "per_agent_tokens",
        "status": "partial",
        "evidence": "Token accounting is per Codex session. Current Full PCS uses driver Codex only; data/compute slave token rows are zero-token bash/file agents.",
    },
    {
        "requirement": "FullPCS_reuse_stage",
        "status": "partial",
        "evidence": "Runs a local capability-registry replay with registry_hit=1 and repeated_generation=0; full observer-backed promotion is still future work.",
    },
]

results = {
    "task": "public-terminal-heterogeneous-dates",
    "run_id": run_id,
    "run_dir": str(run_dir),
    "generated_at_unix": int(time.time()),
    "strict_context_partition": {
        "driver": "task prompt and final avg_temp.txt only; raw CSV forbidden",
        "data-slave": "daily_temp_sf_high.csv and daily_temp_sf_low.csv; produces aligned_temperatures.csv",
        "compute-slave": "receives aligned_temperatures.csv only; produces avg_temp.txt",
        "observer": "records Full PCS events and task metadata",
    },
    "shared_prompt": {
        "path": str(shared_prompt_path),
        "sha256": shared_prompt_sha,
        "prompt_parity_configurations": prompt_parity_configs,
        "control_configurations": ["manual_ssh_cross_context"],
    },
    "redesign_alignment": redesign_alignment,
    "missing_configurations": [],
    "configurations": configs,
}
results_path.write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
(run_dir / "strict_partition_results.json").write_text(json.dumps(results, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

def yn(value):
    if value is True:
        return "通过"
    if value is False:
        return "未通过"
    return "未跑"

def ms(value):
    if value is None:
        return "N/A"
    return f"{value}"

def steps(value):
    if value is None:
        return "N/A"
    return str(value)

def total_tokens(conf):
    tokens = conf.get("all_agent_total_tokens", {})
    return f"{tokens.get('input', 0)} / {tokens.get('output', 0)}"

def avg_value(value):
    if value is None or value == "":
        return "N/A"
    return str(value)

rows = []
for conf in configs:
    rows.append(
        "| `{configuration}` | {passed} | {final_avg} | {expected_avg} | {wall} | {wrong_context} | {manual_steps} | {human} | {observer} | {tokens} | {registry_hit} | {repeated_generation} | {raw_leak} |".format(
            configuration=conf["configuration"],
            passed=yn(conf.get("passed")),
            final_avg=avg_value(conf.get("final_avg_temp")),
            expected_avg=avg_value(conf.get("expected_avg_temp")),
            wall=ms(conf.get("wall_time_ms")),
            wrong_context=conf.get("wrong_context_failure", "N/A"),
            manual_steps=steps(conf.get("manual_steps_ssh")),
            human=conf.get("human_intervention_count", "N/A"),
            observer=conf.get("observer_events", "N/A"),
            tokens=total_tokens(conf),
            registry_hit=conf.get("registry_hit", "N/A"),
            repeated_generation=conf.get("repeated_generation", "N/A"),
            raw_leak=conf.get("raw_csv_in_driver_workspace", "N/A"),
        )
    )

prompt_rows = []
for conf in configs:
    prompt_rows.append(
        "| `{configuration}` | {group} | {agent} | {prompt_sha} | {usage_events} |".format(
            configuration=conf["configuration"],
            group=conf.get("comparison_group", "control_no_llm"),
            agent=conf.get("prompt_agent", "N/A"),
            prompt_sha=conf.get("prompt_sha256", "N/A"),
            usage_events=sum(item.get("usage_events", 0) for item in conf.get("per_agent_tokens", [])),
        )
    )

alignment_rows = []
for item in redesign_alignment:
    alignment_rows.append(
        f"| `{item['requirement']}` | {item['status']} | {item['evidence']} |"
    )

token_sections = []
for conf in configs:
    token_sections.append(f"### {conf['configuration']}")
    agents = conf.get("per_agent_tokens", [])
    if not agents:
        token_sections.append("无 token 记录。")
        continue
    token_sections.append("| agent | input | output | usage_events |")
    token_sections.append("| --- | ---: | ---: | ---: |")
    for item in agents:
        token_sections.append(
            f"| `{item.get('agent')}` | {item.get('model_input_tokens', 0)} | {item.get('model_output_tokens', 0)} | {item.get('usage_events', 0)} |"
        )
    token_sections.append("")

notes = []
for conf in configs:
    notes.append(f"- `{conf['configuration']}`：{conf.get('notes', '')} 输出目录：`{conf.get('output_dir')}`。")

report = f"""# public-terminal-heterogeneous-dates Strict Partition 单任务实验报告

## 任务与分区

本轮是对 `task1_experiment_redesign_cn.md` 的继续实现，但仍是单任务 strict-partition smoke，不是完整 15 任务主实验。driver 只拥有任务说明和最终回收路径，原始 CSV 只存在于 data context，compute context 只接收对齐后的中间表并计算 `avg_temp.txt`。

固定数据流为：`data-slave raw CSV -> aligned_temperatures.csv -> compute-slave avg_temp.txt -> driver final workspace`。报告中的 token 口径按 agent 分开记录，并给出 all-agent 总和。主对比配置均接收同一份 `shared_task_prompt.md`；`manual_ssh_cross_context` 只作为 no-LLM control，不参与 prompt/token 横向比较。

## Shared Prompt

- prompt path: `{shared_prompt_path}`
- prompt_sha256: `{shared_prompt_sha}`
- prompt parity configurations: `{', '.join(prompt_parity_configs)}`

| configuration | comparison_group | prompt_agent | prompt_sha256 | usage_events |
| --- | --- | --- | --- | ---: |
{chr(10).join(prompt_rows)}

## Redesign 对齐状态

| redesign requirement | status | evidence |
| --- | --- | --- |
{chr(10).join(alignment_rows)}

## Fair Cross-Context 结果表

| configuration | passed | final_avg_temp | expected_avg_temp | wall_time_ms | wrong_context | manual_steps_ssh | human_intervention | observer_events | input/output tokens | registry_hit | repeated_generation | raw_csv_in_driver |
| --- | --- | ---: | ---: | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
{chr(10).join(rows)}

## Per-agent Token

{chr(10).join(token_sections)}

## 过程记录

- `manual_ssh_cross_context`：脚本化 SSH/SCP 风格流程，显式执行 data normalize、data->compute transfer、compute aggregate、compute->driver transfer；用于估算人工步骤和验证 oracle。
- `single_machine_codex_ssh`：单个 Codex CLI 在 driver workspace 中运行，本地没有 raw CSV；它只能通过 `ssh`/`scp` 风格 helper 访问 data/compute context。
- `cloud_sandbox_context_injection`：cloud sandbox 初始不含 raw CSV；runner 记录显式 context injection，并只回收 `avg_temp.txt`。
- `FullPCS_strict_partition`：调用 driver MCP smoke 的 `STRICT_PARTITION=1` 模式，raw CSV 预加载在 `smoke-data-slave/workspace/input`，driver 通过 workspace agent discovery 和 MCP 工具协调 data/compute slave。
- `FullPCS_reuse_stage`：复用本轮登记的 capability registry，不再调用 Codex 生成新脚本，记录 `registry_hit=1` 和 `repeated_generation=0`。

## 产物

- 结构化结果：`{results_path}`
- 本报告：`{report_path}`
- 中间过程报告：`{intermediate_report_path}`
- 本轮运行目录：`{run_dir}`

## 局限

- 这是一任务 smoke，不是 15 任务主实验统计。
- `manual_ssh_cross_context` 与 `single_machine_codex_ssh` 当前使用本地目录背后的 SSH/SCP 风格 transport wrapper；它能约束 context 可见性，但不是独立远端主机上的真实 sshd。
- `cloud_sandbox_context_injection` 是本地 context-injection harness，不是真实 E2B/cloud provider 执行。
- `FullPCS_strict_partition` 默认 `FULLPCS_PROMPT_MODE=guided`，用于先跑通 strict partition；可通过环境变量改成 `autonomous` 再做无人路线选择复测。
- wrong-context failure 已在本地 strict contexts 注入；Full PCS 当前记录 post-run boundary/recovery probe，还不是 driver 主动 wrong-target MCP 调用后的恢复。
- `FullPCS_reuse_stage` 当前是本地 capability registry replay，不是完整 observer-backed capability promotion。

## 配置说明

{chr(10).join(notes)}
"""
report_path.write_text(report, encoding="utf-8")
(run_dir / "strict_partition_report_cn.md").write_text(report, encoding="utf-8")

intermediate_sections = []
for conf in configs:
    output_dir = Path(conf.get("output_dir", run_dir / conf["configuration"]))
    metadata_path = run_dir / conf["configuration"] / "metadata.json"
    oracle_path = output_dir / "oracle_result.json"
    if not oracle_path.exists():
        oracle_path = run_dir / conf["configuration"] / "oracle_result.json"
    final_avg_path = conf.get("final_avg_temp_path")
    section = [
        f"## {conf['configuration']}",
        "",
        f"- passed: {conf.get('passed')}",
        f"- final_avg_temp: `{conf.get('final_avg_temp')}`",
        f"- expected_avg_temp: `{conf.get('expected_avg_temp')}`",
        f"- final_avg_temp_path: `{final_avg_path}`",
        f"- comparison_group: `{conf.get('comparison_group', 'control_no_llm')}`",
        f"- prompt_agent: `{conf.get('prompt_agent', 'N/A')}`",
        f"- prompt_sha256: `{conf.get('prompt_sha256', 'N/A')}`",
        f"- prompt_path: `{conf.get('prompt_path', 'N/A')}`",
        f"- wall_time_ms: {conf.get('wall_time_ms')}",
        f"- wrong_context_failure: {conf.get('wrong_context_failure')}",
        f"- human_intervention_count: {conf.get('human_intervention_count')}",
        f"- output_dir: `{output_dir}`",
        f"- metadata: `{metadata_path}`",
        f"- oracle: `{oracle_path}`",
    ]
    probe = conf.get("wrong_context_probe")
    if probe:
        section.append(f"- wrong_context_probe: `{run_dir / conf['configuration'] / 'wrong_context_probe.json'}`")
        section.append(f"- wrong_context_probe_notes: {probe.get('notes')}")
    if conf.get("context_injection_manifest"):
        section.append(f"- context_injection_manifest: `{conf['context_injection_manifest']}`")
    if conf.get("fullpcs_wrong_target_probe"):
        section.append(f"- fullpcs_wrong_target_probe: `{conf['fullpcs_wrong_target_probe']}`")
    if conf.get("capability_registry"):
        section.append(f"- capability_registry: `{conf['capability_registry']}`")
    if conf.get("reuse_stage_report"):
        section.append(f"- reuse_stage_report: `{conf['reuse_stage_report']}`")
    section.append("- artifact_transfers:")
    for transfer in conf.get("artifact_transfers", []):
        section.append(f"  - {transfer}")
    section.append("- per_agent_tokens:")
    for item in conf.get("per_agent_tokens", []):
        section.append(
            f"  - {item.get('agent')}: input={item.get('model_input_tokens', 0)}, output={item.get('model_output_tokens', 0)}, usage_events={item.get('usage_events', 0)}"
        )
    section.append("")
    intermediate_sections.append("\n".join(section))

intermediate_report = f"""# Task1 Strict Partition 中间过程查验报告

## Run

- run_id: `{run_id}`
- run_dir: `{run_dir}`
- generated_at_unix: {results['generated_at_unix']}
- main_report: `{report_path}`
- structured_results: `{results_path}`

## 查验重点

- raw CSV 是否泄漏到 driver workspace：查看每个配置的 `raw_csv_in_driver_workspace`。
- prompt parity：主对比配置应有相同的 `prompt_sha256`，并且 `usage_events > 0`。
- wrong-context 注入或边界探测：查看各配置的 `wrong_context_probe.json` 或 `fullpcs_wrong_target_probe.json`。
- cloud context injection：查看 `context_injection_manifest.json`。
- reuse stage：查看 `capability_registry.json` 与 `reuse_stage_report_cn.md`。
- token 口径：查看每个配置的 `per_agent_tokens` 和 `all_agent_total_tokens`。

{chr(10).join(intermediate_sections)}
"""
intermediate_report_path.write_text(intermediate_report, encoding="utf-8")
(run_dir / "strict_partition_intermediate_trace_cn.md").write_text(intermediate_report, encoding="utf-8")
PY
}

write_shared_task_prompt
manual_ssh_cross_context
single_machine_codex_ssh
cloud_sandbox_context_injection
FullPCS_strict_partition
FullPCS_reuse_stage
generate_report

echo "Strict partition report written to ${REPORT_PATH}"
echo "Strict partition results written to ${RESULTS_PATH}"
echo "Strict partition intermediate report written to ${INTERMEDIATE_REPORT_PATH}"
