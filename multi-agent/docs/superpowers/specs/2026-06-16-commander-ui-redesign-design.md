# Commander 三栏工作台 UI redesign — 设计文档

- **日期**: 2026-06-16
- **状态**: design spec, 待 writing-plans 转成实施计划
- **来源**: `/commander` 现状、`docs/vision/commander-web-entry.md`、`docs/superpowers/specs/2026-06-15-commander-observer-hub-design.md`
- **设计选择**: 方案 B — 前后端一起引入 Commander view model, React/Vite 前端由 Go embed 服务

## 背景

当前 `/commander` 是 `internal/commanderhub/assets/app.js` + `style.css` 的 vanilla JS 页面。它已经具备 device-flow 登录、daemon 列表、session 列表、历史消息和 `session_turn` SSE, 但仍像 debug UI:

- daemon/session/chat 以跳页方式切换, 点开 session 后容易失去列表上下文。
- session 标题显示 session id + preview, 不符合用户用 prompt 识别任务的方式。
- turn 状态目前混在 assistant 文本中, 会污染 agent 内容。
- session list 每次都请求 daemon 深扫, 用户感知卡顿。
- 没有 cwd 文件树、文件预览、active worker 预留位、审批状态的统一展示。

本 redesign 的目标是把 `/commander` 从 debug 页面升级成现代化 agent workbench: 左侧 daemon/session tree, 中间 chat, 右侧当前 session cwd 文件树和只读预览。

## 参考项目

已 clone 到 `/root/pr_review` 并抽取设计参考:

- **OpenHands** (`/root/pr_review/openhands`): agent workbench 三栏感、运行/审批状态在上下文内可见、chat 与文件/工具区域并列。
- **Open WebUI** (`/root/pr_review/open-webui`): chat message 密度、composer 状态、Markdown/code block 渲染和滚动体验。
- **LibreChat** (`/root/pr_review/LibreChat`): 统一 sidebar、可折叠/可调整侧栏、消息渲染层与 shell 分离。
- **Lobe Chat** (`/root/pr_review/lobe-chat`): explorer/tree 的视觉细节、badge polish、任务/agent 状态展示。

本项目不是照搬聊天产品。Commander 是工程工作台: 安静、紧凑、可扫描, 不做 landing/hero/营销式布局。

## 范围

### 本次包含

- `/commander` 前端迁移为 React + Vite, 构建产物由 Go `embed` 服务。
- 三栏工作台:
  - 左栏: 按 daemon/设备分组的 session tree。
  - 中间: 当前 session 的 chat workspace。
  - 右栏: 当前 session `WorkingDir` 为根的文件树与只读文件预览。
- session 标题取第一条 user prompt, 兜底 preview, 再兜底短 session id。
- sessions 按 `UpdatedAt desc` 排序。
- turn 状态、active worker、awaiting approval 在左栏/header/composer 附近展示。
- Codex 格式第一版重点支持 Markdown/code fence 渲染; Claude/opencode 降级为安全纯文本/Markdown。
- observer 侧 per-daemon session list cache, 减少列表卡顿。
- daemon 文件浏览命令: 目录懒加载 + 2MB 内只读预览。

### 本次不包含

- web 端审批执行 UI。第一版只显示 `awaiting_approval`, 并禁用发送。
- active worker 缓存池实现。UI/API 预留 `active_worker`, 初始可为 false; 后续 worker pool 接入后复用同一字段。
- 文件编辑、上传、删除、重命名、diff。
- observer 持久化 Commander DB。session 权威源仍是 daemon 所在机器上的 backend session storage。
- 重写 `serve-mcp`、master/orchestrator 路径。

## 方案取舍

### A. 只重做前端, API 尽量不动

最快, 但 session title、排序、active 状态、文件树都要靠前端拼, list session 卡顿也难以根治。

### B. 前后端一起做 Commander view model

**选择 B。** observer/daemon 明确返回 UI 需要的数据: daemon 分组、session title、排序、turn state、file tree/preview。React 只负责展示和交互, 后续 worker pool、审批 UI 也能接在同一模型上。

### C. 引入 observer 持久化数据库

功能最强, 但范围过大, 且会复制 backend session 权威状态。v1 不做。

## 信息架构

### 左栏: daemon/session tree

一级节点是 daemon/设备, 二级节点是该 daemon 的 sessions。session 必须始终用 `(daemon_id, session_id)` 定位, 因为 session 文件、cwd 文件树、恢复 turn 的 agent 进程都在对应 daemon 所在机器上, `session_id` 不应被当作全局唯一。

