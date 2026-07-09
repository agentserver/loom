#!/usr/bin/env bash
set -euo pipefail

OUT_ROOT="${1:-/root/paper_writing/paper_outputs/multi_agent_driver_mcp_smoke}"
RUN_ID="${RUN_ID:-$(date +%Y%m%d-%H%M%S)-$$}"
OUT_DIR="${OUT_ROOT}/${RUN_ID}"
IMAGE_TAG="${IMAGE_TAG:-multi-agent-e2e-runtime:latest}"
SOURCE_CODEX_CONFIG="${CODEX_CONFIG_PATH:-${CODEX_HOME:-/root/.codex}/config.toml}"
CODEX_NODE_MODULE="${CODEX_NODE_MODULE:-/usr/lib/node_modules/@openai/codex}"
DRIVER_TIMEOUT_SEC="${DRIVER_TIMEOUT_SEC:-1200}"
TELEMETRY_KEY="${TELEMETRY_KEY:-ops-smoke-secret}"
DRIVER_PROMPT_MODE="${DRIVER_PROMPT_MODE:-guided}"

case "$DRIVER_PROMPT_MODE" in
  guided)
    REPORT_FILENAME="multi_agent_driver_mcp_report_cn.md"
    ;;
  autonomous)
    REPORT_FILENAME="multi_agent_driver_autonomous_report_cn.md"
    ;;
  *)
    echo "Unsupported DRIVER_PROMPT_MODE=${DRIVER_PROMPT_MODE}; expected guided or autonomous" >&2
    exit 2
    ;;
esac

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
WORKLOAD_DIR="${ROOT_DIR}/tests/eval/workloads/public-terminal-heterogeneous-dates"

BIN_DIR="${OUT_DIR}/bin"
CONFIG_DIR="${OUT_DIR}/configs"
CRED_DIR="${OUT_DIR}/creds"
LOG_DIR="${OUT_DIR}/logs"
PROMPT_DIR="${OUT_DIR}/prompts"
INPUT_DIR="${OUT_DIR}/driver/workspace/input"
DRIVER_WORKSPACE="${OUT_DIR}/driver/workspace"
FINAL_WORKSPACE="${OUT_DIR}/final_workspace"
OBSERVER_DB="${OUT_DIR}/observer/observer.db"

WORKSPACE_ID="ws-driver-mcp-smoke-${RUN_ID//[^A-Za-z0-9_-]/-}"
STUB_PORT=""
OBSERVER_PORT=""
STUB_PID=""
OBSERVER_PID=""
CONTAINERS=()

cleanup() {
  local code=$?
  for c in "${CONTAINERS[@]:-}"; do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  if [[ -n "${OBSERVER_PID:-}" ]]; then
    kill "$OBSERVER_PID" >/dev/null 2>&1 || true
    wait "$OBSERVER_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "${STUB_PID:-}" ]]; then
    kill "$STUB_PID" >/dev/null 2>&1 || true
    wait "$STUB_PID" >/dev/null 2>&1 || true
  fi
  exit "$code"
}
trap cleanup EXIT

mkdir -p \
  "$BIN_DIR" \
  "$CONFIG_DIR" \
  "$CRED_DIR" \
  "$LOG_DIR" \
  "$PROMPT_DIR" \
  "$INPUT_DIR" \
  "$FINAL_WORKSPACE" \
  "${OUT_DIR}/agentserver" \
  "${OUT_DIR}/observer" \
  "${OUT_DIR}/driver/workspace/.codex" \
  "${OUT_DIR}/driver/codex-home" \
  "${OUT_DIR}/driver/audit" \
  "${OUT_DIR}/driver/runtime" \
  "${OUT_DIR}/smoke-data-slave/workspace" \
  "${OUT_DIR}/smoke-data-slave/codex-home" \
  "${OUT_DIR}/smoke-data-slave/runtime" \
  "${OUT_DIR}/smoke-compute-slave/workspace" \
  "${OUT_DIR}/smoke-compute-slave/codex-home" \
  "${OUT_DIR}/smoke-compute-slave/runtime"

if [[ ! -r "$SOURCE_CODEX_CONFIG" ]]; then
  echo "Codex config is not readable: ${SOURCE_CODEX_CONFIG}" >&2
  exit 2
fi
if [[ ! -d "$CODEX_NODE_MODULE" ]]; then
  echo "Codex node module is not available: ${CODEX_NODE_MODULE}" >&2
  exit 2
fi
if ! docker image inspect "$IMAGE_TAG" >/dev/null 2>&1; then
  echo "Docker image is not available: ${IMAGE_TAG}" >&2
  exit 2
fi

find_free_port() {
  python3 - <<'PY'
import socket

s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

json_get() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

path, key = sys.argv[1:3]
with open(path, "r", encoding="utf-8") as handle:
    print(json.load(handle)[key])
PY
}

wait_for_url() {
  local url="$1"
  local name="$2"
  for _ in $(seq 1 150); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "Timed out waiting for ${name}: ${url}" >&2
  return 1
}

