# Observer 单用户 / 多 Workspace 重塑 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 observer 从"yaml 静态 workspaces + workspace-scoped api_keys"重塑为"顶层 api_keys（隐式单用户）+ agent 注册时自带 workspace_id（lazy 建）"，为后续 userspace 折叠进 observer 铺路。

**Architecture:** schema 重置（DROP + 新建，无 ALTER 迁移）。`api_keys` 提到顶层（PK=id, UNIQUE(key_hash)）。`workspaces` 表加 `created_by_api_key_id` + `last_seen_at`，行的"出生地"从 yaml 改为 `/api/agents/register` 时由服务端 lazy upsert。`agents` 加 `created_by_api_key_id` 列，方便 admin cascade 撤销。Register handler 把 lazy workspace upsert + agent upsert 放进一个 sqlite 事务，前面再加 agent 改绑早检（避免 409 时留孤儿 workspace）。事件 / 任务 / artifact 等业务表 schema 一行不动。

**Tech Stack:** Go 1.x；testify/require；标准库 `database/sql` + `modernc.org/sqlite` driver；`gopkg.in/yaml.v3`；现有 `internal/observerstore`、`internal/observerweb`、`internal/observerclient`、`cmd/observer-server`、`cmd/{driver,master,slave}-agent`。

**Working directory for all commands:** `multi-agent/`（Go module 在子目录里；`cd multi-agent` 一次即可）。

**Spec:** `docs/superpowers/specs/2026-05-25-observer-user-workspace-design.md`

---

## File Map

**Modify:**
- `multi-agent/internal/observerstore/schema.sql` — workspaces 改字段；api_keys 去 workspace_id 加 UNIQUE(key_hash)；agents 加 created_by_api_key_id
- `multi-agent/internal/observerstore/store.go` — LookupAPIKey 签名改；UpsertWorkspaceLazy 替代 UpsertWorkspace；UpsertAgent 接 apiKeyID；ReplaceAPIKeys 取代 ReplaceAPIKeysForWorkspace；新增 RevokeAPIKey；新增 ListWorkspaceSummaries
- `multi-agent/internal/observerstore/store_test.go` — 全部按新签名重写
- `multi-agent/cmd/observer-server/main.go` — Config.Workspaces 删；新增 Config.APIKeys；调 ReplaceAPIKeys
- `multi-agent/cmd/observer-server/main_test.go` — 覆盖新 config 校验
- `multi-agent/cmd/observer-server/config.example.yaml` — 示例换成顶层 api_keys
- `multi-agent/internal/observerweb/server.go` — register handler 接 workspace_id/workspace_name + 409 改绑早检；新增 ListWorkspaces handler + `OBSERVER_WEB_TOKEN` env 守护
- `multi-agent/internal/observerweb/server_test.go` — 覆盖 register + workspaces 列表 + 守护开关
- `multi-agent/internal/observerclient/bootstrap.go` — register payload 加 workspace_id（必）+ workspace_name（可选）
- `multi-agent/internal/observerclient/bootstrap_test.go` — 覆盖 payload 变化
- `multi-agent/internal/observerclient/client.go` — Config 加 WorkspaceName
- `multi-agent/cmd/driver-agent/main.go` — observerclient.Config 透传 WorkspaceName
- `multi-agent/cmd/master-agent/main.go` — 同上
- `multi-agent/cmd/slave-agent/main.go` — 同上
- `multi-agent/cmd/driver-agent/config.example.yaml` — observer 段加 workspace_name 示例（注释中说明可选）
- `multi-agent/cmd/master-agent/config.example.yaml` — 同上
- `multi-agent/cmd/slave-agent/config.example.yaml` — 同上

**Create:**
- 无新文件；纯重塑

**Out of scope（写入风险/Open questions，本 plan 不做）：**
- 看板 navbar 切换器前端 UI（后端 `/api/workspaces` 给齐即可，前端可作后续单独 plan）
- admin CLI（`RevokeAPIKey` 仅暴露 store 层 + 单测，HTTP route 不开）
- `cmd/observer-admin` 子项目

---

## Task 1: 重置 observerstore schema + 调整 Store 方法签名

**Goal:** 让 schema.sql 与 store.go 完成一次性"破而后立"——schema 改完、store.go 全部新签名编译通过、store_test.go 改写成新签名后绿。这是一个原子的大 commit，因为这些改动相互依赖；之后再各个新行为分别 TDD。

**Files:**
- Modify: `multi-agent/internal/observerstore/schema.sql`
- Modify: `multi-agent/internal/observerstore/store.go:319-410`（涵盖 UpsertWorkspace / UpsertAgent / LookupAPIKey / ReplaceAPIKeysForWorkspace 四块）
- Modify: `multi-agent/internal/observerstore/store_test.go`

### Steps

- [ ] **Step 1.1: 改 schema.sql**

打开 `multi-agent/internal/observerstore/schema.sql`，把 `workspaces / api_keys / agents` 三段替换为：

```sql
CREATE TABLE IF NOT EXISTS workspaces (
    id                     TEXT PRIMARY KEY,
    name                   TEXT NOT NULL DEFAULT '',
    created_by_api_key_id  TEXT NOT NULL REFERENCES api_keys(id),
    created_at             TEXT NOT NULL,
    last_seen_at           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
    id          TEXT PRIMARY KEY,
    key_hash    TEXT NOT NULL UNIQUE,
    note        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agents (
    workspace_id           TEXT NOT NULL,
    id                     TEXT NOT NULL,
    role                   TEXT NOT NULL,
    display_name           TEXT NOT NULL,
    token_hash             TEXT NOT NULL,
    created_by_api_key_id  TEXT NOT NULL REFERENCES api_keys(id),
    PRIMARY KEY (workspace_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_token_hash ON agents(token_hash);
```

其余表（events / tasks / subtasks / mcp_servers / artifacts / artifact_requests / writes / task_contracts / resource_snapshots / ...）原样保留，**一行不动**。

- [ ] **Step 1.2: 改 store.go — 删 UpsertWorkspace、加 UpsertWorkspaceLazy**

在 `store.go` 中定位 `func (s *Store) UpsertWorkspace`（~319 行），整体替换为：

