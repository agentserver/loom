# Backend.ListSessions / GetSession — 设计文档

- **日期**：2026-06-14
- **范围**：`pkg/agentbackend.Backend` 接口扩 `ListSessions` + `GetSession`；claude / codex / opencode 三个 backend 各自实现，读自己的 session 持久化目录
- **上层 context**：[[docs/vision/commander-web-entry.md]] 第 4 节 PR-1 —— 是「星池指挥官 web entry」3 PR 蓝图的第一块拼图
- **不在范围**：
  - daemon 模式（PR-2）
  - observer reverse proxy + WS hub（PR-3）
  - web UI（PR-3）
  - session 写入 / 创建 API（用 Backend 现有 `Run` / `RunResume`）
  - master 路径（[[master_path_frozen]]）
  - 跨 backend session 迁移 / 合并

## 背景

vision doc 第 3.3 节定下了 **session 权威源 = backend CLI 自己的持久化**。Daemon / observer / web 全部按需调 backend 拉数据，不复制存储。为此 Backend 接口需要新增 read-side API：枚举 + 读单个 session。

为什么放到 `pkg/agentbackend` 层而不是 daemon 层去扫文件夹：
1. 每个 backend 的 session schema / 文件位置不同，封装在该 backend 包内最自然
2. daemon 只需要面向统一 interface，不关心后端是 claude 还是 codex
3. 测试可在 backend 层用 fixture 文件覆盖，不依赖 daemon
4. 未来 PR-2 daemon 直接调 `b.ListSessions()` 一行，daemon 自身代码极简

## 目标不变量

1. **接口契约**：`Backend` interface 加 `ListSessions(ctx)` + `GetSession(ctx, id)`，签名稳定
2. **零网络**：实现只读本地文件系统，不调任何外部服务（包括 backend CLI 自身的子进程）
3. **空目录耐受**：session 目录不存在时返回空列表 + nil err
4. **不可解析忽略**：单个 session 文件损坏不导致整个 `ListSessions` 失败 —— 跳过该条目，可选记 warning
5. **`GetSession` 错误明确**：找不到 id 时返回明确的 `ErrSessionNotFound`，调用方能区分「不存在」vs「读失败」
6. **不暴露绝对路径**：返回的 `Session.WorkingDir` 是 session 本身关联的 cwd（如 backend 记录），不是 session 文件的磁盘路径
7. **不重命名现有接口方法**：`Run` / `RunResume` / `LLM` / `Permissions` / `Detect` 一字不改

## 接口设计

### `pkg/agentbackend/backend.go` 新增

```go
// Session is a backend-agnostic descriptor of a conversation thread
// persisted by an agent CLI (claude / codex / opencode). Authoritative
// storage lives in the backend's own files; this struct is the
// interchange shape consumed by daemon / web layers via
// Backend.ListSessions / GetSession.
//
// Fixes the daemon's need (vision doc commander-web-entry.md §3.3) to
// enumerate persisted conversations without owning a separate session
// store.
type Session struct {
    // ID is the backend-native session identifier (claude session uuid,
    // codex thread id, opencode session id). Stable; used by RunResume.
    ID string

    // Kind is the backend that owns this session (mirrors Backend.Kind()).
    Kind Kind

    // WorkingDir is the cwd the session was originally created with,
    // as recorded by the backend itself. May be empty when the backend
    // doesn't record it; callers should not depend on it being present.
    WorkingDir string

    // StartedAt is when the first message in the session was recorded.
    // Zero value means unknown.
    StartedAt time.Time

    // UpdatedAt is when the most recent message was appended.
    // Zero value means unknown.
    UpdatedAt time.Time

    // MessageCount is the total messages in the session (any role).
    MessageCount int

    // Preview is a short snippet from the most recent assistant message,
    // suitable for a session-list UI. Empty when no assistant message
    // has been written. Length-capped at sessionPreviewMaxBytes.
    Preview string
}

const sessionPreviewMaxBytes = 256

// SessionMessage is one turn in a session. Roles map to claude / codex /
// opencode conventions: "user", "assistant", "system", "tool".
type SessionMessage struct {
    Role string
    Text string
    Ts   time.Time
}

// ErrSessionNotFound signals GetSession was called with an id that does
// not exist in this backend's persistence. Distinct from other read
// errors so callers can return a 404 cleanly.
var ErrSessionNotFound = errors.New("agentbackend: session not found")
```

### `Backend` interface 扩 2 个方法