wait_for_slaves() {
  local token="$1"
  local server_url="$2"
  for _ in $(seq 1 180); do
    if curl -fsS -H "Authorization: Bearer ${token}" \
      "${server_url}/api/agent/discovery/agents" >"${LOG_DIR}/discovery.latest.json" 2>/dev/null; then
      if python3 - "${LOG_DIR}/discovery.latest.json" <<'PY'
import json
import sys

cards = json.load(open(sys.argv[1], "r", encoding="utf-8"))
names = {card.get("display_name") for card in cards}
raise SystemExit(0 if {"smoke-data-slave", "smoke-compute-slave"} <= names else 1)
PY
      then
        return 0
      fi
    fi
    sleep 0.5
  done
  echo "Timed out waiting for smoke slave discovery" >&2
  return 1
}

write_minimal_codex_config() {
  local src="$1"
  local dst="$2"
  shift 2
  python3 - "$src" "$dst" "$@" <<'PY'
import json
import sys
import tomllib
from pathlib import Path

src, dst, *trusted_dirs = sys.argv[1:]
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
for project in trusted_dirs:
    lines.append(f"[projects.{toml_string(project)}]")
    lines.append('trust_level = "trusted"')
    lines.append("")
Path(dst).write_text("\n".join(lines).rstrip() + "\n", encoding="utf-8")
PY
  chmod 600 "$dst"
}

echo "Building local smoke binaries..."
go build -o "${BIN_DIR}/agentserver-stub" ./tools/eval/agentserver-stub \
  >"${LOG_DIR}/go-build-agentserver-stub.stdout.log" \
  2>"${LOG_DIR}/go-build-agentserver-stub.stderr.log"
go build -o "${BIN_DIR}/observer-server" ./cmd/observer-server \
  >"${LOG_DIR}/go-build-observer-server.stdout.log" \
  2>"${LOG_DIR}/go-build-observer-server.stderr.log"
go build -o "${BIN_DIR}/driver-agent" ./cmd/driver-agent \
  >"${LOG_DIR}/go-build-driver-agent.stdout.log" \
  2>"${LOG_DIR}/go-build-driver-agent.stderr.log"
go build -o "${BIN_DIR}/slave-agent" ./cmd/slave-agent \
  >"${LOG_DIR}/go-build-slave-agent.stdout.log" \
  2>"${LOG_DIR}/go-build-slave-agent.stderr.log"

cp "${WORKLOAD_DIR}/fixtures/task-deps/daily_temp_sf_high.csv" "${INPUT_DIR}/daily_temp_sf_high.csv"
cp "${WORKLOAD_DIR}/fixtures/task-deps/daily_temp_sf_low.csv" "${INPUT_DIR}/daily_temp_sf_low.csv"
cat >"${INPUT_DIR}/task.md" <<'TASK'
Public benchmark task: Terminal-Bench heterogeneous-dates adaptation.

Use daily_temp_sf_high.csv and daily_temp_sf_low.csv to calculate the average
daily high-minus-low temperature difference. The final answer must be written
to avg_temp.txt as only a numeric value.
TASK

STUB_PORT="$(find_free_port)"
OBSERVER_PORT="$(find_free_port)"
SERVER_URL="http://127.0.0.1:${STUB_PORT}"
OBSERVER_URL="http://127.0.0.1:${OBSERVER_PORT}"

"${BIN_DIR}/agentserver-stub" --listen "127.0.0.1:${STUB_PORT}" --workspace-id "$WORKSPACE_ID" \
  >"${LOG_DIR}/agentserver-stub.stdout.log" \
  2>"${LOG_DIR}/agentserver-stub.stderr.log" &
STUB_PID="$!"
wait_for_url "${SERVER_URL}/healthz" "agentserver-stub"

"${BIN_DIR}/agentserver-stub" issue --server "$SERVER_URL" --role driver --short-id smoke-driver --workspace-id "$WORKSPACE_ID" >"${CRED_DIR}/driver.json"
"${BIN_DIR}/agentserver-stub" issue --server "$SERVER_URL" --role slave --short-id smoke-data-slave --workspace-id "$WORKSPACE_ID" >"${CRED_DIR}/smoke-data-slave.json"
"${BIN_DIR}/agentserver-stub" issue --server "$SERVER_URL" --role slave --short-id smoke-compute-slave --workspace-id "$WORKSPACE_ID" >"${CRED_DIR}/smoke-compute-slave.json"

cat >"${CONFIG_DIR}/observer.yaml" <<YAML
listen_addr: "127.0.0.1:${OBSERVER_PORT}"
db_path: "${OBSERVER_DB}"
identity:
  legacy_api_keys:
    enabled: false
  agentserver:
    enabled: true
    url: "${SERVER_URL}"
    fresh_ttl: 180s
    stale_grace: 15m
    request_timeout: 2s
    cache_capacity: 65536
    startup_probe: true
store:
  driver: sqlite
  sqlite:
    path: "${OBSERVER_DB}"
  allow_sqlite_in_production: false
object_store:
  driver: filesystem
  proxy:
    enabled: false
telemetry:
  enabled: true
  api_keys:
    - id: ops-smoke
      key_env: LOOM_SMOKE_TELEMETRY_KEY
      workspace_id: "${WORKSPACE_ID}"
      note: "driver MCP smoke"
  rate_limit:
    per_minute: 1000
    burst: 1000
  max_body_bytes: 262144
  retention_days: 7
YAML

LOOM_SMOKE_TELEMETRY_KEY="$TELEMETRY_KEY" "${BIN_DIR}/observer-server" --config "${CONFIG_DIR}/observer.yaml" \
  >"${LOG_DIR}/observer-server.stdout.log" \
  2>"${LOG_DIR}/observer-server.stderr.log" &
