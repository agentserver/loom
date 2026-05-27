# loom Python 库 (v0) — 设计

**日期：** 2026-05-27
**状态：** draft, awaiting user review
**范围：** 在 `multi-agent/python/` 下产出一个 Python 包 `loom`，把 driver MCP 工具集包成 Python 用户友好的 workflow / fluent verb API。**目的是用最小代价验证 "agent 编程语言" 的组合子形状是否好用**——验证完才决定要不要进一步做独立 DSL。

## 目标

让一个 Python 用户能用一段普通脚本驱动一次完整的 loom 任务（包括多步、含 humanloop 暂停/继续、动态 MCP 注册、文件 I/O），**不暴露任何 driver MCP JSON-RPC 细节、不暴露 slave 文件系统路径、不要求理解 TaskContract envelope**。

库的成功标准：

1. 能在 ≤ 50 行 Python 内复现 `tests/humanloop_e2e/scripts/case2_ask_user.py` 的全部行为
2. 同样能复现 `case5_jetson.py`（跨 arm64 节点）+ `case6_slave_offline.py`（错误恢复）+ Capability 演化循环（scaffold → register → 复用）
3. API 形态 "看起来像 DSL" —— fluent verb 链式调用，未来转写为独立 DSL 时工作量最小
4. 零运行时 Python 依赖（stdlib only）；driver-agent Go binary 是必要外部依赖

## 非目标

- **不做 envelope 编译**：v0 prompt 一字不差走 driver MCP 的 `submit_task`；用户自己用 Python 变量做 task 间数据流。详见下方 § 6 "envelope 决策"。
- **不做 DAG / fanout 编排**：v0 顺序执行；并发交给用户自己 ThreadPool。`wf.fanout([...])` 留 v0.2。
- **不做 retry / circuit breaker**：v0 失败抛 `TaskFailed`；用户自己决定重试策略。
- **不做 Codex backend 差异处理**：v0 假设 backend 是 Claude；codex resume 差异留 v0.2（unit test 已覆盖 backend.RunResume 接口，但 Python API 不暴露差异）。
- **不做 metrics / telemetry**：observer 那侧已经记了；Python 库不重复。
- **不上 PyPI**（v0）：用户走 `pip install -e multi-agent/python` 或 `pip install git+https://github.com/agentserver/loom.git#subdirectory=multi-agent/python`。

## 设计

### 1. 包结构

```
multi-agent/python/
├── pyproject.toml             # name="loom-py", version="0.1.0", python ≥3.10, zero runtime deps
├── README.md                  # 5 分钟 quickstart
├── src/loom/
│   ├── __init__.py            # 导出 workflow / Workflow / Slave / Question / errors
│   ├── client.py              # 底层 driver-agent 进程 + JSON-RPC 通信
│   ├── workflow.py            # Workflow 上下文管理器 + fluent verb API
│   ├── tasks.py               # Task / FutureTask / TaskResult 数据类
│   ├── humanloop.py           # AwaitingUser / Question / expect_or_ask 实现 + 默认终端 handler
│   ├── capability.py          # list_agents / inspect_capabilities / register_mcp 包装
│   ├── files.py               # inputs/outputs 协议 + Blob 抽象
│   ├── errors.py              # LoomError 层级
│   └── _driver_bin.py         # 定位 driver-agent binary
└── tests/
    ├── unit/                  # 无 driver 依赖,fake stdio
    ├── integration/           # 真 driver-agent + 本地 fake slave
    └── e2e/                   # 真 prod_test fleet (slave-local-prod)
```

### 2. 核心 API 形态（4 类典型用法）

#### 2.1 最小用法

```python
import loom

with loom.workflow(goal="询问 slave 当前时间") as wf:
    res = wf.chat("now in UTC, one line", target="slave-local-prod").wait()
print(res.output)
```

- `loom.workflow(goal=...)` 上下文管理器；`goal` 记到 Workflow 对象用于日志/调试，**不进 wire format**
- `wf.chat(...)` 是 `wf.submit(prompt=..., skill="chat", ...)` 的语义糖
- `.wait()` block 到 terminal（completed / failed / cancelled / awaiting_user）

#### 2.2 Humanloop pause/resume

```python
# 默认用法:终端交互
with loom.workflow(goal="…") as wf:
    res = (wf.chat("Choose red or blue, ask the user.", target="slave-local-prod")
             .expect_or_ask()              # ← 无 handler 时用默认终端 handler
             .wait())

# 自定义 handler
def my_handler(q: loom.Question) -> str:
    if q.kind == "request_permission":
        return "approve" if some_policy_check(q) else "deny"
    return ask_my_chat_ui(q.question, q.options)

res = wf.chat(...).expect_or_ask(my_handler).wait()
```

**默认 handler 行为**：
- `ask_user(q, options=[...])` → 终端打印 `q (red/blue): `，读 stdin 一行返回
- `request_permission(intent, target, reason)` → 终端打印 `Approve <intent> on <target>? (y/N): `；按 y 返 `approve`，其余返 `deny`
- **stdin 不是 TTY 时**：抛 `loom.NoInteractiveHandler` —— 强制脚本/cron 用户传 handler；**不静默 approve**

