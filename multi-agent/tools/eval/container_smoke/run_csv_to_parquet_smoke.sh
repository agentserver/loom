#!/usr/bin/env bash
set -euo pipefail

OUT_DIR="${1:-/root/paper_writing/paper_outputs/multi_agent_container_smoke}"
IMAGE_TAG="${IMAGE_TAG:-multi-agent-container-smoke:csv-to-parquet}"

INPUT_DIR="${OUT_DIR}/input"
INSPECTOR_DIR="${OUT_DIR}/agent-inspector"
CONVERTER_DIR="${OUT_DIR}/agent-converter"
VERIFIER_DIR="${OUT_DIR}/agent-verifier"
ARTIFACT_DIR="${OUT_DIR}/artifacts"
LOG_DIR="${OUT_DIR}/logs"
RUNTIME_DIR="${OUT_DIR}/runtime"

mkdir -p \
  "$INPUT_DIR" \
  "$INSPECTOR_DIR" \
  "$CONVERTER_DIR" \
  "$VERIFIER_DIR" \
  "$ARTIFACT_DIR" \
  "$LOG_DIR" \
  "$RUNTIME_DIR"

cat >"${INPUT_DIR}/data.csv" <<'CSV'
name,age,city
John,25,New York
Alice,30,San Francisco
Bob,35,Chicago
Emma,28,Boston
David,33,Seattle
CSV

python3 - "${INPUT_DIR}/data.csv" "${INPUT_DIR}/source_metadata.json" <<'PY'
import hashlib
import json
import sys
from pathlib import Path

data_path = Path(sys.argv[1])
metadata_path = Path(sys.argv[2])
metadata = {
    "benchmark": "Terminal-Bench",
    "task": "csv-to-parquet",
    "source_task_yaml_sha": "7340e0ccfe76db8a7b970a5482940199e38ebf4f",
    "source_data_csv_sha": "54d12838d9cef48ed4ee40960748d57758c23c08",
    "input_sha256": hashlib.sha256(data_path.read_bytes()).hexdigest(),
    "task_instruction": "Convert /app/data.csv to /app/data.parquet.",
    "oracle": "pandas read_csv/read_parquet followed by pandas.testing.assert_frame_equal",
}
metadata_path.write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + "\n")
PY

cat >"${RUNTIME_DIR}/Dockerfile" <<'DOCKERFILE'
FROM postgres:16-alpine
RUN apk update && apk add --no-cache python3 py3-pandas py3-pyarrow py3-pytest coreutils
WORKDIR /work
DOCKERFILE

cat >"${RUNTIME_DIR}/agent_inspector.py" <<'PY'
import hashlib
import json
from pathlib import Path

import pandas as pd

csv_path = Path("/input/data.csv")
df = pd.read_csv(csv_path)
columns = []
for column in df.columns:
    series = df[column]
    columns.append(
        {
            "name": column,
            "dtype": str(series.dtype),
            "non_null": int(series.notna().sum()),
            "sample_values": [str(value) for value in series.head(3).tolist()],
        }
    )

