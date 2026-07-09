#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${1:-/root/paper_writing/paper_outputs/multi_agent_codex_container_smoke}"
IMAGE_TAG="${IMAGE_TAG:-multi-agent-container-smoke:codex-agents}"
CODEX_AGENT_NETWORK="${CODEX_AGENT_NETWORK:-host}"
SOURCE_CODEX_CONFIG="${CODEX_CONFIG_PATH:-${CODEX_HOME:-/root/.codex}/config.toml}"
CODEX_NODE_MODULE="${CODEX_NODE_MODULE:-/usr/lib/node_modules/@openai/codex}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd -P)"
WORKLOAD_DIR="${ROOT_DIR}/tests/eval/workloads/public-terminal-heterogeneous-dates"

INPUT_DIR="${OUT_DIR}/input"
ARTIFACT_DIR="${OUT_DIR}/artifacts"
FINAL_WORKSPACE="${OUT_DIR}/final_workspace"
PROMPT_DIR="${OUT_DIR}/prompts"
RUNTIME_DIR="${OUT_DIR}/runtime"
LOG_DIR="${OUT_DIR}/logs"
DATA_AGENT_DIR="${OUT_DIR}/data-agent"
SOLVER_AGENT_DIR="${OUT_DIR}/solver-agent"
VERIFIER_AGENT_DIR="${OUT_DIR}/verifier-agent"
CODEX_HOME_ROOT="${OUT_DIR}/codex_homes"

mkdir -p \
  "$INPUT_DIR" \
  "$ARTIFACT_DIR" \
  "$FINAL_WORKSPACE" \
  "$PROMPT_DIR" \
  "$RUNTIME_DIR" \
  "$LOG_DIR" \
  "$DATA_AGENT_DIR" \
  "$SOLVER_AGENT_DIR" \
  "$VERIFIER_AGENT_DIR" \
  "$CODEX_HOME_ROOT"

if [[ ! -r "$SOURCE_CODEX_CONFIG" ]]; then
  echo "Codex config is not readable: ${SOURCE_CODEX_CONFIG}" >&2
  exit 2
fi
if [[ ! -d "$CODEX_NODE_MODULE" ]]; then
  echo "Codex node module is not available: ${CODEX_NODE_MODULE}" >&2
  exit 2
fi

cp "${WORKLOAD_DIR}/fixtures/task-deps/daily_temp_sf_high.csv" "${INPUT_DIR}/daily_temp_sf_high.csv"
cp "${WORKLOAD_DIR}/fixtures/task-deps/daily_temp_sf_low.csv" "${INPUT_DIR}/daily_temp_sf_low.csv"

cat >"${INPUT_DIR}/task.md" <<'TASK'
Public benchmark task: Terminal-Bench heterogeneous-dates adaptation.

Use daily_temp_sf_high.csv and daily_temp_sf_low.csv to calculate the average
daily high-minus-low temperature difference. The final answer must be written
to avg_temp.txt as only a numeric value.
TASK

cat >"${PROMPT_DIR}/data-agent.md" <<'PROMPT'
You are data-agent in a multi-agent container benchmark.

Visible inputs:
- /workspace/input/task.md
- /workspace/input/daily_temp_sf_high.csv
- /workspace/input/daily_temp_sf_low.csv

Your job:
1. Inspect the two CSV files and identify the date formats, row counts, columns,
   and any ordering differences.
2. Normalize dates and align rows by date.
3. Write /artifacts/aligned_temperatures.csv with exactly these columns:
   date,high_temperature,low_temperature,difference
4. Write /artifacts/data_profile.md describing what you found and how the
   alignment was done.

Constraints:
- Do not write avg_temp.txt.
- Do not hard-code a final expected answer.
- Compute all derived values from the visible CSV files.
- Keep artifacts machine-readable and concise.
PROMPT

cat >"${PROMPT_DIR}/solver-agent.md" <<'PROMPT'
You are solver-agent in a multi-agent container benchmark.

Visible inputs:
- /artifacts/aligned_temperatures.csv
- /artifacts/data_profile.md

Your job:
1. Read the aligned CSV produced by data-agent.
2. Compute the arithmetic mean of the difference column.
3. Write /workspace/avg_temp.txt.
4. avg_temp.txt must contain only a numeric value and no explanatory text.
5. Write /out/solution_report.md with a short explanation of the calculation.

Constraints:
- You cannot rely on raw benchmark inputs.
- Do not hard-code an expected answer.
- Compute the final value from /artifacts/aligned_temperatures.csv.
PROMPT

cat >"${PROMPT_DIR}/verifier-agent.md" <<'PROMPT'
You are verifier-agent in a multi-agent container benchmark.

Visible inputs:
- /workspace/avg_temp.txt