`.expect_or_ask(handler)` 内部循环：监听 `awaiting_user → handler(q) → resume_task(answer) → 下次 wait`，直到 terminal。多轮 ask_user 自然支持。session_id 由库内部维护。

#### 2.3 能力发现 + 动态扩

```python
with loom.workflow(goal="跑一段需要 weather 工具的脚本") as wf:
    slave = wf.find_slave(mcp_tool="weather_forecast")
    if slave is None:
        host = wf.find_slave(skill="register_mcp")
        spec = loom.MCPSpec.from_dir("./weather-mcp/")  # 读 spec.json + cases.jsonl
        wf.scaffold_and_register(host, spec).wait()     # 内部: scaffold + acceptance + register
        slave = host
    res = wf.chat("Will it rain in Beijing tomorrow? Use weather_forecast.",
                  target=slave).wait()
print(res.output)
```

- `wf.find_slave(mcp_tool=...)` / `wf.list_slaves(...)` 是 `list_agents` + `inspect_capabilities` 的高层
- `wf.scaffold_and_register(...)` 是单一高层动词包 3 步；acceptance 失败抛 `loom.AcceptanceFailed`
- 底层留 `wf.scaffold(...)` / `wf.register_mcp(...)` 分步原语，需要看中间态的用户可用低阶 API
- `MCPSpec.from_dir(...)` 读 `spec.json` + `cases.jsonl` 形成上传 payload

#### 2.4 文件 I/O（inputs/outputs 声明式）

```python
with loom.workflow(goal="让 slave 处理用户提供的 CSV") as wf:
    res = wf.chat(
        "Read {input:data} and write summary stats to {output:report}.",
        target="slave-local-prod",
        inputs={"data": "./data.csv"},          # 本地路径
        outputs={"report": "./report.md"},      # 本地路径(预留)
    ).wait()
print(res.outputs["report"])  # './report.md',库已自动从 slave 拉回
```

**库内部做的事**：
1. 注册 inputs 到 driver（用现有 `read_paths` 机制）
2. 把 prompt 里的 `{input:<name>}` / `{output:<name>}` 占位符替换成 slave-side scratch 路径（每个 task 唯一，库自动分配）
3. submit_task 带 `read_paths` + `write_paths`
4. wait 到 terminal 后自动 PUT-back 写入 outputs 本地路径
5. `res.outputs["report"]` = `'./report.md'`

**用户全程不接触 slave 文件系统拓扑**。没有路径冲突——每个 task 拿到的 slave 路径都是唯一 scratch dir。

### 3. Transport（`client.py`）

```python
class _DriverClient:
    """Singleton-per-Python-process. 第一次调用时 spawn driver-agent serve-mcp,
    后续 call 复用进程 (stdin/stdout JSON-RPC). atexit 钩子 graceful kill."""
    def __init__(self, bin_path: str | None = None, cfg_path: str | None = None): ...
    def call(self, tool: str, args: dict, *, timeout: float = 600) -> dict: ...
```

关键决定：

- **持久化进程**而非每 call 重 spawn —— 摊薄 200ms cold-start
- 同一 Python 进程内单例；不跨进程；多 Python 进程并行各自起一份 driver
- atexit hook 进程退出时 SIGTERM driver，强 kill 兜底
- JSON-RPC error / 进程崩溃 → 抛 `loom.DriverUnavailable`；上层捕获后可以 `loom.client.restart()` 重起
- driver-agent 二进制定位顺序：`LOOM_DRIVER_BIN` env → `$(which driver-agent)` → repo-local `tests/prod_test/bin/`；找不到给清晰报错带安装提示

### 4. 错误类型

```python
loom.LoomError                # 基类
├── loom.DriverUnavailable    # driver-agent 起不来 / JSON-RPC 失败
├── loom.TaskFailed           # task 终态 failed,带 failure_reason
├── loom.TaskCancelled        # 任务被取消
├── loom.SessionLost          # resume_task 时 backend session 不在了
├── loom.AcceptanceFailed     # scaffold_and_register 时 acceptance gate 不通过
├── loom.SlaveNotFound        # find_slave 没找到
├── loom.AmbiguousTarget      # find_slave 找到多个,需手动选
└── loom.NoInteractiveHandler # expect_or_ask 默认 handler 但 stdin 不是 TTY
```

所有异常带 `task_id` / `slave_display_name` / 原始 driver response 等结构化信息便于排查。

### 5. 测试策略

**三层**：

1. **单元测试**（`tests/unit/`，无 driver 依赖）
   - `client.py` 用 fake stdio（`io.StringIO` 喂 mock JSON-RPC response）
   - `workflow.py` 测 fluent API 链式行为、错误传播
   - `humanloop.py` 测 expect_or_ask 循环逻辑（mock awaiting_user 返回 N 次后 final）
   - `files.py` 测 `{input:X}` / `{output:Y}` 占位符替换

