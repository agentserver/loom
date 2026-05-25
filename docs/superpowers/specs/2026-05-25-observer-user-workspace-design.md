# Observer 单用户 / 多 Workspace 重塑

**日期**: 2026-05-25
**状态**: 设计已确认，待写 plan
**前置**: 现行 `2026-05-20-observer-api-key-registration-design.md`（api-key bootstrap）
**后置**: `2026-05-25-personal-mcp-skill-space-design.md` 需基于本 spec 重写为 observer 扩展

---

## 0. 背景与目标

### 0.1 现状

`observer-server` 当前模型：
- `workspaces`：yaml 静态预声明
- `api_keys` PK = `(workspace_id, id)`：每把 key 出生时就绑死 workspace
- `agents`：register 时由 api_key 反查 workspace_id 落行
- 实际部署：**每个 observer 进程只配 1 个 workspace**；schema / handler 支持多 workspace，但 UI 无切换器、yaml 默认单 workspace

这套对"一个人多 namespace"很别扭：要切 namespace 得改 yaml + 重启，或起多个 observer 进程。

### 0.2 目标

- **单用户 + 多 workspace**：一个 observer 进程服务一个"我"，"我"可以有任意多个 workspace（personal / work / experimental ...）
- `api_key` 上升为顶层凭证，**代表"我"**，不再绑 workspace
- agent 注册时**自带 workspace_id**，observer 端 lazy 创建——切 namespace 不需要重启
- web 看板加 workspace 切换器，跨 namespace 浏览
- 为后续 [userspace as observer extension] 铺路：包 PK = `(workspace_id, slug)`，一个 workspace 一个 space

### 0.3 非目标（本期）

- 多用户 / `users` 表 / OAuth / 自助注册
- 跨 workspace 聚合视图（看板永远是单 workspace 视角，只是可切换）
- 数据迁移代码（按已确认决策：观测环境 fresh restart；prod 备份 .db 后重建 schema）
- 改 `events / tasks / subtasks / artifacts / writes / mcp_servers` 任何业务表
- userspace 折叠本身（本 spec 只重塑鉴权 / workspace 来源层；userspace 单独 spec）

---

## 1. 决策摘要

| 维度 | 决策 |
|---|---|
| 租户模型 | 单用户隐式 + 多 workspace 显式；不引入 users 表 |
| api_key 作用域 | 顶层（PK = id，UNIQUE(key_hash)）；代表"本 observer 实例的我" |
| Workspace 来源 | agent register 时自带 `workspace_id`；observer lazy INSERT |
| Workspace 命名 | agent 可选传 `workspace_name`；**先到者定 name**，后到者不覆盖 |
| Agent 改绑 | 禁止：同 agent_id 再 register 但 workspace_id 不同 → 409 |
| 数据迁移 | 无 migration 代码；备份 .db + DROP + 重建 schema |
| yaml | 顶层 `api_keys:` 列表；不再有 `workspaces:` 配置块 |
| 既有业务表 | 100% 不动（events / tasks / artifacts / writes / mcp_servers / ...） |

---

## 2. 数据模型

### 2.1 删 / 改 / 新

```sql
-- 改：workspaces 表语义变更（schema fresh restart, 不需 ALTER）
CREATE TABLE workspaces (
  id                      TEXT PRIMARY KEY,
  name                    TEXT NOT NULL DEFAULT '',
  created_by_api_key_id   TEXT NOT NULL REFERENCES api_keys(id),
  created_at              TEXT NOT NULL,
  last_seen_at            TEXT NOT NULL
);

-- 改：api_keys 顶层化
CREATE TABLE api_keys (
  id          TEXT PRIMARY KEY,            -- 'ak-personal'
  key_hash    TEXT NOT NULL UNIQUE,        -- SHA-256(key)
  note        TEXT NOT NULL DEFAULT '',    -- 显示名
  created_at  TEXT NOT NULL
);

-- 改：agents 表加 created_by_api_key_id（其他列不动）
CREATE TABLE agents (
  workspace_id           TEXT NOT NULL,
  id                     TEXT NOT NULL,
  role                   TEXT NOT NULL,
  display_name           TEXT NOT NULL,
  token_hash             TEXT NOT NULL,
  created_by_api_key_id  TEXT NOT NULL REFERENCES api_keys(id),
  PRIMARY KEY (workspace_id, id)
);
CREATE UNIQUE INDEX idx_agents_token_hash ON agents(token_hash);
```