Your job:
1. Inspect /workspace/avg_temp.txt without modifying /workspace.
2. Verify that it exists, is non-empty, and contains only a numeric value.
3. Write /out/verification_report.md summarizing the format check.

Constraints:
- Do not modify /workspace.
- Do not run or read oracle scripts. The harness runs the public oracle after
  all agents finish.
PROMPT

cat >"${RUNTIME_DIR}/Dockerfile" <<'DOCKERFILE'
FROM postgres:16-alpine
RUN apk update && apk add --no-cache bash coreutils findutils git nodejs python3 ripgrep
RUN ln -sf ../lib/node_modules/@openai/codex/bin/codex.js /usr/bin/codex
WORKDIR /workspace
DOCKERFILE

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

prepare_codex_home() {
  local agent="$1"
  local workspace="$2"
  local home="${CODEX_HOME_ROOT}/${agent}"
  mkdir -p "$home"
  write_minimal_codex_config "$SOURCE_CODEX_CONFIG" "${home}/config.toml" "$workspace" "/tmp"
}

prepare_codex_home data-agent /workspace
prepare_codex_home solver-agent /workspace
prepare_codex_home verifier-agent /workspace

echo "Building ${IMAGE_TAG} for containerized Codex agents..."
docker build --network host --pull=false -t "$IMAGE_TAG" "$RUNTIME_DIR" \
  >"${LOG_DIR}/docker-build.stdout.log" \
  2>"${LOG_DIR}/docker-build.stderr.log"

run_codex_agent() {
  local agent="$1"
  local prompt_file="$2"
  local codex_home="${CODEX_HOME_ROOT}/${agent}"
  local out_dir="${OUT_DIR}/${agent}"
  shift 2

  local stdout_log="${LOG_DIR}/${agent}.codex.jsonl"
  local stderr_log="${LOG_DIR}/${agent}.stderr.log"
  local run_json="${RUNTIME_DIR}/${agent}.run.json"
  local start_ns
  local end_ns
  local exit_code

  mkdir -p "$out_dir"
  start_ns="$(date +%s%N)"
  set +e
  docker run \
    --rm \
    --name "ma-codex-${agent}-$$" \
    --network "$CODEX_AGENT_NETWORK" \
    -e OPENAI_API_KEY \
    -e HTTP_PROXY \
    -e HTTPS_PROXY \
    -e NO_PROXY \
    -e CODEX_HOME=/codex-home \
    -e HOME=/tmp \
    -v "${CODEX_NODE_MODULE}:/usr/lib/node_modules/@openai/codex:ro" \
    -v "${codex_home}:/codex-home" \
    -v "${prompt_file}:/prompt.md:ro" \
    -v "${out_dir}:/out" \
    "$@" \
    "$IMAGE_TAG" \
    sh -lc 'codex exec --json --output-last-message /out/last_message.md --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -C /workspace - < /prompt.md' \
    >"$stdout_log" \
    2>"$stderr_log"
  exit_code="$?"
  set -e
  end_ns="$(date +%s%N)"

  python3 - "$run_json" "$agent" "$exit_code" "$start_ns" "$end_ns" "$stdout_log" "$stderr_log" "$CODEX_AGENT_NETWORK" <<'PY'
import json
import sys
from pathlib import Path

path, agent, exit_code, start_ns, end_ns, stdout_log, stderr_log, network = sys.argv[1:9]
start_ns = int(start_ns)
end_ns = int(end_ns)
record = {
    "agent": agent,
    "exit_code": int(exit_code),
    "network": network,
    "start_ns": start_ns,
    "end_ns": end_ns,
    "duration_seconds": round((end_ns - start_ns) / 1_000_000_000, 3),
    "stdout_jsonl": stdout_log,
    "stderr_log": stderr_log,
}
Path(path).write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
PY

  return "$exit_code"
}

failed=0

if ! run_codex_agent data-agent "${PROMPT_DIR}/data-agent.md" \
  -v "${INPUT_DIR}:/workspace/input:ro" \
  -v "${ARTIFACT_DIR}:/artifacts"; then
  failed=1
fi

if ! run_codex_agent solver-agent "${PROMPT_DIR}/solver-agent.md" \
  -v "${FINAL_WORKSPACE}:/workspace" \
  -v "${ARTIFACT_DIR}:/artifacts:ro"; then
  failed=1
fi

if ! run_codex_agent verifier-agent "${PROMPT_DIR}/verifier-agent.md" \
  -v "${FINAL_WORKSPACE}:/workspace:ro"; then
  failed=1
fi

set +e
"${WORKLOAD_DIR}/oracle.sh" "$FINAL_WORKSPACE" >"${OUT_DIR}/oracle_result.json" 2>"${LOG_DIR}/driver-oracle.stderr.log"
oracle_exit="$?"
set -e

