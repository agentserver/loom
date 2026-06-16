# observer /commander(WS hub + reverse proxy + UI)— 设计文档

- **日期**:2026-06-15
- **来源**:`docs/vision/commander-web-entry.md` PR-3 段;协议契约继承 `docs/superpowers/specs/2026-06-15-driver-daemon-design.md`(PR-2 的 WS envelope schema lock)
- **状态**:design spec,待 writing-plans 转成任务级实施计划
- **不在范围**:master 路径(`cmd/master-agent/`、`internal/orchestrator/`、`internal/orchestration/`);**agentserver 改造**(只复用其现有 device-code OAuth);**daemon 侧任何改动**(PR-2 已实现且为冻结契约);web humanloop 审批 UI;daemon session 持久化

## 背景

commander-web-entry 的 3-PR 蓝图:PR-1 给 `agentbackend.Backend` 加 `ListSessions/GetSession`(已落);PR-2 给 `driver-agent` 加 `serve-daemon` + 出站 WS + 本机 HTTP debug API(已落,PR #21)。**PR-3 是 observer 侧**:接 daemon 的长连、维护 `(userID, workspaceID) → []daemon` 映射、向网页暴露 reverse proxy API 与 `/commander` 页面(含 device-flow 登录),完成「网页随时给某个长跑 daemon 下指令」的闭环。

现状盘点:

- `internal/observerweb/server.go` 已是全局 HTTP server,带 `identity.Resolver`、`IdentityFromRequest`、`bearerToken`、`guardWebToken` 等鉴权基建;旧的无鉴权 dashboard 已下线。
- `internal/identity`:`Resolver.Resolve(ctx, token) (Identity, error)`,`Identity` 含 `UserID/WorkspaceID/WorkspaceName/AgentID/SandboxID/Role/Source`;`internal/identity/agentserver/resolver.go` 调 agentserver `/api/agent/whoami` 解析 **ProxyToken**。daemon/事件上报走这条;web device-flow 登录不把 OAuth access token 送进 whoami。
- agentserver 的 OAuth **仅 device-code**(RFC 8628):`agentsdk.RequestDeviceCode` + `/oauth2/device/auth` + 轮询 `/oauth2/token` + `/device?user_code=...`;**无 authorization-code / redirect 端点**。这决定了 web 登录只能走 device flow(不能「登录后自动跳回」),见「Web 登录」节。
- daemon(PR-2)行为已冻结:dial `<observer.url>/api/daemon-link` + `Authorization: Bearer <ProxyToken>`;连上立即发 `register`;**必须收到 observer 的 `ack` 才把 `linked` 翻 true**;命令通过 `command`→`command_result`/`event`/`error` 往返;`schema_version=1`;每 `HeartbeatInt` 发一次协议级 ping(gorilla `PingMessage`)。
- 唯一 wiring 点:`cmd/observer-server/main.go` 的 `observerweb.NewWithResolverOptions(...)`。

## 目标不变量

1. **用户 + workspace 隔离**:网页只能枚举/操作「当前 web 登录用户在所属 workspace 下」名下的 daemon —— 即 daemon 的 `ProxyToken` 经 whoami 解出的 `(UserID, WorkspaceID)` 与当前 web device-flow `id_token` claims 解出的 `(UserID, WorkspaceID)` **同时相同**。跨用户、或同用户跨 workspace,既不可见也不可达(找不到 → 404,不泄露存在性)。
   - workspace **由登录凭证隐式决定**(web `id_token` 与 daemon ProxyToken 各自带一个 `WorkspaceID`),v1 不做 UI workspace 切换器;要切 workspace 就重新授权/换 token。
2. **契约复用**:observer 直接 `import "github.com/yourorg/multi-agent/internal/commander"`,复用 `Envelope`/`RegisterPayload`/`CommandPayload`/`SessionTurnArgs`/`EventPayload`/`ErrorPayload`/`SchemaVersion`/`ErrCode*`/`marshalTurnResult`。schema 单一来源,与 PR-2 零偏差。
3. **daemon 零改动**:PR-3 只实现「连接对面」,不动 `internal/commander/` 与 `cmd/driver-agent/`。
4. **只增不改**:不动 master 路径、agentserver、`serve-mcp`、现有事件上报路径(`/api/events` 等)。
5. **web 登录 = device flow + observer cookie**:observer 当 device client 走 agentserver device-code OAuth,成功后颁 **httpOnly** cookie;**token 不进浏览器 JS/localStorage**。`/api/commander/*` 接受 cookie 或 `Bearer`。daemon 侧仍用 Bearer ProxyToken,不变。

## 整体结构

```
浏览器  /commander
   │ ① Cookie: observer 颁的 httpOnly session(device flow 登录后获得;也接受 Bearer)
   ▼
observerweb mux(已有 identity.Resolver)
   ├─ POST /api/commander/login        ─ device flow:返回 verification_uri_complete
   ├─ GET  /api/commander/login/poll   ─ 轮询;成功 → Set-Cookie(httpOnly)
   ├─ POST /api/commander/logout       ─ 清 session
   ├─ GET  /api/commander/daemons                       ─┐
   ├─ GET  /api/commander/sessions                       │  commanderIdentity(r)
   ├─ GET  /api/commander/daemons/{id}/sessions          │   = cookie(session→token) 或 Bearer
   ├─ GET  /api/commander/daemons/{id}/sessions/{sid}    │   → resolver → Identity{UserID, WorkspaceID}
   ├─ POST /api/commander/daemons/{id}/sessions/{sid}/turn (SSE)
   │                                                     ─┘
   ├─ GET  /commander  /commander/app.js  /commander/style.css   (go:embed)
   └─ /api/daemon-link   (gorilla Upgrade, Bearer <ProxyToken> → Identity{UserID, WorkspaceID})
                                  ▼
                          commanderhub.Hub
                                  │  owner{userID,workspaceID} → daemonID → *daemonConn
                                  ▼
                          daemonConn ──WS──▶  driver-agent serve-daemon(PR-2)
```

daemon/事件上报使用同一套 proxy-token 身份体系:`identity.Resolver` 把 Bearer ProxyToken 解析为 `Identity{UserID, WorkspaceID, ...}`。web 登录由 observer 自己完成 device flow,从 token response 的 `id_token` claims 得到 `Identity{UserID, WorkspaceID, ...}` 并缓存到 httpOnly session cookie。故「我的 daemon」= WS 连接 ProxyToken 的 `(UserID, WorkspaceID)` 与当前 web cookie 身份 **同时相同**的连接。

## 文件结构

| 文件 | 状态 | 职责 |
|---|---|---|
| `internal/commanderhub/hub.go` | 新 | `Hub`、gorilla `Upgrader`、accept 循环、register/ack、schema 校验、daemon_id 生成、读循环路由 |
| `internal/commanderhub/registry.go` | 新 | `owner{userID,workspaceID}→daemonID→*daemonConn` 映射;增删查;按 owner 快照;断连清理 |
| `internal/commanderhub/proxy.go` | 新 | `SendCommand`(非流式)、`SendCommandStream`(流式)、`fanOutSessions` 聚合、超时 |
| `internal/commanderhub/sse.go` | 新 | daemon `Envelope` → SSE 行;复用 `commander.EventPayload`/`marshalTurnResult` 形态 |
| `internal/commanderhub/auth.go` | 新 | device flow 驱动(`agentsdk.RequestDeviceCode` + 轮询 `/oauth2/token`)、从 `id_token` claims 建 web Identity、in-memory session 存储(cookie→token+Identity)、`commanderIdentity`(cookie 或 bearer)、login/logout/poll 路由 |
| `internal/commanderhub/web.go` | 新 | `/commander` 页面;`go:embed` 的 `index.html`/`app.js`/`style.css` |
| `internal/commanderhub/*_test.go` | 新 | 单测 + e2e(e2e 复用 `commander.WSClient` 当「真 daemon」;device flow 用假 agentserver) |
| `internal/observerweb/server.go` | 改 | 构造 `Hub` + `Authenticator`、挂路由;复用 `bearerToken` |
| `cmd/observer-server/main.go` | **不变** | `Hub`/`Authenticator` 在 observerweb 内部从 `resolver` + agentserver URL 构造,main 零改动 |

> 放在独立包 `internal/commanderhub/` 而非塞进 `observerweb/server.go`:hub 是有状态的并发子系统(注册表 + 命令关联 + 多 goroutine),独立包便于隔离测试;`server.go` 已 1200+ 行不应再胀。

## 协议契约(observer 视角,复用 PR-2)

握手与帧格式全部沿用 PR-2 锁定的 schema(`docs/superpowers/specs/2026-06-15-driver-daemon-design.md` §WS envelope schema lock)。本节只规定 daemon↔observer 的 observer 侧职责(web↔observer 登录见「Web 登录」节):

1. **HTTP Upgrade 前**:`bearerToken(r.Header.Get("Authorization"))` → `resolver.Resolve(ctx, token)` → 取 `(Identity.UserID, Identity.WorkspaceID)`。任一失败 → **401**(不 upgrade)。→ 满足 PR-2 `TestWSClient_RejectsUnauthorized`(期望 401 且不升级)。
2. **首帧必须是 `register`**:读首帧,反序列化为 `commander.RegisterPayload`。
   - 若 `SchemaVersion != commander.SchemaVersion` → 写 `Envelope{type:error, payload:{code: schema_version_mismatch}}` → `WriteControl(CloseMessage)` 关闭。→ 满足 PR-2「schema mismatch 视为终致命」。
3. **register 通过**:分配 `daemonID`(见下)→ 注册表加入 `(owner{userID,workspaceID}, daemonID)→daemonConn` → 写 `Envelope{type:"ack"}`(无 payload)。→ 满足 PR-2 `TestWSClient_LinkedRequiresObserverAck`。
4. **读循环**:对每个入站 `Envelope`,按 `frame.ID` 路由到 pending 命令:
   - `event` → 非阻塞送入 pending chan;
   - `command_result` / `error` → 送入 pending chan 后删除该 pending(结案);
   - `ack` / `heartbeat` → 忽略(可顺带刷新 `lastSeen`);
   - 未知 `ID` → 忽略 + 日志。
5. **写串行**:每个 `daemonConn` 一个 `writeMu`,所有出站帧(含 pong、命令)走它。满足 gorilla「一连接一读一写」。
6. **存活**:PR-2 daemon 每 `HeartbeatInt` 发协议 ping;observer 用 gorilla 默认 ping handler 自动回 pong,无需自写心跳。读循环返回即视为断连 → 从注册表移除 + 关闭该 conn 全部 pending chan。

### daemon_id 生成

observer 在 accept 时分配:`daemonID = hex.EncodeToString(randBytes(8))`(crypto/rand,16 hex 字符)。不持久化;**daemon 重连即换新 id**(与 vision「in-memory map 重启清空」一致,被接受)。web 通过 `GET /api/commander/daemons` 发现当前在线的 id。

### 命令关联

observer 侧维护 per-conn `pending map[cmdID]chan commander.Envelope`。每条命令:

- 生成 `cmdID`(Hub 级 `atomic.Int64` 自增,`strconv.FormatInt(n,36)`),跨 hub 唯一;
- 建 buffered chan(`cap=16` 足够吸收突发 event);
- 写 `Envelope{type:command, id:cmdID, payload:{command, args}}`;
- 入站 `event` 流入 chan,`command_result`/`error` 作为终止帧流入后删 pending;
- 断连:关闭 chan,SSE/调用方据此发 error。

## WS hub(`/api/daemon-link`)

核心结构:

```go
// owner 是隔离键:同一 (UserID, WorkspaceID) 下的 daemon 互见,跨 owner 隔离。
type owner struct {
    userID      string
    workspaceID string
}

type Hub struct {
    resolver identity.Resolver
    upgrader websocket.Upgrader      // CheckOrigin: 放行(daemon 非 browser)
    cmdSeq   atomic.Int64            // cmdID 序列

    mu    sync.Mutex
    conns map[owner]map[string]*daemonConn  // owner → daemonID → conn
}

type daemonConn struct {
    id            string
    owner         owner               // {userID, workspaceID},daemon 注册时由 ProxyToken whoami 确定
    displayName   string              // 取自 RegisterPayload.DisplayName
    kind          string              // RegisterPayload.Kind
    driverVersion string              // RegisterPayload.DriverVersion
    conn          *websocket.Conn
    writeMu       sync.Mutex
    pending       map[string]chan commander.Envelope  // cmdID → 回包流
    done          chan struct{}                        // 读循环退出信号
    hub           *Hub
}
```

`Hub` 暴露给 proxy 层的方法(均以 `owner{userID,workspaceID}` 作权限边界):

- `Daemons(userID, workspaceID string) []DaemonInfo` —— 快照该 owner 全部在线 daemon(`{daemonID, displayName, kind, driverVersion}`)。
- `Lookup(userID, workspaceID, daemonID string) (*daemonConn, bool)` —— 跨 owner(不同用户或不同 workspace)查不到 → `false` → 路由层回 404。
- `SendCommand(ctx, userID, workspaceID, daemonID, command string, args json.RawMessage) (result json.RawMessage, err error)` —— 非流式(`list_sessions`/`get_session`):等首条终止帧(`command_result` 或 `error`)。
- `SendCommandStream(ctx, userID, workspaceID, daemonID, command string, args json.RawMessage) (<-chan commander.Envelope, error)` —— 流式(`session_turn`):返回 chan,调用方(SSE handler)排空至终止帧或 ctx 完成。

## Web 登录(device flow + observer session cookie)

observer 作为 device client 走 agentserver device-code OAuth,成功后给浏览器颁 httpOnly cookie。**token 全程不离开服务器,不进 JS/localStorage。**

1. **发起**:`POST /api/commander/login` → observer 调 `agentsdk.RequestDeviceCode(ctx, agentserverURL)` → 得 `{device_code, user_code, verification_uri_complete, expires_in, interval}`。
2. **login 状态**:observer 把 `device_code` 存在 in-memory `logins[loginID]`(loginID = crypto/rand),返回给浏览器 `{verification_uri_complete, user_code, expires_in, login_id}`(`device_code` 不出服务器)。observer 起后台 goroutine 按 `interval` 轮询 agentserver `/oauth2/token`。
3. **授权**:浏览器**新开标签页**打开 `verification_uri_complete`(agentserver 授权页)→ 用户登录授权。
4. **拿 token + 颁 cookie**:observer 后台轮询拿到 token response → 从 `id_token` claims 取 `sub/workspace_id`(若有 `workspace_role` 也带上)形成 `Identity`(OAuth access token 不是 ProxyToken,不调用 `/api/agent/whoami`) → 生成 `sessionID`(crypto/rand)→ in-memory `sessions[sessionID] = {token, identity, expiresAt}` → `Set-Cookie: commander_sess=<sessionID>; HttpOnly; Secure(HTTPS); SameSite=Lax; Path=/`。
5. **前端感知**:浏览器轮询 `GET /api/commander/login/poll?id=<login_id>`;成功后返回 OK,前端进入已登录态(切回 observer 标签页即见)。失败/超时 → 返回 pending/错误。
6. **登出**:`POST /api/commander/logout` → 删 `sessions[sessionID]`、清 cookie。

`commanderIdentity(r) (identity.Identity, bool)`(供 `/api/commander/*` 除 login 外的所有路由):**先试 cookie** → `sessions[sessionID]` → 用缓存的 `identity`;**无 cookie 再试 `Authorization: Bearer`** → `resolver.Resolve`(仅适合 ProxyToken/debug bearer);都失败 → 401。

session 存储:**v1 in-memory**(`map + mutex + 过期清理`);observer 重启需重登。token 刷新/静默续期 v1 不做(token 失效 → 401 → 重新 device flow)。

## Reverse proxy(`/api/commander/*`)

### 路由

嵌套式(细化 vision 的扁平 `{daemon_id}/{session_id}/turn`,更 RESTful 且天然带 daemon 作用域):

| 方法 | 路径 | 含义 |
|---|---|---|
| GET | `/api/commander/daemons` | 列本用户+workspace 在线 daemon |
| GET | `/api/commander/sessions` | 扇出全部 daemon 的 `list_sessions` + 聚合(每条带 `daemon_id`) |
| GET | `/api/commander/daemons/{id}/sessions` | 单 daemon 的 session 列表 |
| GET | `/api/commander/daemons/{id}/sessions/{sid}` | 单 session 描述 + 历史(`get_session`) |
| POST | `/api/commander/daemons/{id}/sessions/{sid}/turn` | 发一轮 turn,**SSE 响应** |

除 login 流程外,所有 `/api/commander/*` 路由先过 `commanderIdentity(r)`(cookie 或 bearer)→ `Identity{UserID, WorkspaceID}`;失败 → 401。`{id}` 跨 owner(不同用户或不同 workspace)查不到 → 404。

### 鉴权与扇出(fail-open)

`GET /sessions` 对该 owner(`(UserID, WorkspaceID)`)的每台 daemon **并发** `SendCommand(list_sessions)`,每台套 `context.WithTimeout(默认 10s)`;聚合返回:

```json
{
  "daemons": [
    {"daemon_id":"a1b2..","display_name":"office-mac","kind":"claude","status":"ok","sessions":[{...}]},
    {"daemon_id":"c3d4..","display_name":"home-linux","kind":"codex","status":"timeout","error":"context deadline exceeded"}
  ]
}
```

`status ∈ {ok, timeout, error}`。慢/掉线那台不阻塞其余(fail-open,Q4 决议)。

### turn SSE(observer→浏览器)

POST handler:`commanderIdentity` → `Lookup` → `SendCommandStream(session_turn, {id, prompt})` → 边收帧边写 SSE,直到终止帧 / ctx 完成 / 超时(默认 30s)。SSE 行形态与 PR-2 daemon 本机 API **完全一致**(浏览器侧无感知差异):

| daemon 入站帧 | observer 写出的 SSE |
|---|---|
| `event`(payload `EventPayload{event_kind:"chunk", text}`) | `event: chunk\ndata: {"text":"..."}\n\n` |
| `command_result`(payload `marshalTurnResult` → `{"result":{...}}`) | `event: done\ndata: {"result":{...}}\n\n` |
| `error`(payload `{code, message}`) | `event: error\ndata: {"code":"...","message":"..."}\n\n` |

error code 来源区分:**daemon 产生的** error 帧带自己的 code(`commander.ErrCode*`,如 `session_not_found`),observer 原样透传;**observer 合成的** error 用本地 code——daemon 断连 → `code: backend_unavailable`(沿用 `commander.ErrCodeBackendUnavailable`),30s 无终止帧 → `code: timeout`(observer 本地常量,非 commander 集)。

## Web UI(`/commander`)

- 资源:`go:embed` 单页 `index.html` + `app.js` + `style.css`,vanilla JS,无构建步骤。
- 登录:页面顶部「用 agentserver 登录」按钮 → `POST /api/commander/login` 拿 `{verification_uri_complete, user_code, login_id}` → **新开标签页**打开 `verification_uri_complete`(agentserver 授权)→ 用户登录授权 → 前端 `GET /api/commander/login/poll?id=<login_id>` 轮询 → observer 拿到 token、**Set-Cookie**(httpOnly)→ 前端自动进入已登录态(切回标签即见)。**token 全程不进 JS。** workspace 由 token 隐式决定,UI 不做 workspace 选择器。
- 所有 fetch 带 `credentials: 'include'`(发 cookie);`/api/commander/*` 也接受 `Bearer` 供脚本/curl 调试。
- 视图:① daemon 列表(`GET /daemons`)→ ② 选 daemon → session 列表(`GET /daemons/{id}/sessions`)→ ③ 选 session → chat view(拉历史 `GET .../{sid}` + 发 turn 走 `POST .../turn` SSE,chunk 实时追加)。
- **turn SSE 用 `fetch` + `ReadableStream` reader 解析**,不用 `EventSource`(`EventSource` 只支持 GET,无法发 POST body)。其余 GET 路由可用 `fetch`/`EventSource`。
- `command_result.awaiting_user == true` 时显示「请到 CLI 端继续」(anti-scope:不做 web humanloop 审批)。
- daemon 重连换 id:页面每次切回 daemon 列表重新拉 `/daemons`,自然刷新。

## wiring

`observerweb.NewWithResolverOptions(...)` 内部新增:`hub := commanderhub.New(resolver)` 与 `auth := commanderhub.NewAuthenticator(resolver, agentserverURL)`,并在 mux 挂路由(`/api/daemon-link` + `/api/commander/login|login/poll|logout` + 5 条 `/api/commander/*`)+ `/commander` 静态。`cmd/observer-server/main.go` **零改动**(resolver + agentserver URL 已在 / 易取)。

## 测试策略

e2e 复用 `commander.WSClient`(PR-2)当「真 daemon」——这是 PR-2 代码的直接再投资,也是最强集成验证。

- `hub_test.go`:进程内 upgrader + 真 `commander.WSClient`;验证 register/ack 往返、schema mismatch 被拒、401 不升级、多 daemon 注册、断连清理 pending。
- `registry_test.go`:增删查、按 `(UserID, WorkspaceID)` 过滤、跨用户不可见、**同用户跨 workspace 不可见**。
- `proxy_test.go`:2 个假 daemon 的 `list_sessions` 扇出聚合、fail-open 部分(timeout/error 不拖垮其余)、跨 owner `Lookup` 返回 false→404、单 session `get_session` 往返。
- `sse_test.go`:daemon `Envelope` → SSE 行形态(与 PR-2 SSE 断言同构)。
- `auth_test.go`:假 agentserver(或 mock `agentsdk` 的 device flow)→ `login` 发起、`poll` 拿 token → 验证 Set-Cookie(httpOnly);带 cookie 的 `/api/commander/*` 通过、`Bearer` 兜底通过、无凭证 401、`logout` 清 cookie。
- `web_test.go`:无 cookie/bearer → 401;带 cookie → 200;`/commander` 返回嵌入页。
- `e2e_test.go`:用假 `identity.Resolver` 把 4 个 token 映射到 `(user, workspace)` 网格 —— `tok-AW`(alice/W)、`tok-AW2`(alice/W2)、`tok-BW`(bob/W)、`tok-BW2`(bob/W2);各起一个 `commander.WSClient` 注册;断言用 `tok-AW` 查 `/daemons` 只见 (alice,W) 那台、用 `tok-AW2` 只见 (alice,W2) 那台(同用户跨 workspace 隔离)、bob 对 alice 不可见;`/sessions` 扇出与 turn SSE 同理。

## 兼容性

- 不影响现有 `/api/events`、`/api/agents/register`、`/api/tasks/*` 等路由(新增独立前缀)。
- daemon(PR-2)行为不变:observer 实现的是 PR-2 已声明的对面契约。
- `observer.api_key` 旧鉴权仍用于既有事件上报路径,不受影响;commander 走 cookie/bearer + resolver。
- `identity.Resolver` 已有缓存包装(`identity.NewCache`),daemon/bearer 高频鉴权无额外 whoami 压力。

## 决策记录

| 决策 | 备选 | 选择理由 |
|---|---|---|
| daemon_id 由 observer 连接时分配 | 配置稳定 id / 复合 key | 重连换 id 可接受(map 本就重启清空);零新增 driver.Config;web 经 `/daemons` 发现 |
| observer→浏览器用 SSE | 浏览器↔observer 也用 WS | 与 daemon 本机 SSE 形态一致;浏览器侧单向、vanilla 友好;双向收益低 |
| 扇出 fail-open 返回部分 | fail-closed 整体 504 | 单台故障不拖垮列表;每台带 status 透传错误 |
| 独立 `commanderhub` 包 | 塞进 observerweb/server.go | 有状态并发子系统,隔离可测;server.go 已过大 |
| 复用 `internal/commander` 协议类型 | observer 重定义 | schema 单一来源,与 PR-2 零偏差 |
| `/sessions` 扇出嵌套 `/daemons/{id}/...` | 扁平 `{daemon_id}/{session_id}/turn` | RESTful、daemon 作用域清晰、`{id}` 统一鉴权点 |
| 隔离键用 `(UserID, WorkspaceID)` | 仅 UserID / 仅 WorkspaceID / 自建 subject | daemon ProxyToken 的 whoami 与 web `id_token` claims 都提供这两个字段 → (user,workspace) 同时相同才匹配;workspace 由凭证隐式带,UI 无需切换器 |
| web 登录 = device flow + observer httpOnly cookie | 贴 token(localStorage)/ 改 agentserver 加 redirect OAuth | agentserver 仅 device-code、无 redirect;observer 当 device client 干掉「贴 token」且 token 不进 JS;不改 agentserver(守 vision)。代价:授权后需手动切回 observer 标签页(device flow 无 callback),靠前端轮询自动感知登录 |

## 反目标 / 反范围

- **不动** master 路径、`serve-mcp`、事件上报。
- **不改 agentserver**:只复用其现有 device-code OAuth;observer 自管 session cookie。要做真·redirect 静默跳回需 agentserver 加 `/authorize`+PKCE,属跨仓独立 effort,不在本 PR。
- **不改 daemon**:PR-2 冻结。
- **不做 web humanloop 审批 UI**(v1 `awaiting_user` 提示去 CLI)。
- **不持久化** daemon 注册表 / observer session(重启清空;backend session 文件本身一直在)。
- **不做 workspace 切换器**:workspace 由登录凭证隐式决定。
- **v1 session 存内存**(observer 重启需重登);token 静默刷新/续期 v1 不做(cookie 失效 → 401 → 重登)。
- **不加命令取消帧**:浏览器中途断开 SSE 时,daemon 侧 turn 跑到完成(见风险)。

## 风险

1. **浏览器断开 → daemon turn 不取消**:当前 schema 无 cancel 帧;SSE 关闭只让 observer 停止转发,daemon 继续跑到完成。v1 接受(产物仍在 backend session 文件里);v2 加 cancel 帧。
2. **daemon 重连风暴**:每次重连换 daemonID,web 旧引用失效。缓解:`/daemons` 轮询刷新;UI 优雅处理 404(返回列表)。
3. **单 observer 是 SPOF**:vision 接受「云端单 observer」;水平扩展不在本 PR。
4. **身份信任边界**:daemon/事件身份依赖 resolver + agentserver whoami;web 登录身份依赖 agentserver device-flow token response 的 `id_token` claims。cookie 方案下 token 不进 JS,攻击面比「token 存 localStorage」小。
5. **命令关联 chan 背压**:突发大量 event 可能阻塞读循环(非阻塞 send 失败时丢帧 + 日志,而非阻塞整个连接)。
6. **device flow 非静默**:agentserver 授权完不跳回 observer(device flow 无 callback),需用户切回标签页;前端靠轮询自动检测登录态。这是不改 agentserver 的代价。
7. **token 失效**:observer cookie session 过期/重启 → 401 → 重新 device flow。v1 无静默刷新。
8. **CSRF**:cookie 方案下 turn 是 POST;靠 `SameSite=Lax`(同源 fetch 仍带 cookie、跨站 POST 不带)+ observer 与页面同源缓解;`/api/commander/*` 不接受跨站表单 POST。

## Implementer pre-flight checklist

- [x] **已确认**:`identity.Identity` 含 `UserID` 与 `WorkspaceID`;daemon 侧来自 agentserver `/api/agent/whoami`,web 侧来自 device-flow `id_token` claims;隔离键 = `(UserID, WorkspaceID)`。`Identity` 另含 `AgentID/SandboxID/Role/Source`,本 PR 不用。
- [x] **已确认**:agentserver OAuth 仅 device-code(`agentsdk.RequestDeviceCode` + `/oauth2/token` 轮询),**无 redirect 端点** → web 登录走 device flow。
- [ ] 确认 `agentsdk` device flow 的确切 API:`RequestDeviceCode(...)` 返回字段名 + 轮询拿 token 的函数名(`PollDeviceToken` / `ExchangeDeviceCode` 之类),observer 直接复用。
- [ ] 确认 observer 拿得到 agentserver baseURL(resolver 已有;或 observer config),用于驱动 device flow。
- [ ] 确认 `observerweb.bearerToken` 的确切签名(直接复用,勿重造);`commanderIdentity` 在其上扩展 cookie 路径。
- [ ] 确认 gorilla `Upgrader` 在 observerweb 既有 `Options` 里的复用方式(CheckOrigin 放行 daemon)。
- [ ] 确认 `internal/commander` 导出符号清单:`Envelope`/`RegisterPayload`/`CommandPayload`/`GetSessionArgs`/`SessionTurnArgs`/`EventPayload`/`ErrorPayload`/`SchemaVersion`/`ErrCode*`/`marshalTurnResult` 均可被 observer 包导入(必要时把 `marshalTurnResult` 提为导出)。
- [ ] 确认 `cmd/observer-server/main.go` 调 `NewWithResolverOptions` 的位置,验证 Hub+Authenticator 内嵌构造后 main 真零改动。
- [ ] cookie 属性:`HttpOnly; Secure(HTTPS); SameSite=Lax; Path=/`;session/login id = crypto/rand。
- [ ] e2e 用假 `identity.Resolver`(`token→(UserID, WorkspaceID)` 映射网格)+ 假 agentserver(或 mock `agentsdk` device flow)驱动多个 `commander.WSClient`。