其他表（events / tasks / subtasks / mcp_servers / artifacts / artifact_requests / writes / task_contracts / resource_snapshots ...）**schema 一行不改**——它们的 `workspace_id` 列依然是逻辑分区键，只是 workspace 行的"出生地"从 yaml 改为 agent register 时 lazy INSERT。

### 2.2 不变量

- 所有以 `workspace_id` 起手的现有 SQL query 一律不改
- 每个事件 / artifact / write 仍只属于一个 workspace，跨 workspace 不可见
- `LookupAPIKey` 不再返回 workspace_id（key 本身不绑 workspace）

### 2.3 关键变量

| 函数 | 旧签名 | 新签名 |
|---|---|---|
| `LookupAPIKey(key)` | `(workspaceID, keyID, ok, err)` | `(keyID, ok, err)` |
| `ReplaceAPIKeysForWorkspace(wsID, keys)` | 删；改为 | `ReplaceAPIKeys(keys)` 全局同步 |
| `UpsertAgent(...)` | 不带 `created_by_api_key_id` | 加该字段 |
| 新增 `UpsertWorkspaceLazy(wsID, wsName, apiKeyID)` | — | UPSERT，name 仅首次插入时写入 |

---

## 3. 配置（observer.yaml）新形态

```yaml
listen_addr: ":8090"
db_path: observer.db

api_keys:
  - id: ak-personal
    key: <hex>           # operator: openssl rand -hex 32
    note: "alice's main key"
  - id: ak-jetson-fleet
    key: <hex>
    note: "shared key for all jetson slaves"
```

**移除**：`workspaces:` 块整段（workspace 一律由 agent 自声明）。

`main.go` 启动逻辑改一行：原 `for ws := range cfg.Workspaces { ReplaceAPIKeysForWorkspace(ws.ID, ...) }` 换成 `ReplaceAPIKeys(cfg.APIKeys)`——api_keys 全局 sync-from-yaml。

校验：
- `api_keys` 至少 1 个，每个 `id` 非空 + `id` 不重复 + `key` 非空 + 不同 key 的 `key_hash` 不冲突
- 不再校验 `workspaces` 块

---

## 4. Agent 注册流程

### 4.1 Request

```http
POST /api/agents/register
Authorization: Bearer <api_key>
Content-Type: application/json

{
  "agent_id": "slave-jetson-01",
  "role": "slave",
  "display_name": "Jetson 01",
  "workspace_id": "ws-personal",        // 必传
  "workspace_name": "Personal Lab"       // 可选；仅首次创建该 workspace 时写入
}
```

**workspace_id 约束**：
- 非空 + 长度 1~64 + `^[a-zA-Z0-9_-]+$`（避免 SQL 注入和路径串扰）
- 不符合 → 400 with field-level error

### 4.2 服务端逻辑

```
1. LookupAPIKey(bearer) → api_key_id；失败 → 401
2. 校验 workspace_id 格式
3. 改绑早检（在动数据库前）:
     SELECT workspace_id FROM agents WHERE id=? LIMIT 1
     IF found AND existing.workspace_id != req.workspace_id:
       → 409 "agent already bound to workspace X"
     -- 早检的目的：避免步骤 4 顺手 lazy 建出一个孤儿 workspace
4. 全部走一个 sqlite 事务，失败整体回滚：
     BEGIN
     UpsertWorkspaceLazy(workspace_id, workspace_name, api_key_id):
       INSERT INTO workspaces(id, name, created_by_api_key_id, created_at, last_seen_at)
         VALUES(?, COALESCE(NULLIF(?,''), ''), ?, now, now)
         ON CONFLICT(id) DO UPDATE SET last_seen_at = now
         -- name 不在 conflict-update 列表中：先到者定 name
     UpsertAgent(workspace_id, agent_id, role, display_name,
                 token_hash=SHA256(new_token),
                 created_by_api_key_id=api_key_id)
       -- 同 agent_id 重复 register = 旧 token 立即失效，新 token 上位
     COMMIT
5. Return 200:
   { "token": <new_token>,
     "agent_id": "...",
     "workspace_id": "ws-personal" }
```

### 4.3 幂等 / 改绑语义

| 情形 | 行为 |
|---|---|
| 同 agent_id + 同 workspace_id 重 register | 旋转 token（旧 token_hash 被新值覆盖）；workspace 不变；last_seen_at bump |
| 同 agent_id + 不同 workspace_id | 409；客户端必须先 `DELETE /api/agents/{id}`（本期不实现，留 admin CLI 未来加）或手 SQL |
| 同 workspace_id 多 agent | 全部 join；name 仍由最早创建者保持 |
| 不存在的 api_key | 401，不创建 workspace 不创建 agent |