python3 - "$OUT_DIR" "$IMAGE_TAG" "$CODEX_AGENT_NETWORK" "$oracle_exit" <<'PY'
import hashlib
import json
import os
import sys
import time
from pathlib import Path

out_dir = Path(sys.argv[1])
image_tag = sys.argv[2]
network = sys.argv[3]
oracle_exit = int(sys.argv[4])
agents = ["data-agent", "solver-agent", "verifier-agent"]

def load_json(path: Path, default):
    if not path.exists():
        return default
    text = path.read_text(encoding="utf-8").strip()
    if not text:
        return default
    return json.loads(text)

def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()

def find_usage(obj):
    if isinstance(obj, dict):
        usage = obj.get("usage")
        if isinstance(usage, dict):
            yield usage
        for key in ("event", "response", "message"):
            wrapped = obj.get(key)
            if isinstance(wrapped, dict):
                yield from find_usage(wrapped)

def token_int(usage, *keys):
    for key in keys:
        value = usage.get(key)
        if isinstance(value, int):
            return value
        if isinstance(value, float) and value.is_integer():
            return int(value)
    return 0

usage_agents = []
for agent in agents:
    path = out_dir / "logs" / f"{agent}.codex.jsonl"
    input_tokens = 0
    output_tokens = 0
    usage_events = 0
    if path.exists():
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            for usage in find_usage(obj):
                usage_events += 1
                input_tokens += token_int(usage, "input_tokens", "prompt_tokens")
                output_tokens += token_int(usage, "output_tokens", "completion_tokens")
    usage_agents.append(
        {
            "agent": agent,
            "model_input_tokens": input_tokens,
            "model_output_tokens": output_tokens,
            "usage_events": usage_events,
            "jsonl": os.path.relpath(path, out_dir),
        }
    )