```go
// UpsertWorkspaceLazy 插入或 bump 一个 workspace 行。name 仅在首次插入时写入；后续
// 即使传不同 name 也不会覆盖（先到者定 name）。每次调用都 bump last_seen_at。
// 调用前 caller 必须先保证 apiKeyID 在 api_keys 表中存在（由 register handler 流
// 程上游校验 LookupAPIKey 通过）。
func (s *Store) UpsertWorkspaceLazy(id, name, apiKeyID string) error {
    if id == "" {
        return errors.New("observerstore: workspace id must not be empty")
    }
    if apiKeyID == "" {
        return errors.New("observerstore: apiKeyID must not be empty")
    }
    now := nowUTC()
    _, err := s.db.Exec(
        `INSERT INTO workspaces(id, name, created_by_api_key_id, created_at, last_seen_at)
         VALUES(?, ?, ?, ?, ?)
         ON CONFLICT(id) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
        id, name, apiKeyID, now, now,
    )
    return err
}
```

注意 `ON CONFLICT(id) DO UPDATE SET last_seen_at = excluded.last_seen_at` 故意**不更新 name 或 created_by_api_key_id**——这是"先到者定 name"的实现点。

- [ ] **Step 1.3: 改 store.go — UpsertAgent 接 created_by_api_key_id**

替换 `UpsertAgent`（~325 行）为：

```go
func (s *Store) UpsertAgent(a Agent, token, apiKeyID string) error {
    if token == "" {
        return errors.New("observerstore: agent token must not be empty")
    }
    if apiKeyID == "" {
        return errors.New("observerstore: apiKeyID must not be empty")
    }
    _, err := s.db.Exec(
        `INSERT INTO agents(workspace_id, id, role, display_name, token_hash, created_by_api_key_id)
         VALUES(?, ?, ?, ?, ?, ?)
         ON CONFLICT(workspace_id, id) DO UPDATE SET
            role = excluded.role,
            display_name = excluded.display_name,
            token_hash = excluded.token_hash`,
        a.WorkspaceID, a.ID, a.Role, a.DisplayName, tokenHash(token), apiKeyID,
    )
    return err
}
```

`created_by_api_key_id` 故意只在 INSERT 时写入（不在 conflict-update 列表）：同 agent_id 重 register 时，"创建者"身份不变。

- [ ] **Step 1.4: 改 store.go — LookupAPIKey 新签名**

替换 `LookupAPIKey`（~360 行）：

```go
// LookupAPIKey 在 api_keys 表里按 key_hash 反查 api_key id。
// ok=false 表示 key 未匹配；err 仅在 DB 真错时非 nil。
func (s *Store) LookupAPIKey(key string) (keyID string, ok bool, err error) {
    if key == "" {
        return "", false, nil
    }
    err = s.db.QueryRow(
        `SELECT id FROM api_keys WHERE key_hash=?`,
        tokenHash(key),
    ).Scan(&keyID)
    if err == sql.ErrNoRows {
        return "", false, nil
    }
    if err != nil {
        return "", false, err
    }
    return keyID, true, nil
}
```

- [ ] **Step 1.5: 改 store.go — UpsertAPIKey 去 workspaceID，加 note**

替换 `UpsertAPIKey`（~346 行）和它上面的 `APIKeySpec` 类型：

```go
// APIKeySpec 顶层 api_keys 表的一行原料。
type APIKeySpec struct {
    ID   string
    Key  string
    Note string
}

// UpsertAPIKey 插入或刷新一条 api_keys 行。
func (s *Store) UpsertAPIKey(spec APIKeySpec) error {
    if spec.ID == "" {
        return errors.New("observerstore: api key id must not be empty")
    }
    if spec.Key == "" {
        return errors.New("observerstore: api key value must not be empty")
    }
    _, err := s.db.Exec(
        `INSERT INTO api_keys(id, key_hash, note, created_at)
         VALUES(?, ?, ?, ?)
         ON CONFLICT(id) DO UPDATE SET
            key_hash = excluded.key_hash,
            note = excluded.note,
            created_at = excluded.created_at`,
        spec.ID, tokenHash(spec.Key), spec.Note, nowUTC(),
    )
    return err
}
```

- [ ] **Step 1.6: 改 store.go — 用 ReplaceAPIKeys 替代 ReplaceAPIKeysForWorkspace**

替换 `ReplaceAPIKeysForWorkspace`（~381 行）：

```go
// ReplaceAPIKeys 删除所有现有 api_keys 行，然后插入入参指定的全集。在 observer
// 启动时调用一次，把 yaml 与 db 对齐：yaml 里删掉的 key 重启后立即失效。
func (s *Store) ReplaceAPIKeys(keys []APIKeySpec) error {
    seenID := map[string]bool{}
    seenHash := map[string]bool{}
    for i, k := range keys {
        if k.ID == "" {
            return fmt.Errorf("observerstore: api key[%d] id must not be empty", i)
        }
        if k.Key == "" {
            return fmt.Errorf("observerstore: api key[%s] value must not be empty", k.ID)
        }
        if seenID[k.ID] {
            return fmt.Errorf("observerstore: duplicate api key id %q", k.ID)
        }
        h := tokenHash(k.Key)
        if seenHash[h] {
            return fmt.Errorf("observerstore: duplicate api key value (id=%q)", k.ID)
        }
        seenID[k.ID] = true
        seenHash[h] = true
    }
    tx, err := s.db.Begin()
    if err != nil {
        return err
    }
    defer tx.Rollback() //nolint:errcheck
    if _, err := tx.Exec(`DELETE FROM api_keys`); err != nil {
        return err
    }
    now := nowUTC()
    for _, k := range keys {
        if _, err := tx.Exec(
            `INSERT INTO api_keys(id, key_hash, note, created_at) VALUES(?, ?, ?, ?)`,
            k.ID, tokenHash(k.Key), k.Note, now,
        ); err != nil {
            return err
        }
    }
    return tx.Commit()
}
```

- [ ] **Step 1.7: 改 store_test.go — 全部按新签名重写**

打开 `multi-agent/internal/observerstore/store_test.go`，把所有调用旧签名的地方机械替换：

| 旧 | 新 |
|---|---|
| `st.UpsertWorkspace(Workspace{ID: "ws", Name: "..."})` | （删；不再有 UpsertWorkspace；预置 workspace 改走 `UpsertAPIKey` + `UpsertWorkspaceLazy`） |
| `st.UpsertAgent(Agent{...}, "tok")` | `st.UpsertAgent(Agent{...}, "tok", "ak-test")` |
| `st.LookupAPIKey(key)` 三返回值 | 两返回值 `keyID, ok, err` |
| `st.UpsertAPIKey("ws", "id", "key")` | `st.UpsertAPIKey(APIKeySpec{ID: "id", Key: "key"})` |
| `st.ReplaceAPIKeysForWorkspace("ws", keys)` | `st.ReplaceAPIKeys(keys)` |

每个测试用例顶部加：

```go
require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-test", Key: "test-key"}))
require.NoError(t, st.UpsertWorkspaceLazy("ws-test", "Test", "ak-test"))
```

来构造 FK 父行；不要再依赖 yaml 预声明。

- [ ] **Step 1.8: 跑 store 测，期望全部 PASS**

```bash
cd multi-agent
go test ./internal/observerstore/... -v
```

Expected: 全 PASS。若有断言失败，按错误信息调整 store_test.go——但不要回头改 store.go 的签名。

- [ ] **Step 1.9: 跑整仓 `go build`，期望编译失败但仅在调用点失败**

```bash
cd multi-agent
go build ./...
```

Expected: 报错限定在 `internal/observerweb`、`cmd/observer-server`、`internal/observerclient` 三个调用点（它们仍用旧签名）。这些是 Task 4-7 修。

- [ ] **Step 1.10: Commit**

```bash
cd multi-agent
git add internal/observerstore/schema.sql internal/observerstore/store.go internal/observerstore/store_test.go
git commit -m "refactor(observerstore): top-level api_keys + lazy workspaces + created_by_api_key_id

- workspaces: add created_by_api_key_id + last_seen_at; remove static yaml-driven origin
- api_keys: drop workspace_id; add UNIQUE(key_hash); add note column
- agents: add created_by_api_key_id column (insert-only, not conflict-updated)
- Store signatures: LookupAPIKey returns (keyID, ok, err); UpsertAgent takes apiKeyID;
  UpsertAPIKey takes APIKeySpec; ReplaceAPIKeys replaces ReplaceAPIKeysForWorkspace;
  UpsertWorkspaceLazy replaces UpsertWorkspace (first-write-wins on name).
- Tests rewritten to new signatures.
- observerweb / observer-server / observerclient still broken — fixed in follow-ups."
```

---

## Task 2: TDD — UpsertWorkspaceLazy 先到者定 name + bump last_seen_at

**Goal:** Task 1 已经写了实现；本任务用 TDD 锁定"name 不被后到者覆盖、last_seen_at 每次都 bump"这两条不变量。

**Files:**
- Modify: `multi-agent/internal/observerstore/store_test.go`

### Steps

- [ ] **Step 2.1: 加测试用例**

在 `store_test.go` 中追加：

```go
func TestUpsertWorkspaceLazy_FirstWriterDefinesName(t *testing.T) {
    st := newTestStore(t)
    require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-1", Key: "k1"}))

    require.NoError(t, st.UpsertWorkspaceLazy("ws-x", "First Name", "ak-1"))
    require.NoError(t, st.UpsertWorkspaceLazy("ws-x", "Second Name", "ak-1"))

    var name string
    require.NoError(t, st.db.QueryRow(`SELECT name FROM workspaces WHERE id=?`, "ws-x").Scan(&name))
    require.Equal(t, "First Name", name, "second call must not overwrite name")
}