2. **集成测试**（`tests/integration/`，需真 driver-agent binary 但用 mock slave）
   - 起真 driver-agent，target 是本地起的 fake agent
   - 验证 JSON-RPC 实际 round-trip

3. **e2e**（`tests/e2e/`，需 prod_test fleet）
   - 复用 `tests/humanloop_e2e/scripts/lib.py` 的 fleet（slave-local-prod docker）
   - 用 Python 库重写 e2e case 1+2+4+5（happy / ask_user / request_permission / jetson）—— 4 个 case 跑通即证明库 API 覆盖现有 e2e 形态

**pytest fixtures**：
- `driver_client` fixture：spawn driver-agent，session-scope
- `prod_test_slave` fixture：assert slave-local-prod 在 list_agents 里
- 跳过逻辑：没有 driver binary → skip integration & e2e

### 6. Envelope 决策（v0 暂不做的依据）

**v0 不编译 TASK_CONTRACT envelope**。Python 用户写的 `prompt` 一字不差走 driver MCP `submit_task.prompt`。

**理由**：Python 用变量传递就能搞定 task 间数据流；slave 模型看 goal 用 prompt 手贴；dry-run 校验留给 v0.2。

| 共享什么 | Python v0 能不能做？ | envelope 加的是啥 |
|---|---|---|
| 任务间数据流（slave A 输出当 slave B 输入）| ✅ `f"... {res1.output} ..."` | 无差异 |
| 同 slave 上 chat 连续上下文 | ✅ `session_id` 传给下一个 `wf.chat(..., resume_session=sid)`，底层走 `chat_resume` | 无差异 |
| slave 模型"知道整体 goal" | △ 手动每个 prompt 顶部贴"本任务总目标是 X" | envelope 标准化 |
| driver dry-run 校验 target 能力是否够 | × | envelope 是它的入口 |
| 跨任务结构化重放/调试 | △ 用户自写 log | envelope 给统一格式 |

**触发评估升级的信号**：
1. `wf.chat()` 的 prompt 里反复出现"This is part of <goal>"样板代码
2. 用户提报"提交后才发现 target 缺工具"超过 3 次
3. 多人协作场景下有"我想看 6 个月前那次任务到底要做啥"的诉求

任何一条命中，开 v0.2 spec 加 envelope 编译。否则保持不编译。

### 7. v0 范围与后续版本边界

| 能力 | v0 | v0.2 | 后续 |
|---|---|---|---|
| 核心任务（submit/wait/get/cancel）| ✅ | | |
| Humanloop pause/resume | ✅ | | |
| 能力发现 + 动态 MCP | ✅ | | |
| 文件 I/O (inputs/outputs) | ✅ | | |
| Workflow 上下文 / fluent API | ✅ | | |
| Envelope 编译 | ❌ | 评估后 | |
| Fanout / DAG | ❌ | ✅ | |
| Codex backend 差异处理 | ❌ | ✅ | |
| Retry / circuit breaker | ❌ | | ✅ |
| Metrics / OTel | ❌ | | ✅ |
| PyPI 发布 | ❌ | | ✅ |

## E2E 验收

按 § 5 测试策略的第 3 层执行。用 Python 库重写以下 4 个 case，全部通过即视为 v0 完成：

| Case | 现有脚本 | Python 库重写后行数（预计）|
|---|---|---|
| Happy chat | `case1_happy.py` | ≤ 15 行 |
| ask_user pause/resume | `case2_ask_user.py` | ≤ 25 行 |
| request_permission | `case4_request_permission.py` | ≤ 25 行 |
| Jetson 跨节点 | `case5_jetson.py` | ≤ 20 行 |

第二目标：在文档里给一个 ≤ 40 行的"capability 演化"完整 demo（scaffold + register + reuse），证明 Cycle C 也能用库表达。

## 配置 / 知识 / 提示

- driver-agent 需先 `register --config <cfg>` 完成 OAuth；这一步由人手做（不在库范围）
- prod_test fleet 的运维（slave 重授权、container 起来）也不在库范围；库假设 fleet 已就绪
- 用户的 `~/.loom/python-history.jsonl` 是库自己写的本地审计日志（每次 `wf.chat()` 一行），完全可关闭（构造 Workflow 时 `history=False`）

## 开放问题

- **library 名字**：`loom`（与 Go module 一致）vs `loom_py`（避免与未来可能的 npm 同名包冲突）。倾向 `loom`，import 名为 `loom`，pip 包名 `loom-py`，类似 `pyyaml` / `import yaml` 的模式。
- **driver-agent 复用进程的并发安全**：v0 假定 Python 单线程串行 call；多线程并发由 v0.2 处理（加 mutex 或换异步 JSON-RPC）。文档明确写。
- **`Workflow.__exit__` 失败时是否要 cancel 已 in-flight 的 task**：v0 选**不取消**（保留资源给用户事后调试）；可通过 `loom.workflow(..., cancel_on_exit=True)` 显式启用。