OBSERVER_PID="$!"
wait_for_url "${OBSERVER_URL}/healthz" "observer-server"

write_agent_config() {
  local role="$1"
  local display_name="$2"
  local description="$3"
  local workdir="$4"
  local codex_home="$5"
  local audit_dir="$6"
  local config_path="$7"
  local cred_path="${CRED_DIR}/${role}.json"

  python3 - "$cred_path" "$config_path" "$SERVER_URL" "$OBSERVER_URL" "$WORKSPACE_ID" "$TELEMETRY_KEY" "$display_name" "$description" "$workdir" "$codex_home" "$audit_dir" <<'PY'
import json
import sys
from pathlib import Path

(
    cred_path,
    config_path,
    server_url,
    observer_url,
    workspace_id,
    telemetry_key,
    display_name,
    description,
    workdir,
    codex_home,
    audit_dir,
) = sys.argv[1:12]
creds = json.loads(Path(cred_path).read_text(encoding="utf-8"))

is_driver = display_name == "smoke-driver"
skills = ["driver"] if is_driver else ["chat", "bash", "file"]
lines = [
    "server:",
    f"  url: {json.dumps(server_url)}",
    f"  name: {json.dumps(display_name)}",
    "credentials:",
    f"  sandbox_id: {json.dumps(creds['sandbox_id'])}",
    f"  tunnel_token: {json.dumps(creds['tunnel_token'])}",
    f"  proxy_token: {json.dumps(creds['proxy_token'])}",
    f"  workspace_id: {json.dumps(creds['workspace_id'])}",
    f"  short_id: {json.dumps(creds['short_id'])}",
    "agent:",
    "  kind: codex",
    "  bin: codex",
    f"  workdir: {json.dumps(workdir)}",
    "  extra_args: []",
    f"  codex_home: {json.dumps(codex_home)}",
    "discovery:",
    f"  display_name: {json.dumps(display_name)}",
    f"  description: {json.dumps(description)}",
    "  skills:",
]
for skill in skills:
    lines.append(f"    - {skill}")
if is_driver:
    lines.extend([
        "driver_defaults:",
        "  task_timeout_sec: 300",
        f"  audit_log_dir: {json.dumps(audit_dir)}",
        "  disable_uid_check: true",
        "  artifact_transport: peer_proxy",
        f"  workdir: {json.dumps(workdir)}",
        "  source_path_read_roots:",
        f"    - {json.dumps(workdir)}",
    ])
else:
    lines.extend([
        "daemon:",
        "  auto_start: false",
    ])
lines.extend([
    "observer:",
    "  enabled: true",
    "  telemetry_enabled: true",
    f"  telemetry_api_key: {json.dumps(telemetry_key)}",
    f"  url: {json.dumps(observer_url)}",
    f"  workspace_id: {json.dumps(workspace_id)}",
    f"  workspace_name: {json.dumps(workspace_id)}",
    f"  agent_id: {json.dumps(creds['short_id'])}",
    "  force_register: true",
])
if is_driver:
    lines.append(f"  promotion_audit_db_path: {json.dumps('/e2e/observer/observer.db')}")
Path(config_path).write_text("\n".join(lines) + "\n", encoding="utf-8")
PY
}

write_agent_config \
  "driver" \
  "smoke-driver" \
  "Codex CLI driver with driver-agent MCP tools for the public benchmark smoke." \
  "/e2e/driver/workspace" \
  "/e2e/driver/codex-home" \
  "/e2e/driver/audit" \
  "${OUT_DIR}/driver/config.yaml"
write_agent_config \
  "smoke-data-slave" \
  "smoke-data-slave" \
  "Codex-kind slave-agent container for CSV date normalization and artifact production." \
  "/e2e/smoke-data-slave/workspace" \
  "/e2e/smoke-data-slave/codex-home" \
  "" \
  "${OUT_DIR}/smoke-data-slave/config.yaml"
write_agent_config \
  "smoke-compute-slave" \
  "smoke-compute-slave" \
  "Codex-kind slave-agent container for numeric aggregation and final artifact production." \
  "/e2e/smoke-compute-slave/workspace" \
  "/e2e/smoke-compute-slave/codex-home" \
  "" \
  "${OUT_DIR}/smoke-compute-slave/config.yaml"

write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${OUT_DIR}/driver/codex-home/config.toml" "/e2e/driver/workspace"
write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${OUT_DIR}/smoke-data-slave/codex-home/config.toml" "/e2e/smoke-data-slave/workspace"
write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${OUT_DIR}/smoke-compute-slave/codex-home/config.toml" "/e2e/smoke-compute-slave/workspace"

cat >"${OUT_DIR}/driver/workspace/.codex/config.toml" <<'TOML'
# driver-agent serve-mcp is launched by Codex through this project MCP config.
[mcp_servers.driver]
command = "/e2e/bin/driver-agent"
args = ["serve-mcp", "--config", "/e2e/driver/config.yaml"]
startup_timeout_sec = 30
tool_timeout_sec = 300
enabled = true
TOML

case "$DRIVER_PROMPT_MODE" in
guided)
cat >"${PROMPT_DIR}/driver_prompt.md" <<'PROMPT'
You are the driver Codex for a multi-agent benchmark smoke.