daemon 行显示:

- `display_name` 或 daemon id
- backend kind (`codex` / `claude` / `opencode`)
- driver version
- online/last_seen 状态
- session 数、active 数、in-flight turn 数

展开规则:

- 默认展开当前 daemon。
- 默认展开存在 `answering` / `awaiting_approval` / `error` session 的 daemon。
- 其他 daemon 折叠成一行, 显示状态和计数。

session 行显示:

- title: 第一条 user prompt, 单行截断。
- cwd 尾部: 例如 `.../tests/prod_test/driver-codex`。
- `UpdatedAt` 相对时间。
- `MessageCount`。
- badges: `active`, `queued`, `starting`, `answering`, `awaiting_approval`, `error`, `idle`。

session 排序统一按 `UpdatedAt desc`; 前端只保持稳定排序, 不重新解释业务规则。

### 中间: chat workspace

chat header 显示 session title、daemon、kind、cwd、turn status。主体渲染历史消息。composer 位于底部。

**不变量: UI 状态和 agent 内容必须分离。**

- `turn_state` 显示在 chat header 右侧和 composer 上方的细状态条。
- assistant message 只显示 Codex/agent 实际输出。
- `Codex 正在回答`、`已回答完毕`、`需人工审批` 不写进 message 正文。
- 如果 turn 已开始但还没有 chunk, assistant draft 气泡只显示 skeleton/typing dots, 不显示状态文字。

composer 禁用规则:

- `queued` / `starting` / `answering` / `awaiting_approval` 时禁用。
- 同一 `(daemon_id, session_id)` 已有 in-flight turn 时, 重复发送被拒绝。
- 第一版不做 cancel/stop。

### 右栏: cwd 文件树和只读预览

右栏根固定为当前 session `WorkingDir`。目录懒加载, 不递归拉全树。切换 session 时清空旧 preview 并加载新 root。

文件预览:

- 点击文件后请求 daemon 读取内容。
- 只读。
- 单文件上限 **2MB**。
- 超过 2MB 返回 metadata, UI 显示不可预览。
- 二进制文件不显示正文, 只显示类型和大小。
- 文本文件按语言高亮或等宽文本展示。

路径安全:

- browser 和 observer 只传相对 path。
- daemon 用 session `WorkingDir` + path 解析, `filepath.Clean` 后必须仍在 root 内。
- path traversal 返回 400, 不暴露目标机器的敏感路径细节。

## API 与协议

### HTTP view model

#### `GET /api/commander/daemons`

返回在线 daemons 与 observer 已知状态汇总:

```json
{
  "daemons": [
    {
      "daemon_id": "abc",
      "display_name": "prod-codex",
      "kind": "codex",
      "driver_version": "v0.1.0",
      "last_seen_at": "2026-06-16T12:00:00Z",
      "session_count": 42,
      "active_count": 2,
      "turn_count": 1
    }
  ]
}
```

#### `GET /api/commander/tree`

聚合当前 owner 的所有 daemon sessions, 返回左栏模型:

```json
{
  "daemons": [
    {
      "daemon_id": "abc",
      "display_name": "prod-codex",
      "kind": "codex",
      "status": "ok",
      "sessions": [
        {
          "daemon_id": "abc",
          "session_id": "019e21d8-...",
          "title": "Fix commander session cache latency",
          "working_dir": "/root/multi-agent/multi-agent/tests/prod_test/driver-codex",
          "updated_at": "2026-06-16T12:00:00Z",
          "message_count": 18,
          "preview": "I will add a per-daemon cache...",
          "turn_state": "answering",
          "active_worker": false,
          "awaiting_approval": false
        }
      ]
    }
  ]
}
```

observer 负责:

- fan-out 到 daemons。
- per-daemon 短 TTL cache。
- 合成 `title`。
- 合并 observer 内存中的 `turn_state` / `active_worker` / `awaiting_approval`。
- 排序。

#### `GET /api/commander/daemons/{daemon_id}/sessions/{session_id}`

返回 session descriptor + messages。descriptor 使用与 tree row 相同的 enriched fields。打开 session 时以此为准, 并可回写 observer cache 中的 title。

#### `POST /api/commander/daemons/{daemon_id}/sessions/{session_id}/turn`

继续使用 POST + SSE。SSE 事件:

- `status`: machine-readable state, 如 `queued` / `starting` / `answering`。
- `chunk`: agent 实际输出增量。
- `done`: result, 包含 `summary`、`session_id`、`awaiting_user`。
- `error`: code + message。