usage_summary = {
    "agents": usage_agents,
    "total_model_input_tokens": sum(item["model_input_tokens"] for item in usage_agents),
    "total_model_output_tokens": sum(item["model_output_tokens"] for item in usage_agents),
}
(out_dir / "agent_usage_summary.json").write_text(
    json.dumps(usage_summary, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

run_records = [load_json(out_dir / "runtime" / f"{agent}.run.json", {}) for agent in agents]
isolation = {
    "image": image_tag,
    "base_image": "postgres:16-alpine",
    "build_network": "host",
    "agent_network": network,
    "codex_state": "one temporary CODEX_HOME per agent; only model provider config is rendered from the source config",
    "agents": [
        {
            "name": "data-agent",
            "mounts": [
                "input:/workspace/input:ro",
                "artifacts:/artifacts:rw",
                "data-agent:/out:rw",
                "codex_homes/data-agent:/codex-home:rw",
            ],
        },
        {
            "name": "solver-agent",
            "mounts": [
                "final_workspace:/workspace:rw",
                "artifacts:/artifacts:ro",
                "solver-agent:/out:rw",
                "codex_homes/solver-agent:/codex-home:rw",
            ],
        },
        {
            "name": "verifier-agent",
            "mounts": [
                "final_workspace:/workspace:ro",
                "verifier-agent:/out:rw",
                "codex_homes/verifier-agent:/codex-home:rw",
            ],
        },
    ],
    "runs": run_records,
}
(out_dir / "container_isolation.json").write_text(
    json.dumps(isolation, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

manifest_candidates = [
    out_dir / "input" / "task.md",
    out_dir / "input" / "daily_temp_sf_high.csv",
    out_dir / "input" / "daily_temp_sf_low.csv",
    out_dir / "artifacts" / "aligned_temperatures.csv",
    out_dir / "artifacts" / "data_profile.md",
    out_dir / "final_workspace" / "avg_temp.txt",
    out_dir / "data-agent" / "last_message.md",
    out_dir / "solver-agent" / "last_message.md",
    out_dir / "solver-agent" / "solution_report.md",
    out_dir / "verifier-agent" / "last_message.md",
    out_dir / "verifier-agent" / "verification_report.md",
    out_dir / "oracle_result.json",
    out_dir / "agent_usage_summary.json",
    out_dir / "container_isolation.json",
]
for agent in agents:
    manifest_candidates.append(out_dir / "logs" / f"{agent}.codex.jsonl")
    manifest_candidates.append(out_dir / "logs" / f"{agent}.stderr.log")
manifest = {"generated_at_unix": int(time.time()), "files": []}
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
passed = bool(oracle.get("passed")) and all(int(r.get("exit_code", 1)) == 0 for r in run_records)
result_value = ""
avg_path = out_dir / "final_workspace" / "avg_temp.txt"
if avg_path.exists():
    result_value = avg_path.read_text(encoding="utf-8", errors="replace").strip()

run_lines = []
for record in run_records:
    if not record:
        run_lines.append("| 未记录 | 未记录 | 未记录 | 未记录 |")
        continue
    run_lines.append(
        f"| `{record['agent']}` | {record['exit_code']} | {record['duration_seconds']} | "
        f"`{os.path.relpath(record['stdout_jsonl'], out_dir)}` / "
        f"`{os.path.relpath(record['stderr_log'], out_dir)}` |"
    )

usage_lines = []
for item in usage_agents:
    usage_lines.append(
        f"| `{item['agent']}` | {item['model_input_tokens']} | "
        f"{item['model_output_tokens']} | {item['usage_events']} |"
    )

report = f"""# 真实 Codex 多 Agent 容器协作烟测报告

## 任务与目标

本轮使用公开任务 `Terminal-Bench / heterogeneous-dates` 的本地适配版本。任务要求读取每日高温与低温 CSV，处理不一致的日期格式，计算每日高温减低温差值的平均数，并把最终数字写入 `avg_temp.txt`。

这个实验替代上一版脚本化 smoke：driver 不写解题程序，只负责启动容器、限制可见文件、传递 artifact、收集日志和运行最终 oracle。

## Driver

- Driver 脚本：`tools/eval/container_smoke/run_heterogeneous_dates_codex_agents.sh`
- 输出目录：`{out_dir}`
- 运行镜像：`{image_tag}`
- Agent 网络：`{network}`。真实 Codex CLI 需要访问模型服务，因此本轮保留模型网络访问；隔离边界主要是容器进程、独立 `CODEX_HOME` 和文件系统挂载。

## Agent 顺序与职责

| 顺序 | Agent | 容器内功能 | 可见输入 | 主要输出 |
| ---: | --- | --- | --- | --- |
| 1 | `data-agent` | 运行真实 `codex exec`，检查两个 CSV，归一化日期并生成对齐后的中间表 | 原始 high/low CSV | `artifacts/aligned_temperatures.csv`, `artifacts/data_profile.md` |
| 2 | `solver-agent` | 运行真实 `codex exec`，只基于中间 artifact 计算平均差值 | data-agent 产物 | `final_workspace/avg_temp.txt`, `solver-agent/solution_report.md` |
| 3 | `verifier-agent` | 运行真实 `codex exec`，只检查最终 artifact 的格式；公开 oracle 由 harness 在 agent 结束后运行 | `avg_temp.txt` | `verifier-agent/verification_report.md` |

## 隔离与状态

- 每个 agent 使用单独容器、单独输出目录、单独 `CODEX_HOME`。
- 临时 `CODEX_HOME` 只渲染源配置中的模型 provider 与 trusted project，不复制 MCP、sessions、history、skills 或 superpowers。
- `data-agent` 能看原始输入，但不能写最终 workspace。
- `solver-agent` 不能看原始 CSV，只能看 data-agent 产物并写最终答案。
- `verifier-agent` 以只读方式挂载最终 workspace，只能写自己的格式验证报告；它不能读取公开 oracle。

## 运行结果

- 总体状态：{"通过" if passed else "未通过"}
- Driver oracle 退出码：{oracle_exit}
- Driver oracle JSON：`{json.dumps(oracle, ensure_ascii=False)}`
- `avg_temp.txt` 内容：`{result_value or "未生成"}`

| Agent | 退出码 | 耗时（秒） | 日志 |
| --- | ---: | ---: | --- |
{chr(10).join(run_lines)}

## Token 指标

| Agent | model_input_tokens | model_output_tokens | usage_events |
| --- | ---: | ---: | ---: |
{chr(10).join(usage_lines)}

- 总 input tokens：{usage_summary["total_model_input_tokens"]}
- 总 output tokens：{usage_summary["total_model_output_tokens"]}

## 产物

- `agent_usage_summary.json`：按 agent 汇总 Codex JSONL 里的 token usage。
- `container_isolation.json`：记录镜像、网络、挂载边界和退出码。
- `artifact_manifest.json`：记录输入、中间产物、答案、报告和日志的 SHA-256。
- `oracle_result.json`：driver 对最终 workspace 执行公开 oracle 的原始结果。

## 结论与局限

本轮更接近论文需要的多异构 agent 场景：真实 Codex CLI agent 在独立容器中按角色协作，solver-agent 不能直接读取原始 CSV，必须依赖 data-agent 的 artifact。局限是模型网络仍然开放，这是运行真实 LLM agent 的必要条件；如果要测完全离线网络隔离，需要把模型服务也放到容器网络或本地代理中。
"""
(out_dir / "multi_agent_codex_container_report_cn.md").write_text(report, encoding="utf-8")
PY

if [[ "$failed" -ne 0 || "$oracle_exit" -ne 0 ]]; then
  echo "Codex multi-agent smoke did not pass. See ${OUT_DIR}/multi_agent_codex_container_report_cn.md" >&2
  exit 1
fi

echo "Report written to ${OUT_DIR}/multi_agent_codex_container_report_cn.md"