report = {
    "agent": "agent-inspector",
    "role": "inspect input schema and row count",
    "input": "/input/data.csv",
    "input_sha256": hashlib.sha256(csv_path.read_bytes()).hexdigest(),
    "row_count": int(len(df)),
    "column_count": int(len(df.columns)),
    "columns": columns,
}
Path("/out/schema_report.json").write_text(
    json.dumps(report, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)
print(json.dumps({"agent": "agent-inspector", "row_count": report["row_count"]}))
PY

cat >"${RUNTIME_DIR}/agent_converter.py" <<'PY'
import hashlib
import json
from pathlib import Path

import pandas as pd

csv_path = Path("/input/data.csv")
schema_path = Path("/inspect/schema_report.json")
parquet_path = Path("/artifacts/data.parquet")

schema = json.loads(schema_path.read_text(encoding="utf-8"))
df = pd.read_csv(csv_path)
expected_columns = [column["name"] for column in schema["columns"]]
if list(df.columns) != expected_columns:
    raise SystemExit(
        f"schema mismatch: expected {expected_columns}, got {list(df.columns)}"
    )

df.to_parquet(parquet_path, index=False)
report = {
    "agent": "agent-converter",
    "role": "convert CSV to Parquet",
    "input": "/input/data.csv",
    "schema_report": "/inspect/schema_report.json",
    "output": "/artifacts/data.parquet",
    "input_sha256": hashlib.sha256(csv_path.read_bytes()).hexdigest(),
    "output_sha256": hashlib.sha256(parquet_path.read_bytes()).hexdigest(),
    "row_count": int(len(df)),
    "column_count": int(len(df.columns)),
    "columns": list(df.columns),
}
Path("/out/conversion_report.json").write_text(
    json.dumps(report, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)
print(json.dumps({"agent": "agent-converter", "output": str(parquet_path)}))
PY

cat >"${RUNTIME_DIR}/agent_verifier.py" <<'PY'
import hashlib
import json
import traceback
from pathlib import Path

import pandas as pd

csv_path = Path("/input/data.csv")
parquet_path = Path("/artifacts/data.parquet")
result = {
    "agent": "agent-verifier",
    "role": "run public-task oracle against produced Parquet",
    "input": "/input/data.csv",
    "candidate_output": "/artifacts/data.parquet",
    "passed": False,
}

try:
    csv_df = pd.read_csv(csv_path).reset_index(drop=True)
    parquet_df = pd.read_parquet(parquet_path).reset_index(drop=True)
    pd.testing.assert_frame_equal(csv_df, parquet_df)
    result.update(
        {
            "passed": True,
            "row_count": int(len(csv_df)),
            "column_count": int(len(csv_df.columns)),
            "columns": list(csv_df.columns),
            "input_sha256": hashlib.sha256(csv_path.read_bytes()).hexdigest(),
            "output_sha256": hashlib.sha256(parquet_path.read_bytes()).hexdigest(),
            "oracle": "pandas.testing.assert_frame_equal(read_csv(data.csv), read_parquet(data.parquet))",
        }
    )
except Exception as exc:
    result.update(
        {
            "passed": False,
            "error": str(exc),
            "traceback": traceback.format_exc(),
        }
    )

Path("/out/verification.json").write_text(
    json.dumps(result, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)
print(json.dumps({"agent": "agent-verifier", "passed": result["passed"]}))
if not result["passed"]:
    raise SystemExit(1)
PY

echo "Building ${IMAGE_TAG} from postgres:16-alpine..."
docker build --network host --pull=false -t "$IMAGE_TAG" "$RUNTIME_DIR" \
  >"${LOG_DIR}/docker-build.stdout.log" \
  2>"${LOG_DIR}/docker-build.stderr.log"

run_agent() {
  local agent="$1"
  shift
  local stdout_log="${LOG_DIR}/${agent}.stdout.log"
  local stderr_log="${LOG_DIR}/${agent}.stderr.log"
  local run_json="${RUNTIME_DIR}/${agent}.run.json"
  local start_ns
  local end_ns
  local exit_code

  start_ns="$(date +%s%N)"
  set +e
  docker run "$@" >"$stdout_log" 2>"$stderr_log"
  exit_code="$?"
  set -e
  end_ns="$(date +%s%N)"

  python3 - "$run_json" "$agent" "$exit_code" "$start_ns" "$end_ns" "$stdout_log" "$stderr_log" <<'PY'
import json
import sys
from pathlib import Path

path, agent, exit_code, start_ns, end_ns, stdout_log, stderr_log = sys.argv[1:8]
start_ns = int(start_ns)
end_ns = int(end_ns)
record = {
    "agent": agent,
    "exit_code": int(exit_code),
    "start_ns": start_ns,
    "end_ns": end_ns,
    "duration_seconds": round((end_ns - start_ns) / 1_000_000_000, 3),
    "stdout_log": stdout_log,
    "stderr_log": stderr_log,
}
Path(path).write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n")
PY

  return "$exit_code"
}

failed=0

if ! run_agent agent-inspector \
  --rm \
  --name agent-inspector \
  --network none \
  -v "${INPUT_DIR}:/input:ro" \
  -v "${INSPECTOR_DIR}:/out" \
  -v "${RUNTIME_DIR}:/runtime:ro" \
  "$IMAGE_TAG" \
  python3 /runtime/agent_inspector.py; then
  failed=1
fi

if ! run_agent agent-converter \
  --rm \
  --name agent-converter \
  --network none \
  -v "${INPUT_DIR}:/input:ro" \
  -v "${INSPECTOR_DIR}:/inspect:ro" \
  -v "${ARTIFACT_DIR}:/artifacts" \
  -v "${CONVERTER_DIR}:/out" \
  -v "${RUNTIME_DIR}:/runtime:ro" \
  "$IMAGE_TAG" \
  python3 /runtime/agent_converter.py; then
  failed=1
fi

if ! run_agent agent-verifier \
  --rm \
  --name agent-verifier \
  --network none \
  -v "${INPUT_DIR}:/input:ro" \
  -v "${ARTIFACT_DIR}:/artifacts:ro" \
  -v "${VERIFIER_DIR}:/out" \
  -v "${RUNTIME_DIR}:/runtime:ro" \
  "$IMAGE_TAG" \
  python3 /runtime/agent_verifier.py; then
  failed=1
fi

python3 - "$OUT_DIR" "$IMAGE_TAG" <<'PY'
import hashlib
import json
import os
import sys
from pathlib import Path

out_dir = Path(sys.argv[1])
image_tag = sys.argv[2]

def load_json(path: Path, default):
    if not path.exists():
        return default
    return json.loads(path.read_text(encoding="utf-8"))

def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()

manifest_paths = [
    out_dir / "input" / "data.csv",
    out_dir / "input" / "source_metadata.json",
    out_dir / "agent-inspector" / "schema_report.json",
    out_dir / "agent-converter" / "conversion_report.json",
    out_dir / "agent-verifier" / "verification.json",
    out_dir / "artifacts" / "data.parquet",
    out_dir / "logs" / "docker-build.stdout.log",
    out_dir / "logs" / "docker-build.stderr.log",
    out_dir / "logs" / "agent-inspector.stdout.log",
    out_dir / "logs" / "agent-inspector.stderr.log",
    out_dir / "logs" / "agent-converter.stdout.log",
    out_dir / "logs" / "agent-converter.stderr.log",
    out_dir / "logs" / "agent-verifier.stdout.log",
    out_dir / "logs" / "agent-verifier.stderr.log",
]
manifest = {
    "generated_at_unix": int(__import__("time").time()),
    "files": [],
}
for path in manifest_paths:
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

agent_runs = [
    load_json(out_dir / "runtime" / "agent-inspector.run.json", {}),
    load_json(out_dir / "runtime" / "agent-converter.run.json", {}),
    load_json(out_dir / "runtime" / "agent-verifier.run.json", {}),
]
isolation = {
    "image": image_tag,
    "base_image": "postgres:16-alpine",
    "build_network": "host",
    "agent_network": "none",
    "agents": [
        {
            "name": "agent-inspector",
            "mounts": [
                "input:/input:ro",
                "agent-inspector:/out:rw",
                "runtime:/runtime:ro",
            ],
        },
        {
            "name": "agent-converter",
            "mounts": [
                "input:/input:ro",
                "agent-inspector:/inspect:ro",
                "artifacts:/artifacts:rw",
                "agent-converter:/out:rw",
                "runtime:/runtime:ro",
            ],
        },
        {
            "name": "agent-verifier",
            "mounts": [
                "input:/input:ro",
                "artifacts:/artifacts:ro",
                "agent-verifier:/out:rw",
                "runtime:/runtime:ro",
            ],
        },
    ],
    "runs": agent_runs,
}
(out_dir / "container_isolation.json").write_text(
    json.dumps(isolation, ensure_ascii=False, indent=2) + "\n",
    encoding="utf-8",
)

source = load_json(out_dir / "input" / "source_metadata.json", {})
schema = load_json(out_dir / "agent-inspector" / "schema_report.json", {})
conversion = load_json(out_dir / "agent-converter" / "conversion_report.json", {})
verification = load_json(out_dir / "agent-verifier" / "verification.json", {})
passed = bool(verification.get("passed"))
total_agent_seconds = round(
    sum(float(run.get("duration_seconds", 0.0)) for run in agent_runs), 3
)
rows = verification.get("row_count", schema.get("row_count", "未生成"))
columns = verification.get("columns", conversion.get("columns", []))
status = "通过" if passed else "未通过"

agent_lines = []
for run in agent_runs:
    if not run:
        continue
    agent_lines.append(
        f"| {run['agent']} | {run['exit_code']} | {run['duration_seconds']} | "
        f"`{os.path.relpath(run['stdout_log'], out_dir)}` / "
        f"`{os.path.relpath(run['stderr_log'], out_dir)}` |"
    )
if not agent_lines:
    agent_lines.append("| 未记录 | 未记录 | 未记录 | 未记录 |")

report = f"""# 多 Agent 容器隔离烟测报告

## 任务来源

本轮选择公开基准 Terminal-Bench 的 `csv-to-parquet` 任务：输入为 `/app/data.csv`，目标是生成 `/app/data.parquet`，公开 oracle 使用 pandas 同时读取 CSV 与 Parquet，并执行 `assert_frame_equal`。本地烟测保留公开任务语义，将路径映射为容器内 `/input/data.csv` 与 `/artifacts/data.parquet`。

- 任务：`{source.get('benchmark', 'Terminal-Bench')} / {source.get('task', 'csv-to-parquet')}`
- `task.yaml` SHA：`{source.get('source_task_yaml_sha', '未记录')}`
- `data.csv` SHA：`{source.get('source_data_csv_sha', '未记录')}`
- 本地输入 SHA-256：`{source.get('input_sha256', '未生成')}`

## 多 Agent 拆分

| Agent | 容器职责 | 输入挂载 | 输出 |
| --- | --- | --- | --- |
| `agent-inspector` | 读取 CSV，统计列、类型、行数和输入哈希 | `input:/input:ro` | `agent-inspector/schema_report.json` |
| `agent-converter` | 读取 CSV 与 schema 报告，生成 Parquet | `input:/input:ro`, `agent-inspector:/inspect:ro` | `artifacts/data.parquet`, `agent-converter/conversion_report.json` |
| `agent-verifier` | 用公开 oracle 语义验证 Parquet 与 CSV 等价 | `input:/input:ro`, `artifacts:/artifacts:ro` | `agent-verifier/verification.json` |

## 隔离设置

- 运行镜像：`{image_tag}`，基础镜像：`postgres:16-alpine`
- 镜像构建：使用 `--network host` 安装 pandas/pyarrow 依赖。
- Agent 运行：三个任务容器均使用 `--network none`，只通过显式挂载目录交换产物。
- 凭据与用户环境：本轮未把宿主侧 LLM 配置挂入容器，容器内 agent 为脚本化任务 agent。

## 运行结果

- 总体状态：{status}
- 验证行数：{rows}
- 验证列：`{', '.join(columns) if isinstance(columns, list) else columns}`
- Agent 容器累计运行时间：{total_agent_seconds} 秒
- 输出 Parquet SHA-256：`{conversion.get('output_sha256', verification.get('output_sha256', '未生成'))}`

| Agent | 退出码 | 耗时（秒） | 日志 |
| --- | ---: | ---: | --- |
{chr(10).join(agent_lines)}

## 产物

- `artifact_manifest.json`：记录输入、中间报告、Parquet、日志的大小和 SHA-256。
- `container_isolation.json`：记录镜像、网络模式、挂载边界和每个 agent 的退出码/耗时。
- `agent-inspector/schema_report.json`：输入 schema 和样例值。
- `agent-converter/conversion_report.json`：转换输入/输出哈希和尺寸。
- `agent-verifier/verification.json`：oracle 结果。

## 结论与局限

本轮证明了公开任务可以被简单改造为多异构 agent 场景：agent 之间不共享进程、网络或隐式工作目录，只通过受控文件产物传递信息。局限是本次为快速烟测，容器内使用脚本化 agent，没有在容器中启动 Codex CLI，因此不产生 LLM token 指标；后续若要评估真实 LLM agent 协作，需要把 CLI 运行时和认证隔离方案纳入实验设计。
"""
(out_dir / "multi_agent_container_report_cn.md").write_text(
    report,
    encoding="utf-8",
)
PY

if [[ "$failed" -ne 0 ]]; then
  echo "One or more container agents failed. See ${OUT_DIR}/multi_agent_container_report_cn.md" >&2
  exit 1
fi

echo "Report written to ${OUT_DIR}/multi_agent_container_report_cn.md"