### 4.4 API key 撤销级联

不暴露 HTTP；store 层 + 未来 admin CLI 用：
- yaml 删 api_key 行 + 重启 → `ReplaceAPIKeys` 删该行 → 该 key 后续 register 401；**已发 token 不受影响**（agents.token_hash 不依赖 api_key 在不在）
- store 层提供 `RevokeAPIKey(id, cascade bool)`：
  - `cascade=false`：删 api_keys 行（同上）
  - `cascade=true`：先删该 key created 的 agents（`DELETE FROM agents WHERE created_by_api_key_id=?`）再删 api_keys 行
- 本期不接 HTTP，但 store 接口先到位

---

## 5. observerweb 影响

### 5.1 handler 层

零改动。所有现有路由通过 agent token → agent row → workspace_id 推断，自动 scope。

### 5.2 register handler

接受新字段 `workspace_id` / `workspace_name`，按 §4 逻辑处理。

### 5.3 新增 `/api/workspaces`

```http
GET /api/workspaces
→ 200 [
    {"id": "ws-personal", "name": "Personal Lab",
     "last_seen_at": "...", "agent_count": 3, "recent_event_at": "..."},
    {"id": "ws-work", "name": "Work", "last_seen_at": "...", ...}
  ]
```

**本期不鉴权**（单用户场景，浏览器直访）；留 `OBSERVER_WEB_TOKEN` env 作可选守护开关：
- 未设 → 路由开放
- 设 → 请求需带 `?web_token=<...>` 或 `X-Observer-Web-Token` header；失败 401

### 5.4 看板 navbar

加 workspace 切换器：
- 默认选 `last_seen_at` 最新的 workspace
- 切换后 URL 加 `?ws=<id>` query 持久化
- 所有现有视图（events / tasks / artifacts）从 URL 取 `ws=...` 并传给后端 query
- **缺省语义**：URL 不带 `?ws=` 时，后端响应里塞一个 `default_workspace_id`（=最新 last_seen_at 那个），前端立即 redirect 到 `?ws=<default>` 后再渲染；没有任何 workspace 时（fresh install）渲染空态卡片，提示"先起一个 agent 并声明 workspace_id"
- 看板永远是**单 workspace 视图**，不做"所有 workspace 合并"模式（避免视觉混淆 + handler 层无改动）

---

## 6. 迁移（fresh restart）

```bash
# 1) 停 observer
systemctl stop observer-server

# 2) 备份（万一回滚）
mv /var/lib/observer/observer.db /var/lib/observer/observer.db.bak.$(date +%s)

# 3) 改 yaml：删 workspaces 块，提 api_keys 到顶层
vi /etc/observer/observer.yaml

# 4) 各 agent 配置加 workspace_id；删本地 token_state
for host in driver master slave-*; do
  ssh $host "vi /etc/agent/agent.yaml && rm -f /var/lib/agent/observer-token.json"
done

# 5) 起 v2
systemctl start observer-server
systemctl restart driver-agent master-agent slave-*-agent
```

agent 首次 ingest 401 → 现有 auto re-register 路径触发 → 拿 api_key 调 `/api/agents/register` → 落到声明的 workspace → 正常 ingest。

---

## 7. 对 userspace spec 的影响（出本 spec 边界）

`2026-05-25-personal-mcp-skill-space-design.md` 落地时按下表重写：

| userspace 旧概念 | 改写为 observer 概念 |
|---|---|
| `users` 表 | 删（单用户隐式） |
| `owner_user_id` | 删（不需要） |
| `devices` 表 | 删；用 `agents` 表，新增 `device_pubkey BLOB` + `last_seen` 列 |
| `user_token` | api_key |
| `device_token` | agent token（现有 register 流程） |
| 包 PK `(user_id, slug)` | `(workspace_id, slug)`——一个 workspace 一个 space，跨 workspace 同 slug = 两个独立实体（用户已确认） |
| `cmd/mcp-userspace` 独立进程 | 折掉；路由挂 observer-server `/api/userspace/*` |
| `mcp-userspace login` | `mcp-userspace register --workspace-id ws-foo` |

→ 本 spec 落地后，userspace spec 单独改一版（标题 *userspace as observer extension*），ROADMAP 把原 userspace spec 标 superseded。

---

## 8. 测试