Task:
- Solve the public Terminal-Bench heterogeneous-dates adaptation.
- Inputs are in /e2e/driver/workspace/input:
  - task.md
  - daily_temp_sf_high.csv
  - daily_temp_sf_low.csv
- avg_temp.txt must contain only a numeric value and no explanatory text.

Required architecture:
- Use the driver MCP tools. Do not compute the benchmark locally in the driver.
- First call list_agents and verify smoke-data-slave and smoke-compute-slave are available.
- Use write_slave_file to copy both raw CSV inputs from the driver workspace into smoke-data-slave.
- Use run_slave_bash on smoke-data-slave to normalize the two date formats, align records by date, and write:
  - aligned_temperatures.csv with columns date,high_temperature,low_temperature,difference
  - data_profile.md with a concise description of row counts, date formats, and alignment.
- Use read_slave_file to retrieve the aligned CSV and profile from smoke-data-slave.
- Use write_slave_file to send the aligned CSV to smoke-compute-slave.
- Use run_slave_bash on smoke-compute-slave to calculate the arithmetic mean of the difference column and write:
  - avg_temp.txt containing only the numeric value
  - solution_report.md explaining the calculation briefly.
- Use read_slave_file to retrieve avg_temp.txt from smoke-compute-slave.
- After reading the compute result, you may write that exact numeric content to /e2e/driver/workspace/avg_temp.txt so the harness can run the public oracle. Do not modify the number locally.

Operational constraints:
- Do not hard-code an expected final answer.
- This smoke uses only driver MCP plus the two named slave-agent containers.
- Keep the final response short and include which MCP tools you used and where avg_temp.txt was written.
PROMPT
  ;;
autonomous)
cat >"${PROMPT_DIR}/driver_prompt.md" <<'PROMPT'
You are the driver Codex for a fully autonomous multi-agent benchmark run.

Single task:
- Solve the public Terminal-Bench heterogeneous-dates adaptation.
- Initial inputs are in /e2e/driver/workspace/input:
  - task.md
  - daily_temp_sf_high.csv
  - daily_temp_sf_low.csv
- The final artifact must be /e2e/driver/workspace/avg_temp.txt.
- avg_temp.txt must contain only the numeric answer and no explanatory text.

Autonomy rules:
- Use the available driver MCP surface to inspect the workspace and coordinate available agents.
- Decide which available agents, tools, and slave skills to use. No caller has preselected the route for you.
- Do not compute the benchmark entirely inside the driver. The driver should coordinate and verify work performed through workspace agents.
- Do not ask the user, request approval, or pause for human input. If you cannot finish autonomously, fail with a short reason.
- Do not hard-code an expected final answer. Derive the result from the visible CSV inputs and any artifacts you create.
- You may create intermediate files/artifacts wherever the selected agent interfaces allow, but keep the final numeric file at the path above.

Final response:
- Briefly state the route you selected, the agents and slave skills you used, and the final output path.
PROMPT
  ;;
esac

start_slave() {
  local name="$1"
  local container="ma-${name}-${RUN_ID}"
  CONTAINERS+=("$container")
  docker run -d \
    --name "$container" \
    --network host \
    --workdir "/e2e/${name}/runtime" \
    -e OPENAI_API_KEY \
    -e HTTP_PROXY \
    -e HTTPS_PROXY \
    -e NO_PROXY \
    -e HOME="/tmp/${name}" \
    -v "${OUT_DIR}:/e2e" \
    -v "${CODEX_NODE_MODULE}:/usr/lib/node_modules/@openai/codex:ro" \
    "$IMAGE_TAG" \
    bash -lc "/e2e/bin/slave-agent /e2e/${name}/config.yaml" \
    >"${LOG_DIR}/${name}.container-id" \
    2>"${LOG_DIR}/${name}.docker-run.stderr.log"
}

start_slave "smoke-data-slave"
start_slave "smoke-compute-slave"

DRIVER_PROXY_TOKEN="$(json_get "${CRED_DIR}/driver.json" proxy_token)"
wait_for_slaves "$DRIVER_PROXY_TOKEN" "$SERVER_URL"

DRIVER_CONTAINER="ma-smoke-driver-${RUN_ID}"
CONTAINERS+=("$DRIVER_CONTAINER")
driver_start_ns="$(date +%s%N)"
driver_exit=0
set +e
timeout "${DRIVER_TIMEOUT_SEC}s" docker run --rm \
  --name "$DRIVER_CONTAINER" \
  --network host \
  --workdir "/e2e/driver/workspace" \
  -e OPENAI_API_KEY \
  -e HTTP_PROXY \
  -e HTTPS_PROXY \
  -e NO_PROXY \
  -e CODEX_HOME="/e2e/driver/codex-home" \
  -e HOME="/tmp/smoke-driver" \
  -v "${OUT_DIR}:/e2e" \
  -v "${CODEX_NODE_MODULE}:/usr/lib/node_modules/@openai/codex:ro" \
  "$IMAGE_TAG" \
  bash -lc 'codex() { node /usr/lib/node_modules/@openai/codex/bin/codex.js "$@"; }; codex exec --json --output-last-message /e2e/driver/last_message.md --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -c '\''mcp_servers.driver.command="/e2e/bin/driver-agent"'\'' -c '\''mcp_servers.driver.args=["serve-mcp","--config","/e2e/driver/config.yaml"]'\'' -c '\''mcp_servers.driver.startup_timeout_sec=30'\'' -c '\''mcp_servers.driver.tool_timeout_sec=300'\'' - < /e2e/prompts/driver_prompt.md' \
  >"${LOG_DIR}/driver.codex.jsonl" \
  2>"${LOG_DIR}/driver.stderr.log"