observer 在 SSE 生命周期中更新 `(owner, daemon_id, session_id)` 的 turn state:

```text
idle -> queued -> starting -> answering -> done
                                  \-> awaiting_approval
                                  \-> error
```

同一 session 并发 turn:

- 如果已经 in-flight, HTTP 层返回 409 或 SSE `error`。
- 前端同时禁用 composer, 正常路径不应触发第二个请求。

#### `GET /api/commander/daemons/{daemon_id}/sessions/{session_id}/files?path=.`

列目录, 返回目录 children:

```json
{
  "root": "/root/project",
  "path": ".",
  "entries": [
    {"name": "internal", "path": "internal", "kind": "dir"},
    {"name": "go.mod", "path": "go.mod", "kind": "file", "size": 1234}
  ]
}
```

#### `GET /api/commander/daemons/{daemon_id}/sessions/{session_id}/files/content?path=go.mod`

只读预览:

```json
{
  "path": "go.mod",
  "size": 1234,
  "mime": "text/plain; charset=utf-8",
  "binary": false,
  "too_large": false,
  "content": "module github.com/..."
}
```

超过 2MB:

```json
{
  "path": "large.log",
  "size": 4123456,
  "too_large": true
}
```

### daemon WS command additions

新增可选命令:

- `list_files`
- `read_file`

它们是 additive capability, **不 bump `commander.SchemaVersion`**。原因: 当前 schema mismatch 会导致 observer 直接拒绝老 daemon, 无法实现文件树降级。新增命令通过 register capabilities 表达:

```json
{
  "type": "register",
  "payload": {
    "schema_version": 1,
    "kind": "codex",
    "display_name": "prod-codex",
    "capabilities": ["sessions", "turn", "files"]
  }
}
```

兼容规则:

- 没有 `files` capability 的 daemon 仍可连接。
- UI 对该 daemon/session 隐藏文件树或显示 daemon 需要升级。
- observer 不向无 `files` capability 的 daemon 发送 file commands。

## 状态模型

`turn_state` 是 observer 已知实时状态:

- `idle`
- `queued`
- `starting`
- `answering`
- `done`
- `error`
- `awaiting_approval`
- `disconnected`

`active_worker` 是为后续 worker pool 预留的布尔字段。第一版没有 worker pool 时默认 false。后续最多缓存 10 个 session worker 的能力接入后, 同一字段变为真实 active badge。

`awaiting_approval` 来自 `command_result.result.awaiting_user != nil`。第一版只展示, 不在 web 端审批。

daemon disconnect:

- registry 移除 daemon。
- in-flight turn 置为 `disconnected` 或 `error`。
- 左栏对应 daemon 消失或变为离线, 不继续显示 stale online。
- 当前打开的 session 显示 daemon disconnected, composer 禁用。

## 缓存策略

session list 卡顿用两层缓存:

1. daemon 侧已有 `internal/sessioncache.FileCache`: 文件 path + size + mtime 未变时复用 session descriptor。
2. observer 侧新增 per-daemon cache: key 为 `(owner, daemon_id)`, TTL 5-10 秒。

交互策略:

- 打开 Commander 时先渲染 observer cache, 后台刷新。
- 用户切 session 不触发全量 tree 重扫。
- turn 完成后只 invalidate 对应 daemon cache。
- session detail 打开后可用完整 messages 修正 title 并回写 observer cache。

## 前端实现结构

新增前端目录建议:

```text
internal/commanderhub/webapp/
  package.json
  vite.config.ts
  index.html
  src/
    CommanderApp.tsx
    api/
    components/
      DaemonSessionTree.tsx
      ChatWorkspace.tsx
      MessageRenderer.tsx
      Composer.tsx
      FileExplorerPanel.tsx
      StatusBadge.tsx
    state/
      commanderStore.ts
```

构建输出建议:

```text
internal/commanderhub/assets/dist/
```

`web.go` 改为 embed `assets/dist/*`, 同时保持 `/commander` 返回 SPA index。`dist` 作为 build artifact 提交进仓库, 让普通 `go test ./...` 和 Go binary build 不需要先安装 Node。CI 仍要运行 `npm ci && npm run build`, 并检查 build 后 `assets/dist` 没有未提交 diff, 防止 source 与 embed artifact 漂移。Go tests 需要验证 index 和静态 assets 可服务。

依赖选择:

- React + Vite + TypeScript。
- Markdown renderer 使用安全配置, 禁止 raw HTML。
- icons 使用 lucide-react。
- 代码高亮可先用轻量 highlighter; 若依赖成本过高, 第一版用 styled `<pre><code>`。