```go
type Backend interface {
    Kind() Kind
    Run(ctx context.Context, t Task, sink Sink) (Result, error)
    RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
    LLM() LLMRunner
    Permissions() PermissionsStore
    Detect(ctx context.Context) error

    // ListSessions returns descriptors for every session this backend
    // has persisted on disk. Empty list (with nil error) when the
    // backend has no session storage directory or it is empty.
    // Implementations must not shell out to the backend CLI.
    // Individual unparseable session entries are skipped silently;
    // a hard error is returned only when the storage location itself
    // can't be read (e.g. permission denied on a directory we expected).
    ListSessions(ctx context.Context) ([]Session, error)

    // GetSession returns the descriptor plus full message history of one
    // session. Returns ErrSessionNotFound when id is unknown to this
    // backend. Like ListSessions, no subprocess invocation.
    GetSession(ctx context.Context, id string) (Session, []SessionMessage, error)
}
```

## 每 backend 的实现摘要

### claude (`pkg/agentbackend/claude/sessions.go`)

- **存储位置**：`~/.claude/projects/<encoded_cwd>/<session_uuid>.jsonl`
  - `<encoded_cwd>` = cwd 路径每个 `/` 替换成 `-`（claude 的约定）
  - 每个文件 = 一个 session，每行是 JSON 对象代表一条 message / event
- **List**：遍历 `~/.claude/projects/` 下所有目录 + 所有 `.jsonl`
- **Get**：拼路径 + 流式扫描所有行 + 解码

Schema 调研已知（claude code 0.5.x）：
```json
{"type":"user","timestamp":"2026-06-14T...","message":{"role":"user","content":"..."}}
{"type":"assistant","timestamp":"...","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
{"type":"summary","summary":"..."}
{"type":"tool_use","timestamp":"...","tool":"...","input":{...}}
```

Implementer **必须先 probe** 本机一个真 claude session 文件确认 schema 细节（特别是 timestamp 字段名 / content 是 string 还是 array），spec 写的是 0.5.x best-effort。

### codex (`pkg/agentbackend/codex/sessions.go`)

- **存储位置**：implementer 调研（候选：`~/.codex/threads/`、`~/.codex/sessions/`、sqlite db）
- 已知 codex 用 `thread_id` 概念，与 session 等价
- PR #18 e2e 时 e2e workdir 里有 `data.db`，可能是 codex 的本地 store —— 调研时确认

Implementer 调研步骤（PR-1 必做）：
```bash
# Probe codex session storage:
ls -la ~/.codex/ 2>/dev/null
codex --help 2>&1 | grep -i session
codex session list 2>&1 | head  # 如有 list 子命令直接读
find ~/.codex/ -name "*.json*" 2>/dev/null | head
```

把发现写进 `pkg/agentbackend/codex/sessions.go` 顶部 doc comment 作为可考据来源。

### opencode (`pkg/agentbackend/opencode/sessions.go`)

- **存储位置**：candidate `~/.local/share/opencode/sessions/` 或 `~/.local/share/opencode/messages/`
- opencode 有 `opencode session list / delete` 子命令（cf. CLI doc），意味着 session 是 first-class 概念，必有持久化
- Implementer 调研步骤：
  ```bash
  ls -la ~/.local/share/opencode/
  opencode session list
  opencode db path  # opencode 提供 db 路径子命令
  ```

opencode 较新（PR #19 引入），schema 可能稳定也可能波动；implementer 在 fixture 里用 captured sample 写测试。

## 测试策略

**关键**：每 backend 必须能离线测试 —— **不调真 claude/codex/opencode CLI**。靠 fixture session 文件 + 临时 HOME。

### Fixture 文件

每 backend 在 `pkg/agentbackend/<kind>/testdata/sessions/` 放 1-3 个 sample session 文件：
- 1 个 happy 完整 session
- 1 个 损坏 / 无法 parse 的（验证 ListSessions skip 不 fail）
- 1 个 空消息历史的 session

### 测试矩阵（每 backend 都要）

| Test | 覆盖 |
|---|---|
| `TestListSessions_EmptyDir` | 目录不存在 → 空列表 + nil |
| `TestListSessions_ReturnsKnownSessions` | fixture 目录 → 返回 N 条 + 正确 ID/Preview |
| `TestListSessions_SkipsCorruptFiles` | 故意损坏一个 → 其他 N-1 条仍返回 |
| `TestGetSession_ReturnsMessages` | 已知 id → 描述符 + 完整消息历史 |
| `TestGetSession_UnknownIDReturnsErrSessionNotFound` | 未知 id → `errors.Is(err, agentbackend.ErrSessionNotFound)` |
| `TestGetSession_RespectsPreviewCap` | preview 字段不超 `sessionPreviewMaxBytes` |

测试通过 `HOME` env 重定向到 `t.TempDir()`，把 fixture 拷过去。

### 集成层