driver_exit="$?"
set -e
driver_end_ns="$(date +%s%N)"
CONTAINERS=("${CONTAINERS[@]/$DRIVER_CONTAINER}")

if [[ -f "${DRIVER_WORKSPACE}/avg_temp.txt" ]]; then
  cp "${DRIVER_WORKSPACE}/avg_temp.txt" "${FINAL_WORKSPACE}/avg_temp.txt"
fi

set +e
"${WORKLOAD_DIR}/oracle.sh" "$FINAL_WORKSPACE" >"${OUT_DIR}/oracle_result.json" 2>"${LOG_DIR}/driver-oracle.stderr.log"
oracle_exit="$?"
set -e

python3 - "$OUT_DIR" "$OUT_ROOT" "$RUN_ID" "$IMAGE_TAG" "$WORKSPACE_ID" "$SERVER_URL" "$OBSERVER_URL" "$driver_exit" "$driver_start_ns" "$driver_end_ns" "$oracle_exit" "$DRIVER_PROMPT_MODE" "$REPORT_FILENAME" <<'PY'
import hashlib
import json
import os
import sqlite3
import sys
import time
from pathlib import Path

(
    out_dir_s,
    out_root_s,
    run_id,
    image_tag,
    workspace_id,
    server_url,
    observer_url,
    driver_exit_s,
    driver_start_ns_s,
    driver_end_ns_s,
    oracle_exit_s,
    prompt_mode,
    report_filename,
) = sys.argv[1:14]
out_dir = Path(out_dir_s)
out_root = Path(out_root_s)
driver_exit = int(driver_exit_s)
driver_start_ns = int(driver_start_ns_s)
driver_end_ns = int(driver_end_ns_s)
oracle_exit = int(oracle_exit_s)

def load_json(path: Path, default):
    if not path.exists():
        return default
    text = path.read_text(encoding="utf-8", errors="replace").strip()
    if not text:
        return default
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return default

def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()

def walk_usage(obj):
    if isinstance(obj, dict):
        usage = obj.get("usage")
        if isinstance(usage, dict):
            yield usage
        for value in obj.values():
            if isinstance(value, (dict, list)):
                yield from walk_usage(value)
    elif isinstance(obj, list):
        for value in obj:
            yield from walk_usage(value)

def token_int(usage, *keys):
    for key in keys:
        value = usage.get(key)
        if isinstance(value, int):
            return value
        if isinstance(value, float) and value.is_integer():
            return int(value)
    return 0

def collect_codex_usage(path: Path):
    input_tokens = 0
    output_tokens = 0
    usage_events = 0
    tool_mentions = {}
    human_intervention_count = 0
    if path.exists():
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            text = json.dumps(obj, ensure_ascii=False)
            for name in ("list_agents", "write_slave_file", "run_slave_bash", "read_slave_file", "wait_task"):
                if name in text:
                    tool_mentions[name] = tool_mentions.get(name, 0) + 1
            item = obj.get("item") if isinstance(obj, dict) else None
            if isinstance(item, dict):
                if item.get("type") in {"ask_user", "request_user_input", "human_input"}:
                    human_intervention_count += 1
                if item.get("type") == "mcp_tool_call" and str(item.get("tool", "")) in {"ask_user", "request_user_input"}:
                    human_intervention_count += 1
            if isinstance(obj, dict) and obj.get("status") == "awaiting_user":
                human_intervention_count += 1
            for usage in walk_usage(obj):
                usage_events += 1
                input_tokens += token_int(usage, "input_tokens", "prompt_tokens")
                output_tokens += token_int(usage, "output_tokens", "completion_tokens")
    return {
        "model_input_tokens": input_tokens,
        "model_output_tokens": output_tokens,
        "usage_events": usage_events,
        "human_intervention_count": human_intervention_count,
        "tool_mentions": tool_mentions,
        "jsonl": "logs/driver.codex.jsonl",
    }