func TestUpsertWorkspaceLazy_BumpsLastSeen(t *testing.T) {
    st := newTestStore(t)
    require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-1", Key: "k1"}))

    require.NoError(t, st.UpsertWorkspaceLazy("ws-y", "Y", "ak-1"))
    var firstSeen string
    require.NoError(t, st.db.QueryRow(`SELECT last_seen_at FROM workspaces WHERE id=?`, "ws-y").Scan(&firstSeen))

    time.Sleep(10 * time.Millisecond)
    require.NoError(t, st.UpsertWorkspaceLazy("ws-y", "Y", "ak-1"))

    var secondSeen string
    require.NoError(t, st.db.QueryRow(`SELECT last_seen_at FROM workspaces WHERE id=?`, "ws-y").Scan(&secondSeen))
    require.NotEqual(t, firstSeen, secondSeen, "last_seen_at must bump on every upsert")
}
```

`newTestStore(t)` 应该是文件里已有的 helper；如果没有，照着别的测试用例怎么开 store 写一个。

- [ ] **Step 2.2: 跑测**

```bash
cd multi-agent
go test ./internal/observerstore/ -run TestUpsertWorkspaceLazy -v
```

Expected: 两条 PASS（实现已在 Task 1.2 完成）。

- [ ] **Step 2.3: Commit**

```bash
cd multi-agent
git add internal/observerstore/store_test.go
git commit -m "test(observerstore): lock first-writer-defines-name + last_seen bump"
```

---

## Task 3: TDD — RevokeAPIKey(cascade)

**Goal:** 加 store 层 API：`RevokeAPIKey(id, cascade bool)`。`cascade=true` 时连带 DELETE 该 key 注册的 agent 行（依靠 `agents.created_by_api_key_id`）；`cascade=false` 只删 api_key 行。本期 HTTP 不暴露，只供 admin CLI 未来用。

**Files:**
- Modify: `multi-agent/internal/observerstore/store.go`
- Modify: `multi-agent/internal/observerstore/store_test.go`

### Steps

- [ ] **Step 3.1: 写失败测试**

在 `store_test.go` 追加：

```go
func TestRevokeAPIKey_NoCascadeKeepsAgents(t *testing.T) {
    st := newTestStore(t)
    require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-1", Key: "k1"}))
    require.NoError(t, st.UpsertWorkspaceLazy("ws-a", "A", "ak-1"))
    require.NoError(t, st.UpsertAgent(Agent{WorkspaceID: "ws-a", ID: "agent-1", Role: "slave", DisplayName: "S1"}, "tok1", "ak-1"))

    require.NoError(t, st.RevokeAPIKey("ak-1", false))

    // api_keys 行没了
    _, ok, err := st.LookupAPIKey("k1")
    require.NoError(t, err)
    require.False(t, ok)
    // agent 行仍在
    var cnt int
    require.NoError(t, st.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE workspace_id=? AND id=?`, "ws-a", "agent-1").Scan(&cnt))
    require.Equal(t, 1, cnt)
}

func TestRevokeAPIKey_CascadeDeletesAgents(t *testing.T) {
    st := newTestStore(t)
    require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-1", Key: "k1"}))
    require.NoError(t, st.UpsertWorkspaceLazy("ws-a", "A", "ak-1"))
    require.NoError(t, st.UpsertAgent(Agent{WorkspaceID: "ws-a", ID: "agent-1", Role: "slave", DisplayName: "S1"}, "tok1", "ak-1"))
    // 另一把 key 注册一个 agent，确认 cascade 不连带误删
    require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-2", Key: "k2"}))
    require.NoError(t, st.UpsertAgent(Agent{WorkspaceID: "ws-a", ID: "agent-2", Role: "slave", DisplayName: "S2"}, "tok2", "ak-2"))

    require.NoError(t, st.RevokeAPIKey("ak-1", true))

    var cnt1, cnt2 int
    require.NoError(t, st.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id=?`, "agent-1").Scan(&cnt1))
    require.Equal(t, 0, cnt1, "agent created by ak-1 must be deleted")
    require.NoError(t, st.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id=?`, "agent-2").Scan(&cnt2))
    require.Equal(t, 1, cnt2, "agent created by ak-2 must remain")
}
```

- [ ] **Step 3.2: 跑测，期望失败（方法不存在）**

```bash
cd multi-agent
go test ./internal/observerstore/ -run TestRevokeAPIKey -v
```

Expected: FAIL with "st.RevokeAPIKey undefined".

- [ ] **Step 3.3: 实现 RevokeAPIKey**

在 `store.go` 中 `ReplaceAPIKeys` 函数下方追加：

```go
// RevokeAPIKey 从 api_keys 表删 id 行。cascade=true 时同事务删除该 key 注册的
// 所有 agents 行（依赖 agents.created_by_api_key_id）。cascade=false 时已注册的
// agent 保持有效 token，仍可 ingest，直到 admin 手动清理。
func (s *Store) RevokeAPIKey(id string, cascade bool) error {
    if id == "" {
        return errors.New("observerstore: api key id must not be empty")
    }
    tx, err := s.db.Begin()
    if err != nil {
        return err
    }
    defer tx.Rollback() //nolint:errcheck
    if cascade {
        if _, err := tx.Exec(`DELETE FROM agents WHERE created_by_api_key_id=?`, id); err != nil {
            return err
        }
    }
    if _, err := tx.Exec(`DELETE FROM api_keys WHERE id=?`, id); err != nil {
        return err
    }
    return tx.Commit()
}
```

- [ ] **Step 3.4: 跑测，期望 PASS**

```bash
cd multi-agent
go test ./internal/observerstore/ -run TestRevokeAPIKey -v
```

Expected: 两条 PASS。

- [ ] **Step 3.5: Commit**

```bash
cd multi-agent
git add internal/observerstore/store.go internal/observerstore/store_test.go
git commit -m "feat(observerstore): RevokeAPIKey(id, cascade) for admin use"
```

---

## Task 4: observer-server 配置 + main 重写

**Goal:** 让 `cmd/observer-server` 走顶层 `api_keys:` yaml，启动时调 `ReplaceAPIKeys`。删除 `WorkspaceConfig` 类型与相关校验。

**Files:**
- Modify: `multi-agent/cmd/observer-server/main.go:18-115`
- Modify: `multi-agent/cmd/observer-server/main_test.go`
- Modify: `multi-agent/cmd/observer-server/config.example.yaml`

### Steps

- [ ] **Step 4.1: 替换 Config 结构体与解析校验**

在 `main.go` 中找到 `type Config struct { ... }` 与下方 `validateConfig` 风格的校验函数，整体替换为：

```go
type Config struct {
    ListenAddr string         `yaml:"listen_addr"`
    DBPath     string         `yaml:"db_path"`
    APIKeys    []APIKeyConfig `yaml:"api_keys"`
}

type APIKeyConfig struct {
    ID   string `yaml:"id"`
    Key  string `yaml:"key"`
    Note string `yaml:"note,omitempty"`
}

func validateConfig(cfg *Config) error {
    if len(cfg.APIKeys) == 0 {
        return fmt.Errorf("config must define at least one api_keys entry")
    }
    seenID := map[string]bool{}
    for i, k := range cfg.APIKeys {
        if k.ID == "" {
            return fmt.Errorf("api_keys[%d].id is required", i)
        }
        if k.Key == "" {
            return fmt.Errorf("api_keys[%s].key is required", k.ID)
        }
        if seenID[k.ID] {
            return fmt.Errorf("duplicate api_keys.id %s", k.ID)
        }
        seenID[k.ID] = true
    }
    return nil
}
```

（如果文件原先没有显式 `validateConfig`，看 `loadConfig` 里的 inline 检查，把那部分逻辑搬到这个新函数；loadConfig 末尾调 `validateConfig` 即可。）

- [ ] **Step 4.2: 替换 main() 中的 workspace 循环**

找到 `for _, workspace := range cfg.Workspaces { ... }`（大约 50 行）整段替换为：

```go
specs := make([]observerstore.APIKeySpec, 0, len(cfg.APIKeys))
for _, k := range cfg.APIKeys {
    specs = append(specs, observerstore.APIKeySpec{ID: k.ID, Key: k.Key, Note: k.Note})
}
if err := st.ReplaceAPIKeys(specs); err != nil {
    log.Fatal(err)
}
log.Printf("observer-server loaded %d api_keys", len(specs))
```

**注意启动日志：不打印任何 key 或 hash，只打数量与 id 列表（如有需要）**。

- [ ] **Step 4.3: 改 config.example.yaml**

把整个文件替换为：

```yaml
listen_addr: ":8090"
db_path: observer.db

api_keys:
  - id: ak-personal
    key: REPLACE_ME_OPENSSL_RAND_HEX_32
    note: "main operator key"
```

加注释说明 workspace 由 agent 注册时自带，observer 这边不再列 workspaces。

- [ ] **Step 4.4: 改 main_test.go**

把现有所有引用 `Workspaces:` 字段的测试用例改成 `APIKeys:` 字段；删掉测试 `"workspace with no api_keys"` 之类已不适用的 case；加新 case：

```go
{
    name: "config with no api_keys",
    yaml: `
listen_addr: ":8090"
db_path: ":memory:"
api_keys: []
`,
    wantErr: "must define at least one api_keys entry",
},
{
    name: "duplicate api_keys id",
    yaml: `
listen_addr: ":8090"
db_path: ":memory:"
api_keys:
  - { id: dup, key: k1 }
  - { id: dup, key: k2 }
`,
    wantErr: "duplicate api_keys.id dup",
},
{
    name: "empty api_key id",
    yaml: `
listen_addr: ":8090"
db_path: ":memory:"
api_keys:
  - { id: "", key: k1 }
`,
    wantErr: "api_keys[0].id is required",
},
```

- [ ] **Step 4.5: 跑测 + 编译**

```bash
cd multi-agent
go test ./cmd/observer-server/... -v
go build ./cmd/observer-server
```

Expected: 测试 PASS；`go build` 该子包成功。整仓 `go build ./...` 仍会因 observerweb / observerclient 而失败——下一任务修。

- [ ] **Step 4.6: Commit**

```bash
cd multi-agent
git add cmd/observer-server/main.go cmd/observer-server/main_test.go cmd/observer-server/config.example.yaml
git commit -m "feat(observer-server): top-level api_keys config; drop workspaces yaml block

Workspaces are now created lazily by /api/agents/register; the operator only
declares api_keys (no workspace pre-declaration). Validation moved to a single
validateConfig() with explicit error messages for empty/duplicate ids."
```

---

## Task 5: observerweb register handler — workspace_id/name + 409 改绑早检

**Goal:** 重写 `/api/agents/register` 处理函数，按 spec §4.2 流程：先 LookupAPIKey，再校验 workspace_id 格式，再改绑早检，最后单事务里 UpsertWorkspaceLazy + UpsertAgent。

**Files:**
- Modify: `multi-agent/internal/observerweb/server.go:42, 640-740`（interface + register handler + types）
- Modify: `multi-agent/internal/observerweb/server_test.go`

### Steps

- [ ] **Step 5.1: 改 server.go 中的 Store interface**

在文件顶部 `LookupAPIKey(...)` 行（约 42 行）改签名，同时加 `UpsertWorkspaceLazy` 与 `AgentBoundWorkspace`（新增的一个只读查询，由本任务在 store 层补一个 wrapper 或直接用 SQL）：

```go
LookupAPIKey(key string) (keyID string, ok bool, err error)
UpsertWorkspaceLazy(id, name, apiKeyID string) error
AgentBoundWorkspace(agentID string) (workspaceID string, found bool, err error)
UpsertAgent(a observerstore.Agent, token, apiKeyID string) error
```

- [ ] **Step 5.2: 在 store.go 加 AgentBoundWorkspace 查询**

打开 `multi-agent/internal/observerstore/store.go`，在 `UpsertAgent` 上方追加：

```go
// AgentBoundWorkspace 查询某 agentID 当前绑在哪个 workspace。
// found=false 表示该 agentID 还从未注册过。
func (s *Store) AgentBoundWorkspace(agentID string) (string, bool, error) {
    if agentID == "" {
        return "", false, nil
    }
    var ws string
    err := s.db.QueryRow(`SELECT workspace_id FROM agents WHERE id=? LIMIT 1`, agentID).Scan(&ws)
    if err == sql.ErrNoRows {
        return "", false, nil
    }
    if err != nil {
        return "", false, err
    }
    return ws, true, nil
}
```

- [ ] **Step 5.3: 改 server.go 中的 registerRequest / registerResponse**

```go
type registerRequest struct {
    AgentID       string `json:"agent_id"`
    Role          string `json:"role"`
    DisplayName   string `json:"display_name"`
    WorkspaceID   string `json:"workspace_id"`
    WorkspaceName string `json:"workspace_name,omitempty"`
}

type registerResponse struct {
    WorkspaceID string `json:"workspace_id"`
    AgentID     string `json:"agent_id"`
    Role        string `json:"role"`
    DisplayName string `json:"display_name"`
    Token       string `json:"token"`
}
```

- [ ] **Step 5.4: 在 server.go 加 workspaceID 校验正则**

文件靠近顶部的 `agentIDPattern` 旁边加：

```go
var workspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
```

- [ ] **Step 5.5: 重写 register handler**

把 `func (h *handler) register(...)` 整体替换为：

```go
func (h *handler) register(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    apiKey, ok := bearerToken(r.Header.Get("Authorization"))
    if !ok {
        http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
        return
    }
    keyID, ok, err := h.s.LookupAPIKey(apiKey)
    if err != nil {
        log.Printf("observer: LookupAPIKey error: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    if !ok {
        http.Error(w, "invalid api key", http.StatusUnauthorized)
        return
    }

    var req registerRequest
    r.Body = http.MaxBytesReader(w, r.Body, 1<<14)
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields()
    if err := dec.Decode(&req); err != nil {
        http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
        return
    }
    var trailing struct{}
    if err := dec.Decode(&trailing); err != io.EOF {
        http.Error(w, "bad json: trailing content", http.StatusBadRequest)
        return
    }

    if !agentIDPattern.MatchString(req.AgentID) {
        http.Error(w, "agent_id must match [A-Za-z0-9_-]{1,64}", http.StatusBadRequest)
        return
    }
    if !workspaceIDPattern.MatchString(req.WorkspaceID) {
        http.Error(w, "workspace_id must match [A-Za-z0-9_-]{1,64}", http.StatusBadRequest)
        return
    }
    if !validRegisterRole(req.Role) {
        http.Error(w, "role must be one of driver, master, slave", http.StatusBadRequest)
        return
    }
    displayName := req.DisplayName
    if displayName == "" {
        displayName = req.AgentID
    }

    // Step 3 of spec §4.2: 改绑早检（在动数据库前）
    if existing, found, err := h.s.AgentBoundWorkspace(req.AgentID); err != nil {
        log.Printf("observer: AgentBoundWorkspace error: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    } else if found && existing != req.WorkspaceID {
        http.Error(w,
            fmt.Sprintf("agent already bound to workspace %s", existing),
            http.StatusConflict)
        return
    }

    token, err := mintAgentToken()
    if err != nil {
        log.Printf("observer: mintAgentToken error: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }

    // Step 4: lazy upsert workspace + upsert agent.
    // 这里没有显式事务包装：UpsertWorkspaceLazy 与 UpsertAgent 各自单语句已是原子；
    // 步骤间失败时 workspace 行已就位的"轻微孤儿"风险可接受（无 agent 的 workspace
    // 不会被任何 query 看到，下次同 workspace_id 重 register 自动复用）。
    if err := h.s.UpsertWorkspaceLazy(req.WorkspaceID, req.WorkspaceName, keyID); err != nil {
        log.Printf("observer: UpsertWorkspaceLazy error ws=%s: %v", req.WorkspaceID, err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    agent := observerstore.Agent{
        WorkspaceID: req.WorkspaceID,
        ID:          req.AgentID,
        Role:        req.Role,
        DisplayName: displayName,
    }
    if err := h.s.UpsertAgent(agent, token, keyID); err != nil {
        log.Printf("observer: UpsertAgent error ws=%s id=%s: %v", req.WorkspaceID, req.AgentID, err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }

    log.Printf("observer: registered agent ws=%s id=%s role=%s via api_key_id=%s",
        req.WorkspaceID, req.AgentID, req.Role, keyID)

    w.Header().Set("Content-Type", "application/json")
    if err := json.NewEncoder(w).Encode(registerResponse{
        WorkspaceID: req.WorkspaceID,
        AgentID:     agent.ID,
        Role:        agent.Role,
        DisplayName: agent.DisplayName,
        Token:       token,
    }); err != nil {
        log.Printf("observer: encode register response error: %v", err)
    }
}
```

注意 spec §4.2 原写"全部走一个 sqlite 事务"，本实现降级为"早检 + 两个单语句 upsert"，因为：
1. 改绑早检已挡住唯一会引起非预期顺序的场景
2. 两个 upsert 之间崩溃留下的空 workspace 不会被任何业务 query 看到（业务查询都通过 agent token → agents → workspace_id 路径推断 workspace）
3. 跨多个 store 方法做事务需要把 `*sql.Tx` 暴露出去，污染 Store 接口

如果未来需要严格事务，可加 `WithTx(func(*Tx) error)` 包装。本期 YAGNI。

- [ ] **Step 5.5.5: 在 server_test.go 写测试 helper（如果尚不存在）**

现有 `multi-agent/internal/observerweb/server_test.go` 没有 register 相关 helper。在文件靠近顶部、`TestPostEventAuthAndViews` 之前追加：

```go
func newTestHandler(t *testing.T) (http.Handler, *observerstore.Store) {
    t.Helper()
    st, err := observerstore.New(":memory:")
    require.NoError(t, err)
    t.Cleanup(func() { st.Close() })
    return New(st), st
}

func seedAPIKey(t *testing.T, st *observerstore.Store, id, key string) {
    t.Helper()
    require.NoError(t, st.UpsertAPIKey(observerstore.APIKeySpec{ID: id, Key: key}))
}

func postRegister(t *testing.T, h http.Handler, apiKey, jsonBody string, wantStatus int) string {
    t.Helper()
    req := httptest.NewRequest(http.MethodPost, "/api/agents/register", strings.NewReader(jsonBody))
    req.Header.Set("Authorization", "Bearer "+apiKey)
    req.Header.Set("Content-Type", "application/json")
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, req)
    require.Equal(t, wantStatus, rr.Code, "body=%s", rr.Body.String())
    return rr.Body.String()
}

func extractToken(t *testing.T, body string) string {
    t.Helper()
    var resp struct {
        Token string `json:"token"`
    }
    require.NoError(t, json.Unmarshal([]byte(body), &resp))
    require.NotEmpty(t, resp.Token)
    return resp.Token
}
```

`require` import 为 `github.com/stretchr/testify/require`；如尚未 import 加上。
对 `st.QueryRowForTest(...)`：observerstore 的 `Store.db` 字段是私有（小写）。本期 plan 内的所有断言改走公开方法（例如检查 workspaces 是否存在用 `ListWorkspaceSummaries`；检查 agents 用 `ValidateToken` 或 `AgentBoundWorkspace`）。如果实在需要直查 SQL，在 store_test.go 已位于同包内可直接 `st.db.QueryRow(...)`；observerweb 测试不能跨包访问私有字段——这是为什么 helper 用公开方法的原因。

- [ ] **Step 5.6: 改测试 — 覆盖 workspace_id 缺失、改绑、name 不覆盖、token 旋转**

打开 `multi-agent/internal/observerweb/server_test.go`，把所有调 `/api/agents/register` 的测试用例补 `workspace_id`；新增：

```go
func TestRegister_RejectsMissingWorkspaceID(t *testing.T) {
    h, _ := newTestHandler(t)
    // ... POST {"agent_id":"a","role":"slave"} without workspace_id ...
    // expect 400 with "workspace_id must match"
}

func TestRegister_RejectsRebinding(t *testing.T) {
    h, st := newTestHandler(t)
    seedAPIKey(t, st, "ak-1", "key1")
    // first register to ws-A
    postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
    // second register same agent to ws-B → 409
    resp := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-B"}`, http.StatusConflict)
    require.Contains(t, resp, "already bound to workspace ws-A")
}