组件职责:

- `CommanderApp`: auth state, selected daemon/session, app shell。
- `DaemonSessionTree`: daemon grouping, session rows, refresh, badges。
- `ChatWorkspace`: session detail fetch, message list, SSE turn flow。
- `MessageRenderer`: Codex Markdown/code fence 渲染, 纯文本降级。
- `Composer`: textarea, disabled state, send action。
- `FileExplorerPanel`: lazy directory tree, selected file preview。
- `commanderStore`: normalized daemons/sessions/turn state, 保证左栏和 chat header 同步。

## 视觉规则

- 整体是浅色工程工作台, neutral 基底。
- active 使用 teal/blue, approval 使用 amber, error 使用 red, online 使用 green。
- 左栏密度高但行高固定, 长标题/长 cwd 必须截断。
- 中间 chat 保持较舒展的阅读宽度。
- 右栏文件树紧凑, 不用嵌套卡片。
- 不使用 hero、营销式大卡片、纯渐变背景、装饰 orb。
- badges 用短词和图标, 不用大段说明。
- 桌面默认三栏; 窄屏时左栏/右栏可折叠, 中间 chat 优先。

## 错误处理

- 未登录: 保持现有 device-flow login, token 不进 JS/localStorage。
- daemon offline: 左栏刷新后移除或标离线; 当前 session 显示 disconnected。
- session not found: 从左栏移除该 row, chat 显示 session not found。
- list_sessions timeout: daemon row 显示 timeout, 不阻塞其他 daemon。
- turn 409: composer 保持 disabled, 状态显示已有 turn 在运行。
- file cwd unknown: 右栏显示文件树不可用。
- file path traversal: 400, UI 显示无法打开。
- file too large: 显示 size 和 2MB 限制。
- binary file: 显示 mime/size, 不渲染正文。

## 测试策略

### Go

- `commanderhub` API:
  - `/tree` 按 daemon 分组。
  - sessions 按 `UpdatedAt desc`。
  - title fallback 顺序: first user prompt -> preview -> short id。
  - turn state 更新和断连处理。
  - 同 session 并发 turn 返回 409。
- WS command:
  - register capabilities 解析。
  - 无 `files` capability 时不发送 file commands。
  - `list_files` / `read_file` round trip。
- daemon file commands:
  - root sandbox。
  - directory lazy list。
  - 2MB 限制。
  - binary detection。
  - unknown cwd/session errors。
- cache:
  - observer per-daemon cache hit。
  - turn done invalidates one daemon。
  - daemon disconnect clears stale state。

### Frontend

- component tests:
  - daemon grouping and default expand rules。
  - status badge rendering。
  - composer disabled while in-flight / awaiting approval。
  - turn status is outside message body。
  - file too-large/binary/preview states。
- Playwright/screenshot:
  - desktop three-pane layout。
  - narrow viewport with collapsible sidebars。
  - long title/cwd/code block no overflow。
  - selected session remains selected after refresh route restore。
- build:
  - `npm test` / `npm run build`。
  - Go embed serving `/commander` index and assets。

## Risks

- React/Vite adds Node build requirements to CI. Mitigation: keep frontend under `internal/commanderhub/webapp`, commit the generated `assets/dist`, and make CI verify `npm run build` leaves the committed artifact unchanged.
- Markdown/code rendering can introduce XSS if raw HTML is allowed. Mitigation: disable raw HTML and render untrusted text safely.
- observer cache can show stale sessions briefly. Mitigation: stale-while-refresh UI, turn completion invalidation, and session detail fetch as source of truth when opened.
- old daemon without `files` capability cannot serve file tree. Mitigation: capability-gated UI degradation, no schema bump.
- active worker field is initially mostly false. Mitigation: label it as worker presence, not turn state; turn state is already accurate in v1.

## Acceptance Criteria

- User can open `/commander`, see all online daemons grouped on the left, and expand daemon sessions sorted by latest update time.
- Session rows use first user prompt as title when available.
- Selecting a session keeps the left tree visible, renders history in the center, and shows cwd file tree on the right.
- Sending a turn disables composer until terminal state.
- Running/done/approval/error status appears outside assistant message content.
- Codex messages render Markdown/code fences safely.
- File preview works for text files up to 2MB and refuses oversized/binary content cleanly.
- Session list navigation uses observer cache and does not deep-scan on every click.
- Tests cover backend view model, file sandboxing, turn state, and frontend status/message separation.