driver_usage = collect_codex_usage(out_dir / "logs" / "driver.codex.jsonl")
usage_summary = {
    "agents": [
        {
            "agent": "smoke-driver-codex",
            **driver_usage,
        }
    ],
    "total_model_input_tokens": driver_usage["model_input_tokens"],
    "total_model_output_tokens": driver_usage["model_output_tokens"],
    "human_intervention_count": driver_usage["human_intervention_count"],
}
(out_dir / "agent_usage_summary.json").write_text(
    json.dumps(usage_summary, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

journal_path = out_dir / "driver" / "audit" / "driver-tasks.jsonl"
journal_records = []
if journal_path.exists():
    for line in journal_path.read_text(encoding="utf-8", errors="replace").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            journal_records.append(json.loads(line))
        except json.JSONDecodeError:
            pass
tool_counts = {}
target_counts = {}
skill_counts = {}
for rec in journal_records:
    tool = rec.get("tool", "")
    target = rec.get("target_display_name", "")
    skill = rec.get("skill", "")
    if tool:
        tool_counts[tool] = tool_counts.get(tool, 0) + 1
    if target:
        target_counts[target] = target_counts.get(target, 0) + 1
    if skill:
        skill_counts[skill] = skill_counts.get(skill, 0) + 1
driver_mcp_summary = {
    "journal": os.path.relpath(journal_path, out_dir),
    "task_records": len(journal_records),
    "tool_counts": tool_counts,
    "skill_counts": skill_counts,
    "target_counts": target_counts,
    "codex_log_tool_mentions": driver_usage["tool_mentions"],
    "required_tools_present": {
        "write_slave_file": tool_counts.get("write_slave_file", 0) >= 3,
        "run_slave_bash": tool_counts.get("run_slave_bash", 0) >= 2,
        "read_slave_file": tool_counts.get("read_slave_file", 0) >= 2,
    },
    "required_targets_present": {
        "smoke-data-slave": target_counts.get("smoke-data-slave", 0) > 0,
        "smoke-compute-slave": target_counts.get("smoke-compute-slave", 0) > 0,
    },
}
(out_dir / "driver_mcp_tool_summary.json").write_text(
    json.dumps(driver_mcp_summary, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

db_path = out_dir / "observer" / "observer.db"
observer_summary = {
    "db_path": os.path.relpath(db_path, out_dir),
    "exists": db_path.exists(),
    "tables": {},
    "event_type_counts": {},
    "task_status_counts": {},
    "agent_role_counts": {},
}
if db_path.exists():
    conn = sqlite3.connect(str(db_path))
    cur = conn.cursor()
    for table in ("workspaces", "agents", "events", "tasks", "subtasks", "writes", "artifacts", "capability_snapshot_usages"):
        try:
            cur.execute(f"SELECT COUNT(*) FROM {table}")
            observer_summary["tables"][table] = cur.fetchone()[0]
        except sqlite3.Error:
            observer_summary["tables"][table] = None
    try:
        cur.execute("SELECT type, COUNT(*) FROM events GROUP BY type ORDER BY type")
        observer_summary["event_type_counts"] = dict(cur.fetchall())
    except sqlite3.Error:
        pass
    try:
        cur.execute("SELECT status, COUNT(*) FROM tasks GROUP BY status ORDER BY status")
        observer_summary["task_status_counts"] = dict(cur.fetchall())
    except sqlite3.Error:
        pass
    try:
        cur.execute("SELECT role, COUNT(*) FROM agents GROUP BY role ORDER BY role")
        observer_summary["agent_role_counts"] = dict(cur.fetchall())
    except sqlite3.Error:
        pass
    conn.close()
(out_dir / "observer_summary.json").write_text(
    json.dumps(observer_summary, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

container_isolation = {
    "image": image_tag,
    "network": "host",
    "workspace_id": workspace_id,
    "agentserver_url": server_url,
    "observer_url": observer_url,
    "driver": {
        "runtime": "codex exec with driver-agent serve-mcp",
        "containerized": True,
        "codex_home": "driver/codex-home",
        "workspace": "driver/workspace",
        "exit_code": driver_exit,
        "duration_seconds": round((driver_end_ns - driver_start_ns) / 1_000_000_000, 3),
    },
    "slaves": [
        {
            "name": "smoke-data-slave",
            "runtime": "slave-agent",
            "agent_kind": "codex",
            "workspace": "smoke-data-slave/workspace",
            "codex_home": "smoke-data-slave/codex-home",
            "skills": ["chat", "bash", "file"],
        },
        {
            "name": "smoke-compute-slave",
            "runtime": "slave-agent",
            "agent_kind": "codex",
            "workspace": "smoke-compute-slave/workspace",
            "codex_home": "smoke-compute-slave/codex-home",
            "skills": ["chat", "bash", "file"],
        },
    ],
    "state_isolation": "one temporary CODEX_HOME and workspace per driver/slave; only model provider config is copied",
}
(out_dir / "container_isolation.json").write_text(
    json.dumps(container_isolation, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

manifest_candidates = [
    out_dir / "driver" / "workspace" / "input" / "task.md",
    out_dir / "driver" / "workspace" / "input" / "daily_temp_sf_high.csv",
    out_dir / "driver" / "workspace" / "input" / "daily_temp_sf_low.csv",
    out_dir / "driver" / "workspace" / "avg_temp.txt",
    out_dir / "final_workspace" / "avg_temp.txt",
    out_dir / "driver" / "last_message.md",
    out_dir / "logs" / "driver.codex.jsonl",
    out_dir / "logs" / "driver.stderr.log",
    out_dir / "oracle_result.json",
    out_dir / "agent_usage_summary.json",
    out_dir / "observer_summary.json",
    out_dir / "driver_mcp_tool_summary.json",
    out_dir / "driver_decision_summary.json",
    out_dir / "container_isolation.json",
    journal_path,
]
manifest = {"generated_at_unix": int(time.time()), "run_id": run_id, "files": []}
for path in manifest_candidates:
    if path.exists():
        manifest["files"].append(
            {
                "path": os.path.relpath(path, out_dir),
                "size_bytes": path.stat().st_size,
                "sha256": sha256(path),
            }
        )
(out_dir / "artifact_manifest.json").write_text(
    json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

oracle = load_json(out_dir / "oracle_result.json", {"passed": False, "details": {"reason": "oracle result missing"}})
strict_tool_gate = all(driver_mcp_summary["required_tools_present"].values()) and all(driver_mcp_summary["required_targets_present"].values())
autonomy_gate = (
    prompt_mode == "autonomous"
    and driver_usage["human_intervention_count"] == 0
    and len(journal_records) > 0
    and bool(skill_counts)
    and bool(target_counts)
)
tool_gate = strict_tool_gate if prompt_mode == "guided" else autonomy_gate
passed = bool(oracle.get("passed")) and driver_exit == 0 and tool_gate
result_value = ""
avg_path = out_dir / "final_workspace" / "avg_temp.txt"
if avg_path.exists():
    result_value = avg_path.read_text(encoding="utf-8", errors="replace").strip()

driver_decision_summary = {
    "prompt_mode": prompt_mode,
    "human_intervention_count": driver_usage["human_intervention_count"],
    "autonomy_gate": autonomy_gate,
    "strict_tool_gate": strict_tool_gate,
    "driver_task_records": len(journal_records),
    "actual_tool_counts": tool_counts,
    "actual_skill_counts": skill_counts,
    "actual_target_counts": target_counts,
    "driver_selected_skills": sorted(skill_counts),
    "driver_selected_targets": sorted(target_counts),
}
(out_dir / "driver_decision_summary.json").write_text(
    json.dumps(driver_decision_summary, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

usage_lines = []
for item in usage_summary["agents"]:
    usage_lines.append(
        f"| `{item['agent']}` | {item['model_input_tokens']} | {item['model_output_tokens']} | {item['usage_events']} |"
    )

tool_lines = []
for name in sorted(tool_counts):
    tool_lines.append(f"| `{name}` | {tool_counts.get(name, 0)} |")
if not tool_lines:
    tool_lines.append("| 未记录 | 0 |")
target_lines = []
for name in sorted(target_counts):
    target_lines.append(f"| `{name}` | {target_counts.get(name, 0)} |")
if not target_lines:
    target_lines.append("| 未记录 | 0 |")

observer_lines = []
for table, count in sorted(observer_summary["tables"].items()):
    observer_lines.append(f"| `{table}` | {count} |")

if prompt_mode == "autonomous":
    report_title = "Autonomous Driver MCP + Codex 多 Agent 容器实验报告"
    experiment_note = "本轮是 autonomous 完整实验：driver 只收到一个 benchmark 任务和边界条件，未被预先指定具体 slave 技能或工具调用顺序。driver 需要自行发现可用 agent、选择 slave skill、执行中间交接，并在无人介入下写出最终结果。"
    step_3 = "harness 启动两个 slave-agent 容器，二者 `agent.kind=codex` 并暴露 `chat`、`bash`、`file` 等可发现技能；harness 不指定 driver 应调用哪一种。"
    step_4 = "harness 启动 driver 容器运行 `codex exec`；driver Codex 加载 `[mcp_servers.driver]` 后自主选择 MCP 工具、目标 slave 和 slave skill。"
    gate_line = f"autonomy_gate：`{json.dumps(autonomy_gate, ensure_ascii=False)}`；human_intervention_count：`{driver_usage['human_intervention_count']}`。"
    conclusion = "这轮实验更接近完整的单任务多 agent 执行：外层只提供 workspace、容器、observer 和一次性任务，driver 自主规划路由和技能调用。实际仍属于一任务 smoke，尚不是多任务统计实验。"
else:
    report_title = "Driver MCP + Codex 多 Agent 容器烟测报告"
    experiment_note = "本轮使用公开任务 `Terminal-Bench / heterogeneous-dates` 的本地适配版本。driver 是容器内真实 `codex exec`，通过 `driver-agent serve-mcp` 暴露的 MCP 工具协调两个真实 `slave-agent` 容器；observer-server 与 agentserver-stub 单独运行并记录事件。"
    step_3 = "harness 启动两个 slave-agent 容器，二者 `agent.kind=codex`，但本轮子任务通过 `bash`/`file` 技能执行，避免再引入 slave LLM 不确定性。"
    step_4 = "harness 启动 driver 容器运行 `codex exec`；driver Codex 加载 `[mcp_servers.driver]`，由 MCP 调用 `list_agents`、`write_slave_file`、`run_slave_bash`、`read_slave_file` 完成跨 slave 协作。"
    gate_line = f"工具门禁：`{json.dumps(driver_mcp_summary['required_tools_present'], ensure_ascii=False)}`；目标门禁：`{json.dumps(driver_mcp_summary['required_targets_present'], ensure_ascii=False)}`。"
    conclusion = "这轮 smoke 相比旧脚本更接近论文要测的多异构 agent 场景：driver 是真实 Codex CLI，编排能力来自 driver MCP；两个 slave 是真实 `slave-agent` 容器并连接同一 workspace；observer 独立记录运行信息。局限是本轮为降低成本和随机性，slave 侧使用 `bash`/`file` 技能执行确定性子任务，没有让 slave 再各自调用 Codex LLM。"

report = f"""# {report_title}

## 任务

{experiment_note}

## 执行流程

1. 外层 harness 构建 `agentserver-stub`、`observer-server`、`driver-agent`、`slave-agent` 四个本地二进制。
2. harness 启动 agentserver-stub 和 observer-server，并给 driver、data slave、compute slave 签发同一个 workspace 下的凭据。
3. {step_3}
4. {step_4}
5. harness 只在 driver 结束后复制 driver 写出的 `avg_temp.txt` 到最终 workspace 并运行公开 oracle。

## 容器职责

| 容器 | 进程 | 主要职责 | 可见状态 |
| --- | --- | --- | --- |
| `smoke-driver` | `codex exec` + `driver-agent serve-mcp` | 决策与编排，不能本地计算 benchmark，只能把子任务交给 slave | 原始输入、driver MCP、最终结果写入位置 |
| `smoke-data-slave` | `slave-agent` | 归一化异构日期格式并生成对齐中间表 | 通过 MCP 写入的原始 CSV |
| `smoke-compute-slave` | `slave-agent` | 基于 data slave 的中间表计算平均差值 | 通过 MCP 写入的 `aligned_temperatures.csv` |
| `observer-server` | `observer-server` | 记录 agent 身份、事件和任务状态 | SQLite: `observer/observer.db` |
| `agentserver-stub` | `agentserver-stub` | 本地 workspace、discovery、task queue、peer proxy | 单进程内存状态 |

## 结果

- 总体状态：{"通过" if passed else "未通过"}
- driver exit code：{driver_exit}
- oracle exit code：{oracle_exit}
- oracle JSON：`{json.dumps(oracle, ensure_ascii=False)}`
- `avg_temp.txt` 内容：`{result_value or "未生成"}`
- 输出目录：`{out_dir}`

## MCP 工具使用

| 工具 | driver task journal 记录数 |
| --- | ---: |
{chr(10).join(tool_lines)}

| 目标 slave | 被调用次数 |
| --- | ---: |
{chr(10).join(target_lines)}

{gate_line}

## Driver 实际选择

- 实际选择的 slave skills：`{json.dumps(driver_decision_summary["driver_selected_skills"], ensure_ascii=False)}`
- 实际选择的目标 slave：`{json.dumps(driver_decision_summary["driver_selected_targets"], ensure_ascii=False)}`
- human_intervention_count：{driver_usage["human_intervention_count"]}
- driver_decision_summary：`driver_decision_summary.json`

## Token 指标

| Codex 会话 | model_input_tokens | model_output_tokens | usage_events |
| --- | ---: | ---: | ---: |
{chr(10).join(usage_lines)}

- 总 input tokens：{usage_summary["total_model_input_tokens"]}
- 总 output tokens：{usage_summary["total_model_output_tokens"]}

## Observer 摘要

| 表 | 行数 |
| --- | ---: |
{chr(10).join(observer_lines)}

- event type counts：`{json.dumps(observer_summary["event_type_counts"], ensure_ascii=False)}`
- task status counts：`{json.dumps(observer_summary["task_status_counts"], ensure_ascii=False)}`
- agent role counts：`{json.dumps(observer_summary["agent_role_counts"], ensure_ascii=False)}`

## 产物

- `agent_usage_summary.json`：Codex CLI JSONL token 统计。
- `driver_mcp_tool_summary.json`：driver MCP 委托任务、工具和目标 slave 统计。
- `observer_summary.json`：observer SQLite 表级统计。
- `container_isolation.json`：容器、workspace、CODEX_HOME 和技能边界。
- `driver_decision_summary.json`：driver 实际选择的工具、slave skill、目标和无人介入门禁。
- `artifact_manifest.json`：主要输入、日志、结果和报告 SHA-256。
- `oracle_result.json`：公开 oracle 的原始输出。

## 结论

{conclusion}
"""
(out_dir / report_filename).write_text(report, encoding="utf-8")
out_root.mkdir(parents=True, exist_ok=True)
(out_root / "latest_run_path.txt").write_text(str(out_dir) + "\n", encoding="utf-8")
(out_root / report_filename).write_text(report, encoding="utf-8")

result_status = {
    "passed": passed,
    "prompt_mode": prompt_mode,
    "driver_exit": driver_exit,
    "oracle_exit": oracle_exit,
    "tool_gate": tool_gate,
    "autonomy_gate": autonomy_gate,
    "human_intervention_count": driver_usage["human_intervention_count"],
    "report": str(out_dir / report_filename),
}
(out_dir / "result_status.json").write_text(
    json.dumps(result_status, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)
PY

if [[ "$driver_exit" -ne 0 || "$oracle_exit" -ne 0 ]]; then
  echo "Driver MCP smoke did not pass. See ${OUT_DIR}/${REPORT_FILENAME}" >&2
  exit 1
fi

if [[ "$DRIVER_PROMPT_MODE" == "guided" ]]; then
  if ! python3 - "${OUT_DIR}/driver_mcp_tool_summary.json" <<'PY'
import json
import sys

summary = json.load(open(sys.argv[1], "r", encoding="utf-8"))
ok = all(summary["required_tools_present"].values()) and all(summary["required_targets_present"].values())
raise SystemExit(0 if ok else 1)
PY
  then
    echo "Driver MCP smoke did not use the required MCP tools/targets. See ${OUT_DIR}/driver_mcp_tool_summary.json" >&2
    exit 1
  fi
else
  if ! python3 - "${OUT_DIR}/result_status.json" <<'PY'
import json
import sys

status = json.load(open(sys.argv[1], "r", encoding="utf-8"))
ok = status.get("autonomy_gate") is True and status.get("human_intervention_count") == 0
raise SystemExit(0 if ok else 1)
PY
  then
    echo "Autonomous driver run did not satisfy autonomy/no-human gate. See ${OUT_DIR}/result_status.json" >&2
    exit 1
  fi
fi

echo "Report written to ${OUT_DIR}/${REPORT_FILENAME}"