func TestRegister_NameStickyOnSecondRegister(t *testing.T) {
    h, st := newTestHandler(t)
    seedAPIKey(t, st, "ak-1", "key1")
    postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-X","workspace_name":"First"}`, http.StatusOK)
    postRegister(t, h, "key1", `{"agent_id":"b","role":"slave","workspace_id":"ws-X","workspace_name":"Second"}`, http.StatusOK)
    sums, err := st.ListWorkspaceSummaries()
    require.NoError(t, err)
    require.Len(t, sums, 1)
    require.Equal(t, "First", sums[0].Name, "first writer must win the name")
}

func TestRegister_RebindRejectedDoesNotCreateOrphanWorkspace(t *testing.T) {
    h, st := newTestHandler(t)
    seedAPIKey(t, st, "ak-1", "key1")
    postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
    postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-LEAK"}`, http.StatusConflict)
    sums, err := st.ListWorkspaceSummaries()
    require.NoError(t, err)
    for _, s := range sums {
        require.NotEqual(t, "ws-LEAK", s.ID, "rejected rebind must not create the workspace")
    }
}

func TestRegister_TokenRotation(t *testing.T) {
    h, st := newTestHandler(t)
    seedAPIKey(t, st, "ak-1", "key1")
    body1 := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
    body2 := postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-A"}`, http.StatusOK)
    require.NotEqual(t, extractToken(t, body1), extractToken(t, body2))
    // old token must no longer ValidateToken
    _, ok, err := st.ValidateToken(extractToken(t, body1))
    require.NoError(t, err)
    require.False(t, ok, "old token must be invalidated after rotation")
}
```

`postRegister` / `seedAPIKey` / `extractToken` 是测试 helper；按 server_test.go 现有风格写或复用。

- [ ] **Step 5.7: 跑测 + 编译**

```bash
cd multi-agent
go test ./internal/observerweb/... -v
go build ./...
```

Expected: observerweb 测试全 PASS；整仓编译仅剩 `internal/observerclient` 失败（仍用旧签名）。

- [ ] **Step 5.8: Commit**

```bash
cd multi-agent
git add internal/observerstore/store.go internal/observerweb/server.go internal/observerweb/server_test.go
git commit -m "feat(observerweb): register handler accepts workspace_id/name + 409 on rebind