| 用例 | 形态 | 文件 |
|---|---|---|
| yaml 加/删/改 api_keys 后重启 → `api_keys` 表 sync 一致 | unit (store) | `observerstore/store_test.go` |
| Register 未知 api_key → 401 | httptest | `observerweb/server_test.go` |
| Register 首次见 workspace_id → workspaces INSERT，name 写入 | httptest | 同上 |
| Register 二次同 workspace_id 带不同 name → name 不变，last_seen bump | httptest | 同上 |
| Register 同 workspace 多 agent → 全部 join，互不覆盖 | httptest | 同上 |
| Register 同 agent_id + 不同 workspace_id → 409 | httptest | 同上 |
| Register 改绑被拒后**不留孤儿 workspace**（即不调 lazy upsert）| httptest | 同上 |
| Token 旋转：同 agent_id 重 register → 旧 token_hash 被覆盖，旧 token 401 | httptest | 同上 |
| 跨 workspace 隔离：A 的 token 不能 ingest B 的 events | httptest | 同上 |
| `GET /api/workspaces` 按 last_seen_at 倒序 | httptest | 同上 |
| `workspace_id` 含非法字符（空格 / `;` / 超长）→ 400 | httptest | 同上 |
| `RevokeAPIKey(id, cascade=true)` 删 api_key 同时删 created agents | unit | `observerstore/store_test.go` |
| `RevokeAPIKey(id, cascade=false)` 仅删 api_key，已发 agent 仍可 ingest | unit | 同上 |

---

## 9. 代码触面

```
multi-agent/
├── cmd/observer-server/
│   ├── main.go               # 改：Config.Workspaces 删；新增 Config.APIKeys；ReplaceAPIKeys
│   ├── main_test.go          # 改：覆盖新 config 校验
│   └── config.example.yaml   # 改：示例换成顶层 api_keys
├── internal/observerstore/
│   ├── schema.sql            # 改：workspaces 加 created_by_api_key_id+last_seen_at；api_keys 去 workspace_id，加 UNIQUE(key_hash)；agents 加 created_by_api_key_id
│   ├── store.go              # 改：LookupAPIKey 改签名；ReplaceAPIKeys；UpsertWorkspaceLazy；UpsertAgent 接 api_key_id；RevokeAPIKey
│   └── store_test.go         # 改：覆盖新签名
├── internal/observerweb/
│   ├── server.go             # 改：register handler 接 workspace_id/workspace_name；新增 ListWorkspaces handler；optional OBSERVER_WEB_TOKEN
│   └── server_test.go        # 改：覆盖 register + workspaces 列表
├── internal/observerclient/
│   ├── client.go             # 改：Register 入参加 workspace_id (必)、workspace_name (可选)
│   └── client_test.go        # 改
└── cmd/{driver,master,slave}-agent/
    └── (各自 main / config)   # 改：yaml 加 workspace_id 字段；启动透传给 observerclient.Register
```

约 9 个文件改动；不动 events / tasks / subtasks / artifacts / writes / mcp_servers 任何业务逻辑。

---

## 10. 实施顺序建议（给后续 plan 用）

1. `internal/observerstore/{schema,store,store_test}` —— 新 schema + 新签名 + RevokeAPIKey
2. `cmd/observer-server/{main,config.example,main_test}` —— 顶层 api_keys 解析 + ReplaceAPIKeys 调用
3. `internal/observerweb/{server,server_test}` —— register handler 改签 + ListWorkspaces
4. `internal/observerclient/{client,client_test}` —— Register 入参扩展
5. `cmd/{driver,master,slave}-agent` 各自 main + config —— 透传 workspace_id
6. e2e：本地起 observer + 三个 agent 各带不同 workspace_id → register OK → ingest → `/api/workspaces` 列出全部
7. observerweb 看板 navbar 切换器（前端，本 spec 算可选；后端 ListWorkspaces 给齐即可）

---

## 11. 风险 & Open questions

1. **workspace_id 命名空间污染**：单用户场景，恶意/手滑 agent 配错 workspace_id 会污染。校验只能限格式不能限语义。MVP 接受，未来可加 admin CLI `observer-admin workspace prune`
2. **api_key 显示**：observer 启动日志当前会打 api_key 数量；务必**不打印 key 本身或 hash**，只打 `id`
3. **`OBSERVER_WEB_TOKEN` 缺省 = 开放**：单用户场景默认在内网，可接受；若部署到公网请显式设置
4. **agent 改绑禁止 + 无 admin CLI 删 agent**：本期被卡住的用户只能直 SQL 删行。可加 `cmd/observer-admin` 子项目，本期不强求