`pkg/agentbackend/sessions_test.go`（顶层包测试）跑：
- `agentbackend.RegisteredKinds()` 后枚举每个 builder
- 用 fresh tempdir HOME → ListSessions 返回 nil/empty（所有 backend 必须空目录耐受）

## 兼容性

| 变更 | 影响 |
|---|---|
| `Backend` interface 加 2 个方法 | **break**：所有 `Backend` 实现必须加这两方法。当前实现仅 claude/codex/opencode 3 个，本 PR 一并加。下游测试中如有 `mock Backend` 也要补（grep `interface satisfaction` 找出来） |
| 加 `Session` / `SessionMessage` / `ErrSessionNotFound` 三个 export | additive，无影响 |
| 不动其他方法签名 | 现有调用方零影响 |

潜在 mock backends（implementer grep 验证）：
- `pkg/agentbackend/backend_test.go` 的 `nilBackend` —— 要补两个空实现
- `internal/journal/journal_test.go` 的 `fakeLLM` —— 不是 Backend，无影响
- `internal/planner/planner_test.go` 的 `fakeLLM` —— 同上
- 其他地方 grep `agentbackend.Backend` 看是否有 type assertion 或满足检查

## 决策记录

| 决策 | 备选 | 选择理由 |
|---|---|---|
| 加在 `Backend` interface 里 | 单独定义 `SessionLister` 可选接口 + type assert | daemon 总要查 3 backend；让 3 backend 都强制实现避免 daemon 写 type-switch；接口加 2 方法成本低 |
| `Session` 字段最小（无 model、无 tool calls） | 完整 schema | v1 web UI 只要列表 + 末条预览；详细 schema 留给后续 backend-specific 扩展 |
| 用 `time.Time` 而非 unix nano | int64 | 调用方少 1 层转换；Go idiomatic |
| `ErrSessionNotFound` 用 sentinel error | typed error | sentinel + `errors.Is` 是 Go 现行习惯；少一层抽象 |
| Preview 用 `sessionPreviewMaxBytes = 256` | 让调用方截 | 截一次 vs 重复截；256 字节足以 UI 显示一行 |
| `ListSessions` skip 损坏，`GetSession` 报错 | 统一报错 / 统一 skip | List 是 batch 操作，单个坏不应炸；Get 是单个，找不到必须明确告诉调用方 |
| 不调 backend CLI 的 `session list` 子命令 | 走 CLI | 启动开销大 + 返回 schema 不稳；直接读文件最快最稳 |
| Fixture 文件落 testdata/ | 在测试代码里 inline | fixture 反映真实 schema 更好；inline 易漂移 |

## 反目标 / 反范围

- 不写 daemon（PR-2）
- 不写 observer changes（PR-3）
- 不实现 session 删除 / 重命名（read-only v1）
- 不持久化任何 Session 数据到本仓自己的存储
- 不调 backend CLI 子进程
- 不引入新二进制依赖（全 stdlib + 已有 yaml/json 库）
- 不修改 `Run` / `RunResume` 的签名
- 不动 master 路径

## Implementer pre-flight checklist

PR-1 implementer 必须在写代码前完成（写进 PR 描述）：

1. **本机 probe claude session 目录** —— `ls ~/.claude/projects/`，挑一个 `.jsonl` 看前 5 行，记入 sessions.go 顶部 comment
2. **本机 probe codex session 存储** —— 跑 `codex --help` / `codex session --help` / `ls ~/.codex/`；找到具体位置 + 文件格式
3. **本机 probe opencode session 存储** —— 跑 `opencode session list`、`opencode db path`；找到位置 + 格式
4. **grep mock backends 是否需要补**：
   ```bash
   grep -rn "agentbackend\.Backend\b" --include="*.go" | grep -v _test.go | grep -v pkg/agentbackend
   grep -rn "func.*Run.*context.*Task.*Sink" --include="*_test.go" | head
   ```

完成上述后，把发现写进 PR description「Pre-flight findings」section。

## 风险

1. **claude / codex / opencode 升级会改 session 格式** —— `Run / RunResume` 已经面临过这个风险（PR #19 opencode event schema 从 source code 读的）；session 解析也同款。Mitigation：在 sessions.go 顶部 doc comment 引用具体 backend 版本 + source 来源，方便未来维护对照
2. **opencode session 可能是 sqlite db 而非 jsonl** —— implementer 用 readonly sqlite3 库读，或者通过 `opencode db path` 找到再用文件系统读。两种实现复杂度差不多
3. **codex sessions 可能不存在专门的「list」概念** —— 如调研发现 codex 没有 session 列表（每次 run 都是 new thread），ListSessions 返回空 + nil；GetSession 永远返 ErrSessionNotFound。Implementer 在 commit message 写明 + 留 TODO 等 codex 推出 session 列表概念时再补