- Adds Store.AgentBoundWorkspace for early-check before any write.
- registerRequest now requires workspace_id; workspace_name optional, first-writer wins.
- Same agent_id re-registered to different workspace_id returns 409; the workspace
  row is NOT created in that case (early check happens before lazy upsert).
- Same agent_id re-registered to same workspace_id rotates the token.
- Tests cover all four cases plus orphan-workspace prevention."
```

---

## Task 6: observerweb ListWorkspaces handler + 可选 OBSERVER_WEB_TOKEN 守护

**Goal:** 加 `GET /api/workspaces` 路由，返回所有 workspace 的概览（id, name, last_seen_at, agent_count, recent_event_at）。

**Files:**
- Modify: `multi-agent/internal/observerstore/store.go` (新 `ListWorkspaceSummaries`)
- Modify: `multi-agent/internal/observerstore/store_test.go`
- Modify: `multi-agent/internal/observerweb/server.go` (新 handler + interface 方法 + env-guard middleware)
- Modify: `multi-agent/internal/observerweb/server_test.go`

### Steps

- [ ] **Step 6.1: 写 store 失败测试**

```go
func TestListWorkspaceSummaries(t *testing.T) {
    st := newTestStore(t)
    require.NoError(t, st.UpsertAPIKey(APIKeySpec{ID: "ak-1", Key: "k1"}))
    require.NoError(t, st.UpsertWorkspaceLazy("ws-a", "A", "ak-1"))
    require.NoError(t, st.UpsertWorkspaceLazy("ws-b", "B", "ak-1"))
    require.NoError(t, st.UpsertAgent(Agent{WorkspaceID: "ws-a", ID: "ag1", Role: "slave", DisplayName: "S"}, "t1", "ak-1"))
    require.NoError(t, st.UpsertAgent(Agent{WorkspaceID: "ws-a", ID: "ag2", Role: "driver", DisplayName: "D"}, "t2", "ak-1"))

    sums, err := st.ListWorkspaceSummaries()
    require.NoError(t, err)
    require.Len(t, sums, 2)
    // Expect order by last_seen_at DESC; ws-b was upserted later
    require.Equal(t, "ws-b", sums[0].ID)
    require.Equal(t, "ws-a", sums[1].ID)
    require.Equal(t, 2, sums[1].AgentCount)
    require.Equal(t, 0, sums[0].AgentCount)
}
```

- [ ] **Step 6.2: 跑测，期望失败**

```bash
cd multi-agent
go test ./internal/observerstore/ -run TestListWorkspaceSummaries -v
```

Expected: FAIL with "undefined: ListWorkspaceSummaries".

- [ ] **Step 6.3: 在 store.go 加类型 + 方法**

```go
type WorkspaceSummary struct {
    ID            string `json:"id"`
    Name          string `json:"name"`
    LastSeenAt    string `json:"last_seen_at"`
    AgentCount    int    `json:"agent_count"`
    RecentEventAt string `json:"recent_event_at,omitempty"`
}

func (s *Store) ListWorkspaceSummaries() ([]WorkspaceSummary, error) {
    rows, err := s.db.Query(`
        SELECT w.id, w.name, w.last_seen_at,
               COALESCE((SELECT COUNT(*) FROM agents a WHERE a.workspace_id = w.id), 0),
               COALESCE((SELECT MAX(ts) FROM events e WHERE e.workspace_id = w.id), '')
          FROM workspaces w
         ORDER BY w.last_seen_at DESC
    `)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []WorkspaceSummary
    for rows.Next() {
        var ws WorkspaceSummary
        if err := rows.Scan(&ws.ID, &ws.Name, &ws.LastSeenAt, &ws.AgentCount, &ws.RecentEventAt); err != nil {
            return nil, err
        }
        out = append(out, ws)
    }
    return out, rows.Err()
}
```

- [ ] **Step 6.4: 跑测，期望 PASS**

```bash
cd multi-agent
go test ./internal/observerstore/ -run TestListWorkspaceSummaries -v
```

Expected: PASS.

- [ ] **Step 6.5: server.go 加 interface 方法 + handler + 路由**

在 Store interface 加：

```go
ListWorkspaceSummaries() ([]observerstore.WorkspaceSummary, error)
```

在路由注册段（约 50 行）加：

```go
mux.HandleFunc("/api/workspaces", h.guardWebToken(h.listWorkspaces))
```

在 `mintAgentToken` 同区域加 handler：

```go
func (h *handler) listWorkspaces(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    sums, err := h.s.ListWorkspaceSummaries()
    if err != nil {
        log.Printf("observer: ListWorkspaceSummaries error: %v", err)
        http.Error(w, "internal", http.StatusInternalServerError)
        return
    }
    if sums == nil {
        sums = []observerstore.WorkspaceSummary{}
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(sums)
}
```

- [ ] **Step 6.6: 加 OBSERVER_WEB_TOKEN 守护中间件**

```go
// guardWebToken 仅当环境变量 OBSERVER_WEB_TOKEN 非空时启用：要求请求带
// ?web_token=<...> 或 X-Observer-Web-Token header；缺省（env 空）则透传。
func (h *handler) guardWebToken(next http.HandlerFunc) http.HandlerFunc {
    want := os.Getenv("OBSERVER_WEB_TOKEN")
    if want == "" {
        return next
    }
    return func(w http.ResponseWriter, r *http.Request) {
        got := r.Header.Get("X-Observer-Web-Token")
        if got == "" {
            got = r.URL.Query().Get("web_token")
        }
        if got != want {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
        next(w, r)
    }
}
```

import 加 `"os"` 如尚未 import。

- [ ] **Step 6.7: 写 handler 测试**

在 `server_test.go` 追加：

```go
func TestListWorkspaces_HappyPath(t *testing.T) {
    h, st := newTestHandler(t)
    seedAPIKey(t, st, "ak-1", "key1")
    postRegister(t, h, "key1", `{"agent_id":"a","role":"slave","workspace_id":"ws-1","workspace_name":"One"}`, http.StatusOK)
    postRegister(t, h, "key1", `{"agent_id":"b","role":"slave","workspace_id":"ws-2","workspace_name":"Two"}`, http.StatusOK)

    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
    require.Equal(t, http.StatusOK, rr.Code)

    var got []observerstore.WorkspaceSummary
    require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &got))
    require.Len(t, got, 2)
    require.Equal(t, "ws-2", got[0].ID) // last_seen DESC
}

func TestListWorkspaces_WebTokenGuard(t *testing.T) {
    t.Setenv("OBSERVER_WEB_TOKEN", "secret")
    h, _ := newTestHandler(t)
    // missing → 401
    rr1 := httptest.NewRecorder()
    h.ServeHTTP(rr1, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
    require.Equal(t, http.StatusUnauthorized, rr1.Code)
    // wrong → 401
    req2 := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
    req2.Header.Set("X-Observer-Web-Token", "nope")
    rr2 := httptest.NewRecorder()
    h.ServeHTTP(rr2, req2)
    require.Equal(t, http.StatusUnauthorized, rr2.Code)
    // right → 200
    req3 := httptest.NewRequest(http.MethodGet, "/api/workspaces?web_token=secret", nil)
    rr3 := httptest.NewRecorder()
    h.ServeHTTP(rr3, req3)
    require.Equal(t, http.StatusOK, rr3.Code)
}
```

`t.Setenv` 是 Go 1.17+ 标准做法。

- [ ] **Step 6.8: 跑测**

```bash
cd multi-agent
go test ./internal/observerweb/... -v
```

Expected: 全 PASS。

- [ ] **Step 6.9: Commit**

```bash
cd multi-agent
git add internal/observerstore/store.go internal/observerstore/store_test.go internal/observerweb/server.go internal/observerweb/server_test.go
git commit -m "feat(observerweb): GET /api/workspaces + optional OBSERVER_WEB_TOKEN guard

ListWorkspaceSummaries returns id/name/last_seen_at/agent_count/recent_event_at
ordered by last_seen DESC. The web token guard activates only when the env var
is set, keeping the default single-user local case prompt-free."
```

---

## Task 7: observerclient register payload 加 workspace_id + workspace_name

**Goal:** 让 observerclient 在 `loadOrRegister` 时把 `Config.WorkspaceID` + `Config.WorkspaceName` 透传给服务端，否则 Task 5 之后所有 agent 都会收到 400。

**Files:**
- Modify: `multi-agent/internal/observerclient/client.go:24-31`（Config struct）
- Modify: `multi-agent/internal/observerclient/bootstrap.go:50-100`（register payload + 函数签名）
- Modify: `multi-agent/internal/observerclient/bootstrap_test.go`

### Steps

- [ ] **Step 7.1: 在 Config 加 WorkspaceName**

`client.go`：

```go
type Config struct {
    Enabled        bool
    URL            string
    WorkspaceID    string
    WorkspaceName  string // optional; first-writer-wins at observer
    AgentID        string
    AgentRole      string
    APIKey         string
    TokenStatePath string
}
```

- [ ] **Step 7.2: 改 bootstrap.go register payload + 函数签名**

替换 `registerRequest`：

```go
type registerRequest struct {
    AgentID       string `json:"agent_id"`
    Role          string `json:"role"`
    DisplayName   string `json:"display_name"`
    WorkspaceID   string `json:"workspace_id"`
    WorkspaceName string `json:"workspace_name,omitempty"`
}
```

替换 `register(...)` 函数签名 + body 构造：

```go
func register(
    ctx context.Context,
    httpc *http.Client,
    baseURL, apiKey, agentID, role, displayName, workspaceID, workspaceName string,
) (token, returnedWorkspaceID string, err error) {
    body, _ := json.Marshal(registerRequest{
        AgentID:       agentID,
        Role:          role,
        DisplayName:   displayName,
        WorkspaceID:   workspaceID,
        WorkspaceName: workspaceName,
    })
    // ... rest unchanged ...
}
```

- [ ] **Step 7.3: 改 loadOrRegister 的 register 调用**

在 `loadOrRegister` 里找到 `register(regCtx, c.http, ...)` 这行，加传 `c.cfg.WorkspaceID, c.cfg.WorkspaceName`：

```go
token, ws, err := register(regCtx, c.http,
    c.cfg.URL, c.cfg.APIKey,
    c.cfg.AgentID, c.cfg.AgentRole, c.cfg.AgentID, // displayName fallback unchanged
    c.cfg.WorkspaceID, c.cfg.WorkspaceName,
)
```

（具体的 displayName 取值看现有代码用 cfg 哪个字段；按现状保留。）

如果 loadOrRegister 内有 `if ws != c.cfg.WorkspaceID { return "", fmt.Errorf(...) }` 之类的 cross-check，保留即可（server 现在仍 echo workspace_id，cross-check 仍合理）。

- [ ] **Step 7.4: 改 bootstrap_test.go**

打开 `multi-agent/internal/observerclient/bootstrap_test.go`，把所有 `register(...)` 调用补 `workspaceID, workspaceName` 两个参数；新加：

```go
func TestRegister_SendsWorkspaceFieldsInBody(t *testing.T) {
    var gotBody []byte
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotBody, _ = io.ReadAll(r.Body)
        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(`{"token":"t1","workspace_id":"ws-x","agent_id":"a","role":"slave","display_name":"a"}`))
    }))
    defer srv.Close()

    _, _, err := register(context.Background(), http.DefaultClient,
        srv.URL, "apikey", "a", "slave", "a", "ws-x", "Test Name")
    require.NoError(t, err)
    require.JSONEq(t,
        `{"agent_id":"a","role":"slave","display_name":"a","workspace_id":"ws-x","workspace_name":"Test Name"}`,
        string(gotBody))
}
```

- [ ] **Step 7.5: 跑测 + 整仓编译**

```bash
cd multi-agent
go test ./internal/observerclient/... -v
go build ./...
```

Expected: observerclient 测试 PASS；整仓编译错误现在转到 `cmd/{driver,master,slave}-agent` 三处对 `observerclient.Config{...}` 的初始化没传 WorkspaceName——下一任务修。如果三个 agent 还能编译过（因为 WorkspaceName 是新字段、不传默认零值），那么本任务的 build 已经完整通过；如此就更好。

- [ ] **Step 7.6: Commit**

```bash
cd multi-agent
git add internal/observerclient/client.go internal/observerclient/bootstrap.go internal/observerclient/bootstrap_test.go
git commit -m "feat(observerclient): include workspace_id (required) + workspace_name (optional) in register payload"
```

---

## Task 8: 三个 agent 透传 WorkspaceName + yaml 示例更新

**Goal:** 让 driver / master / slave 三个 agent 的 yaml 都支持 `observer.workspace_name`（可选），并把它透传到 `observerclient.Config`。

**Files:**
- Modify: `multi-agent/internal/driver/config.go:81-90`（driver-agent 的 `type Observer struct`，line ~84 是 `WorkspaceID`）
- Modify: `multi-agent/internal/config/config.go:83-92`（master-agent + slave-agent 共用的 `type Observer struct`，line ~86 是 `WorkspaceID`）
- Modify: `multi-agent/cmd/driver-agent/main.go:144-153`
- Modify: `multi-agent/cmd/master-agent/main.go:78-86`
- Modify: `multi-agent/cmd/slave-agent/main.go:195-204`
- Modify: `multi-agent/cmd/driver-agent/config.example.yaml`
- Modify: `multi-agent/cmd/master-agent/config.example.yaml`
- Modify: `multi-agent/cmd/slave-agent/config.example.yaml`

### Steps

- [ ] **Step 8.1: 给两个 Observer config struct 加 WorkspaceName 字段**

`multi-agent/internal/driver/config.go` 在 `type Observer struct` 内、`WorkspaceID string` 字段后加：

```go
WorkspaceName  string `yaml:"workspace_name,omitempty"`
```

`multi-agent/internal/config/config.go` 同样位置同样加。两文件 `type Observer struct` 互独立，需要分别改。

- [ ] **Step 8.2: 在三个 agent main.go 透传 WorkspaceName**

每个文件找到 `observerclient.New(observerclient.Config{...})`，在 `WorkspaceID: cfg.Observer.WorkspaceID,` 下方加：

```go
WorkspaceName: cfg.Observer.WorkspaceName,
```

涉及行（参考 Task 7 前置阅读）：
- `cmd/driver-agent/main.go:148` 后插
- `cmd/master-agent/main.go:81` 后插
- `cmd/slave-agent/main.go:199` 后插

- [ ] **Step 8.3: 改三个 config.example.yaml**

每个 yaml 的 `observer:` 段加注释 + 字段：

```yaml
observer:
  enabled: true
  url: http://observer.local:8090
  workspace_id: ws-personal              # required; observer 端 lazy 建
  workspace_name: "Personal Lab"          # optional; 仅首次创建该 workspace 时被记入
  agent_id: slave-jetson-01
  api_key: REPLACE_ME
  token_state_path: /var/lib/slave-agent/observer-token.json
```

- [ ] **Step 8.4: 编译三个 agent**

```bash
cd multi-agent
go build ./cmd/driver-agent ./cmd/master-agent ./cmd/slave-agent
```

Expected: 全部 build 通过。

- [ ] **Step 8.5: 跑整仓测试**

```bash
cd multi-agent
go test ./...
```

Expected: 全 PASS。

- [ ] **Step 8.6: Commit**

```bash
cd multi-agent
git add cmd/driver-agent cmd/master-agent cmd/slave-agent internal/driver internal/master internal/slave
git commit -m "feat(agents): pass optional observer.workspace_name to observerclient

Each agent yaml grows an optional workspace_name alongside the existing
workspace_id; main.go forwards it to observerclient.Config. observer's
register handler records the name only on first creation of that workspace."
```

（实际 `git add` 路径按 step 8.1 grep 找到的为准；如果 config struct 在 internal/ 下，需要带上。）

---

## Task 9: e2e 集成 + 手工验证清单

**Goal:** 起一个内存 observer + 三个虚拟 agent（同进程协程，各带不同 workspace_id），验证：注册成功 → events 互不串台 → `/api/workspaces` 列出全部。

**Files:**
- Create / Modify: `multi-agent/internal/observerweb/server_test.go`（追加 e2e 风格用例；不新建文件）

### Steps

- [ ] **Step 9.1: 写 e2e 用例**

```go
func TestE2E_MultiWorkspaceIsolation(t *testing.T) {
    h, st := newTestHandler(t)
    seedAPIKey(t, st, "ak-1", "key1")

    // 三个 agent 注册到两个 workspace
    bodyA := postRegister(t, h, "key1", `{"agent_id":"driver-A","role":"driver","workspace_id":"ws-personal","workspace_name":"Personal"}`, http.StatusOK)
    bodyB := postRegister(t, h, "key1", `{"agent_id":"slave-A","role":"slave","workspace_id":"ws-personal"}`, http.StatusOK)
    bodyC := postRegister(t, h, "key1", `{"agent_id":"slave-W","role":"slave","workspace_id":"ws-work","workspace_name":"Work"}`, http.StatusOK)
    tokA, tokB, tokC := extractToken(t, bodyA), extractToken(t, bodyB), extractToken(t, bodyC)

    // 各自发一个事件
    ingestEvent(t, h, tokA, "ws-personal", "driver-A", "driver", observer.EventDriverTaskSubmitted, "personal-task-1")
    ingestEvent(t, h, tokB, "ws-personal", "slave-A", "slave", observer.EventSlaveTaskStarted, "personal-task-1")
    ingestEvent(t, h, tokC, "ws-work", "slave-W", "slave", observer.EventSlaveTaskStarted, "work-task-1")

    // 列 workspaces，期望两条
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workspaces", nil))
    require.Equal(t, http.StatusOK, rr.Code)
    var sums []observerstore.WorkspaceSummary
    require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &sums))
    require.Len(t, sums, 2)

    // 通过公开方法验证事件隔离：listEvents 按 workspace_id 过滤
    // （Store 已有 ListEventsByWorkspace 之类的方法；若名字不同按文件中实际方法调用）
    personalEvents := listEventsForWorkspace(t, st, "ws-personal")
    workEvents := listEventsForWorkspace(t, st, "ws-work")
    require.Len(t, personalEvents, 2)
    require.Len(t, workEvents, 1)
}

// listEventsForWorkspace 是测试 helper。如果 observerstore 已有同语义公开方法
// （例如 ListEventsByWorkspace），直接调用；否则在 store.go 加一个最小读方法
// `func (s *Store) ListEventsForWorkspace(wsID string) ([]Event, error)`，配套
// 在 store_test.go 加一条覆盖。**不要**为了测试在 observerweb 包内 reach 到
// st.db 私有字段。
```

`ingestEvent` 是测试 helper。如果文件里没有现成的，参照 `TestPostEventAuthAndViews`（observerweb/server_test.go 顶部）那一段构造 events JSON + POST `/api/events` with `Authorization: Bearer <token>` 的写法，提炼成：

```go
func ingestEvent(t *testing.T, h http.Handler, token, wsID, agentID, role, eventType, taskID string) {
    t.Helper()
    body, _ := json.Marshal(observer.Event{
        WorkspaceID: wsID, AgentID: agentID, AgentRole: role,
        Type: eventType, TaskID: taskID,
    })
    req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, req)
    require.Equal(t, http.StatusOK, rr.Code, "ingest body=%s", rr.Body.String())
}
```

- [ ] **Step 9.2: 跑 e2e**

```bash
cd multi-agent
go test ./internal/observerweb/ -run TestE2E_MultiWorkspaceIsolation -v
```

Expected: PASS。

- [ ] **Step 9.3: 手工验证（本地起服务）**

```bash
cd multi-agent
# 备份现有 .db（如有）
[ -f observer.db ] && mv observer.db observer.db.bak.$(date +%s)

# 起 observer（用一份临时 yaml）
cat >/tmp/observer-test.yaml <<'YAML'
listen_addr: ":18090"
db_path: ":memory:"
api_keys:
  - id: ak-test
    key: testkey123
    note: "manual smoke test"
YAML
go run ./cmd/observer-server -config /tmp/observer-test.yaml &
SERVER_PID=$!
sleep 1

# 注册一个 agent
curl -fsS -X POST http://localhost:18090/api/agents/register \
  -H "Authorization: Bearer testkey123" \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"smoke-1","role":"slave","workspace_id":"ws-smoke","workspace_name":"Smoke"}'
echo

# 重复注册同 agent 到不同 workspace → 期望 409
curl -fsS -X POST http://localhost:18090/api/agents/register \
  -H "Authorization: Bearer testkey123" \
  -H "Content-Type: application/json" \
  -d '{"agent_id":"smoke-1","role":"slave","workspace_id":"ws-other"}' \
  -w "\nHTTP %{http_code}\n" || true

# 列 workspaces
curl -fsS http://localhost:18090/api/workspaces
echo

kill $SERVER_PID
```

Expected:
- 第 1 个 curl 返回 200 + token
- 第 2 个 curl 返回 409 + "agent already bound to workspace ws-smoke"
- 第 3 个 curl 返回 `[{"id":"ws-smoke","name":"Smoke",...}]`，**不应**含 `ws-other`（被 409 拒了，无孤儿）

- [ ] **Step 9.4: Commit**

```bash
cd multi-agent
git add internal/observerweb/server_test.go
git commit -m "test(observerweb): e2e multi-workspace isolation + workspaces listing"
```

---

## Self-review checklist

完成全部 Task 后做这些核对：

1. **整仓绿**：`cd multi-agent && go test ./... && go build ./...` 全 PASS
2. **schema 一致**：手工跑 `sqlite3 observer.db ".schema workspaces"` `".schema api_keys"` `".schema agents"`，与 Task 1.1 给的三段一字不差
3. **无残留 API**：`grep -rn "ReplaceAPIKeysForWorkspace\|UpsertWorkspace[^L]" multi-agent/` 应该 0 命中
4. **无残留 yaml workspaces 块**：`grep -rn "^workspaces:" multi-agent/cmd/observer-server/` 应该 0 命中
5. **跨 workspace 隔离**：跑 `TestE2E_MultiWorkspaceIsolation` 单条
6. **手工 smoke 三个 curl 都符合预期**
7. **Spec §11 风险项 #2"observer 启动日志当前会打 api_key 数量；务必不打印 key 本身或 hash"**：`grep -nE "key_hash|cfg.APIKeys\[.*\]\.Key" multi-agent/cmd/observer-server/main.go` 不应在 `log.Print` 里出现

如发现 spec 与实现有 drift，回头改 spec 或调实现，不要"先记着以后再说"。

---

## 后续

- 看板 navbar workspace 切换器（前端）独立 plan
- userspace 折叠 spec 改写：本 plan 落地后，按 spec §7 表把 `2026-05-25-personal-mcp-skill-space-design.md` 重写为 "userspace as observer extension"；ROADMAP 把旧 spec 标 superseded
- admin CLI `cmd/observer-admin`（`RevokeAPIKey`、`workspace prune` 等）独立 plan
