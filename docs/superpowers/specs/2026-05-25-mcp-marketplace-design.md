# MCP Server 市场设计

**日期**: 2026-05-25
**状态**: 设计已确认，待写实施 plan
**思想根**: `docs/算力封装的价值.md` 方案 A（能力卡 + 语义匹配器）+ 方案 C（双向翻译层）
**关系**: 在现有 `register_slave_mcp` / `scaffold-mcp-server` / `mcp-acceptance` 链路前**增加一个起点**——从空白起步变为"从市场已有 MCP 起步"。**现有链路一行不改**。

---

## 0. 目标与非目标

**目标**
- 把 MCP server 当作"算力封装的可流通对象"——带源码 + 能力卡（软件依赖 + 硬件需求 + SLA hint）发布、被搜到、被拿来重构再注册
- 让 driver 用户能自然语言搜索类似 MCP，pull 到本地、看 diff、决定是否复用或重构
- 提供发布通路：publisher 上传带签名的包，admin 审核后入库
- **市场不一定安全** → 用户确认是硬闸：双闸（publisher ed25519 签名 + driver 端 diff/approve）

**非目标（本期）**
- 自助注册 publisher（OAuth/邮箱验证等）—— **未来必做，本期手工 onboarding**；schema 与代码接口为它预留位置
- Registry 端跑 LLM 重构（remix）—— 重构完全在 driver 本地
- 水平扩展架构（PG / S3 / 拆服务）—— SQLite + 本地 FS 单体起步，接口预留以便后期迁移
- 复杂计量/账单/审计 dashboard —— admin 用 CLI

---

## 1. 决策摘要

| 维度 | 决策 |
|---|---|
| 市场载体 | 中心化 Registry HTTP 服务（新写） |
| 搜索 | 自然语言 → embedding 语义检索（FTS5 兜底） |
| 重构 | Driver 本地 fork + 手改 + `scaffold-mcp-server` 重跑（市场不掺和） |
| 能力卡 | 混合两层：结构化 `manifest.json` + 开放式 `capability_card.md` |
| 信任 | 双闸：publisher ed25519 签名 + driver 端 diff/Y-n（同 `source_hash` 复用免重审） |
| 范围 | 读写都做：search + fetch + publish + 审核 + 签名 |
| 架构 | 单体 Go 二进制 + SQLite-vec + 本地 FS（接口化以便后期迁 PG/S3） |
| Publisher 验证（本期） | Admin 手工 onboarding；publisher 离线生成 ed25519 keypair；带外提交 pubkey；admin 录入并发 token |

---

## 2. 系统组件总图

```
┌────────────────────── Driver (Claude Code)  ──────────────────────┐
│  MCP tools (new):                                                 │
│   - search_mcp_market(query) → top-K cards                        │
│   - pull_mcp_package(slug, version) → driver fork + diff + flags  │
│   - approve_mcp_package(slug, version, source_hash)               │
│                                                                   │
│  Then existing flow (unchanged):                                  │
│   scaffold-mcp-server (re-run after edit) → mcp-acceptance        │
│   → register_slave_mcp                                            │
└──────────────────────────────┬────────────────────────────────────┘
                               │ HTTPS (Bearer for publish/admin only)
                               ▼
┌────────────────────── cmd/mcp-registry (single Go binary) ─────────┐
│  HTTP API (chi router):                                            │
│    GET  /v1/search?q=...&limit=10                                  │
│    GET  /v1/packages/{slug}                                        │
│    GET  /v1/packages/{slug}/versions/{ver}                         │
│    GET  /v1/packages/{slug}/versions/{ver}/source.tar.gz           │
│    GET  /v1/publishers/{id}/pubkey                                 │
│    POST /v1/packages                          (publisher token)    │
│    POST /v1/admin/publishers                  (admin token)        │
│    POST /v1/admin/review/{slug}/{ver}         (admin token)        │
│    POST /v1/admin/yank/{slug}/{ver}           (admin token)        │
│                                                                    │
│  Internals:                                                        │
│    MetaStore (SQLite + sqlite-vec + FTS5)                          │
│    BlobStore (fs sha256-addressed)                                 │
│    EmbeddingProvider (iface; default=HTTP call; nil → FTS5)        │
│    SigVerifier (ed25519)                                           │
│    ReviewQueue (publish → pending → approve/reject)                │
│    PublisherOnboarder (iface; MVP=ManualOnboarder)                 │
└────────────────────────────────────────────────────────────────────┘

     + cmd/mcp-publish (publisher CLI, separate binary)
     + cmd/mcp-admin   (admin CLI, separate binary)
```

**关键边界**
- Registry 不懂 Python、不跑代码、不做 LLM 推理。它只做：元数据存取 / blob 寻址 / embedding 写入 / 签名验证 / 审核队列
- LLM 只在两条冷路径出现：① publish approve 时由 registry 调 `EmbeddingProvider` 算一次 embedding；② driver 侧 search 拿回 top-K 后用本地 Claude 重排（可选）。热路径——fetch / 验签 / blob 下载——零 LLM
- driver 不直接拿市场源码注册——必须经过 fork 区 + `scaffold-mcp-server` 重跑 + `mcp-acceptance`，保留所有现有保护
- 签名 = publisher 上传时用自己的 ed25519 私钥对 `tarball_sha256 || canonical(manifest_without_sig)` 签名；driver 拉到包后用 registry 公布的 pubkey 验签
- 双闸独立：签名只保证"是这个 publisher 发的"；diff/approve 才保证"内容看过、确认安全"。**首装必走 diff**，同 `source_hash` 复用免重审

---

## 3. 包格式

每个市场包是一个确定性打包的 tar.gz：

```
mcp-package-<slug>-<version>.tar.gz
├── manifest.json              ← 结构化能力卡（机器可校）
├── capability_card.md         ← 开放式能力卡（语义检索 + 给人读）
├── spec.json                  ← 直接喂给 register_mcp 的 buildspec.Spec
├── src/
│   └── server.py              ← 主入口（path 由 manifest.entrypoint 声明）
├── tests/
│   └── cases.json             ← mcp-acceptance 用的样例
└── README.md
```

**确定性打包**：所有文件 `mtime=0, uid=gid=0`，文件名按字典序入包——同一份源码每次打包 `sha256` 都一样，CI 里可重现。

---

## 4. 数据模型

### 4.1 `manifest.json` schema

```json
{
  "schema_version": 1,
  "slug": "wedding_almanac",
  "version": "1.0.0",
  "publisher_id": "alice@labs",
  "tarball_sha256": "<填写后自校>",
  "signature": "<ed25519 over (tarball_sha256 || canonical(manifest_without_sig))>",

  "spec_ref": "spec.json",
  "entrypoint": "src/server.py",
  "card_ref": "capability_card.md",
  "cases_ref": "tests/cases.json",

  "software": {
    "python": ">=3.10,<3.13",
    "packages": []
  },
  "hardware": {
    "min_ram_mb": 128,
    "gpu_class": null,
    "network_egress": ["api.example.com"]
  },
  "sla_hint": {
    "latency_p99_ms": 800,
    "warmup_ms": 0
  },
  "tags": ["divination", "calendar", "zh-cn"],
  "license": "MIT",
  "created_at": "2026-05-25T..."
}
```

**两个 "version" 别混**：
- `manifest.version`（字符串，semver）= 市场版本
- driver fork 后 `spec.json.version`（整数）= 本地版本，首次设 1，重构后自增

### 4.2 SQLite schema

```sql
CREATE TABLE publishers (
  id              TEXT PRIMARY KEY,         -- 'alice@labs'
  pubkey_ed25519  BLOB NOT NULL,
  approved        INTEGER NOT NULL DEFAULT 0,
  trust_level     TEXT NOT NULL DEFAULT 'manual',  -- 预留: 'manual'|'verified'|'community'
  auth_method     TEXT NOT NULL DEFAULT 'manual',  -- 预留: 'manual'|'github_oauth'|...
  created_at      TEXT NOT NULL
);

CREATE TABLE packages (
  slug            TEXT PRIMARY KEY,
  current_version TEXT,                     -- 最新通过审核的 semver
  description     TEXT,                     -- 从最新 spec.description 拷贝
  tags_json       TEXT
);

CREATE TABLE package_versions (
  slug            TEXT NOT NULL,
  version         TEXT NOT NULL,
  publisher_id    TEXT NOT NULL,
  status          TEXT NOT NULL,            -- 'pending'|'approved'|'yanked'|'rejected'
  manifest_json   TEXT NOT NULL,
  spec_json       TEXT NOT NULL,
  card_md         TEXT NOT NULL,
  tarball_sha256  TEXT NOT NULL,
  signature       BLOB NOT NULL,
  blob_path       TEXT NOT NULL,            -- 相对 blob root, sha256 寻址
  created_at      TEXT NOT NULL,
  PRIMARY KEY (slug, version)
);

CREATE TABLE review_queue (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  slug            TEXT NOT NULL,
  version         TEXT NOT NULL,
  submitted_at    TEXT NOT NULL,
  notes           TEXT,                     -- 服务端预扫风险提示
  reviewed_at     TEXT,
  decision        TEXT,                     -- 'approved'|'rejected'
  reason          TEXT
);

CREATE TABLE audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  ts              TEXT NOT NULL,
  actor           TEXT NOT NULL,            -- publisher_id or 'admin'
  action          TEXT NOT NULL,            -- 'publish'|'approve'|'reject'|'yank'|'onboard'
  target          TEXT NOT NULL,            -- 'slug@version' or 'publisher:id'
  detail_json     TEXT
);

-- 语义检索：sqlite-vec 扩展
CREATE VIRTUAL TABLE pkg_embed USING vec0(
  rowid INTEGER PRIMARY KEY,
  embedding FLOAT[1024]
);
CREATE TABLE pkg_embed_ref(
  rowid INTEGER PRIMARY KEY,
  slug TEXT NOT NULL, version TEXT NOT NULL
);

-- 退路：FTS5
CREATE VIRTUAL TABLE pkg_fts USING fts5(
  slug, description, card_md, content='package_versions'
);
```

Embedding 输入 = `spec.description + "\n\n" + tools[*].description + "\n\n" + card_md`，approve 时算一次写入。

### 4.3 HTTP API 表

| Method | Path | 鉴权 | 行为 |
|---|---|---|---|
| `GET` | `/v1/search?q=&limit=10` | 无 | NL → embedding → vec0 KNN top-N → 回填元数据；embedding 不可用退到 FTS5。**只返回 status=approved**。响应含 `search_mode: "vec"\|"fts5_fallback"` |
| `GET` | `/v1/packages/{slug}` | 无 | 列出此 slug 所有 approved 版本（不含 tarball） |
| `GET` | `/v1/packages/{slug}/versions/{ver}` | 无 | 返回 manifest_json + capability_card.md 全文 + pubkey 引用 |
| `GET` | `/v1/packages/{slug}/versions/{ver}/source.tar.gz` | 无 | 流式吐 blob；`ETag = tarball_sha256` |
| `GET` | `/v1/publishers/{id}/pubkey` | 无 | 返回 ed25519 公钥（PEM） |
| `POST` | `/v1/packages` | Publisher Bearer | multipart：tarball + manifest；解包、验签、校 schema、入 `status=pending`、发审核事件 |
| `POST` | `/v1/admin/publishers` | Admin Bearer | body=`{publisher_id, pubkey_ed25519}`；写表 + 返回该 publisher 的 token |
| `POST` | `/v1/admin/review/{slug}/{ver}` | Admin Bearer | body=`{decision, reason}`；通过时算 embedding 入 vec0 + 更 packages.current_version |
| `POST` | `/v1/admin/yank/{slug}/{ver}` | Admin Bearer | 软撤回；已 fork 的副本不动 |

**Fetch 链路免鉴权**——市场公开；driver 离线情况下用本地缓存也不被 token 过期连带挂掉。

---

## 5. Publisher 验证与 onboarding（MVP 手工 + 扩展点）

### 5.1 两层防护
| 层 | 防什么 | 检查方 |
|---|---|---|
| Bearer token | 防止陌生人冒用某 publisher_id 调 publish API | Registry |
| ed25519 签名 | 即使 registry 被攻陷，driver 仍能自验"是这个 publisher 发的" | Driver |

两者不冗余：token 保护上传通路，签名保护下游使用通路。

### 5.2 MVP onboarding（手工带外）
1. Bootstrap admin：registry 启动从 `MCP_REGISTRY_ADMIN_TOKEN` env 读取，**无其他口子**
2. Publisher 离线 `ssh-keygen -t ed25519 -f mcp-publisher.key`（或专用 `mcp-keygen` 子命令）
3. Publisher 带外把 `{publisher_id, pubkey_pem}` 提交给 admin
4. Admin 调 `POST /v1/admin/publishers` 录入，服务端写 `publishers` 表 + 返回 publisher token（随机 32 字节 base64）
5. Admin 带外把 token 发给 publisher，后续用 `Authorization: Bearer <token>` publish

### 5.3 扩展点（未来必做：自助注册）
- `publishers` 表已含 `trust_level` + `auth_method` 字段
- 代码组织：抽象 `PublisherOnboarder` interface，MVP 提供 `ManualOnboarder` 实现，未来加 `GithubOAuthOnboarder` 等只需新实现 + 多一条路由
- 自助注册版 publisher_id 形如 `github:<login>`，trust_level 默认 'community'，仍走每版本审核；admin 可手动提升到 'verified' 跳过审核

### 5.4 Publish 时的双重校验
```
1. 解 Bearer token → publisher_id_from_token
2. 解 manifest    → publisher_id_from_manifest
3. publisher_id_from_token == publisher_id_from_manifest ?       否则 403
4. publishers.approved=1 ? 取 pubkey                              否则 403
5. ed25519_verify(pubkey, tarball_sha256||canonical(manifest_no_sig), sig) ? 否则 400
6. 重算 tarball_sha256 == manifest.tarball_sha256 ?               否则 400
7. schema 校验 + buildspec.Validate(spec.json)                    否则 400
8. 静态预扫，写 review_notes（不阻塞）
9. 落 blob + INSERT package_versions(status='pending') + 入 review_queue
```

### 5.5 Driver 验签时（fetch 后）
```
1. tarball 落到本地，重算 sha256 对比 manifest.tarball_sha256
2. GET /v1/publishers/{publisher_id}/pubkey
3. TOFU：如本地 .driver/known_publishers.json 有该 publisher，pubkey 必须一致；
   不一致弹"pubkey changed"警告，不自动接受
4. ed25519_verify 通过
任一不过 → 拒绝进 fork
```

---

## 6. Driver 端用户旅程

### 6.1 全链路
```
用户: "找一个能根据出生年月推荐结婚日的 MCP"
   ▼
[driver] search_mcp_market(q="...") → Registry /v1/search → top-K cards
   ▼
[Claude] 评估 top-K, 报告给用户
   ▼
用户: "用 wedding_almanac@1.0.0, 推荐数从 10 改成 5, 去掉黄道吉日字段"
   ▼
[driver] pull_mcp_package(slug, version)
         → /v1/packages/.../source.tar.gz → 验签 + 哈希 + 静态扫描 → staging
         → 生成 diff 报告 (imports / network / file IO / subprocess 高亮)
   ▼
[Claude] 展示 diff 给用户, 等待 approve
   ▼
用户: y
   ▼
[driver] approve_mcp_package(slug, version, source_hash)
         → 写 .driver/approved_packages.json
         → 把 staging 内容移到 generated_mcp/<slug>/
   ▼
[Claude] 按用户重构意图改 spec.json + handler (保留 scaffold marker)
   ▼
[Claude] scaffold-mcp-server --spec spec.json --out src/server.py  (重跑)
   ▼
[Claude] mcp-acceptance (现有 skill, 必过)
   ▼
[Claude] register_slave_mcp(target, spec, source_path)  (现有 MCP tool)
   ▼
✅ slave 已装好, dynamic_mcp.yaml 已更新
```

### 6.2 三个新 driver MCP tool（签名）

```go
// internal/driver/search_mcp_market_tool.go
Input:  {"q": string, "limit": int (default 10)}
Output: {"results": [{"slug","version","description","score","tags","hw_summary"}],
         "search_mode": "vec"|"fts5_fallback"}

// internal/driver/pull_mcp_package_tool.go
Input:  {"slug": string, "version": string}
Output: {
  "manifest": <manifest.json>,
  "card": <capability_card.md>,
  "spec": <spec.json>,
  "source_summary": {
    "files": [{"path","sha256","lines","imports":[...]}],
    "risk_flags": ["network_egress_undeclared", "subprocess_call", ...]
  },
  "source_hash": "<sha256 of canonical tarball>",
  "fork_staging_path": "<driver tmp dir, NOT yet generated_mcp/>",
  "signature_ok": true,
  "publisher": {"id":"alice@labs", "first_seen": "..."},
  "needs_approval": true  // false if (slug,version,source_hash) in approved_packages.json
}

// internal/driver/approve_mcp_package_tool.go
Input:  {"slug": string, "version": string, "source_hash": string,
         "fork_to": string (default "generated_mcp/<slug>/")}
Output: {"fork_path": "...", "approved_at": "..."}
// 校验 source_hash 与 staging 一致 → 移到 fork_to → 写 approved_packages.json
```

### 6.3 失败模式与降级

| 失败 | 在哪 | 处理 |
|---|---|---|
| Registry 不可达 | `search_mcp_market` | 错误返回；fallback：让 Claude 走 scaffold 从 0 写（现有路径） |
| Embedding provider 挂 | Registry 内部 | 自动退 FTS5；响应 `search_mode: "fts5_fallback"` |
| 验签失败 | `pull_mcp_package` | 立即拒绝、不落 staging；报 publisher_id + pubkey_fingerprint |
| tarball_sha256 不匹配 | `pull_mcp_package` | 同上 |
| 声明 `network_egress=[]` 但源码 import urllib | source scanner | 写 `risk_flags`，diff 强标红；不阻塞，由用户决定 |
| 用户拒绝 approve | `approve_mcp_package` 未调 | staging 24h GC，不污染 generated_mcp |
| 已 approve 的版本被 yank | 下次同版本 pull | 返回 `yanked: true, reason`；不强清本地副本 |
| 重构后 spec 与 manifest 漂移 | scaffold/acceptance | acceptance 失败即阻塞 register（与现有一致） |

### 6.4 与现有路径的关系（不破坏点）
- 从 0 写 MCP 的链路一行不动；市场只是**多一个起点**
- `mcp-acceptance` 仍是 register 前的硬闸——fork 后改动可能让上游测例失效，必须重跑
- `approved_packages.json` 只是 UX 缓存，删了顶多多走一次 diff，不是安全边界

---

## 7. Publish 侧

### 7.1 Authoring 流程
```
[Publisher 在 driver 里写完 MCP]
  generated_mcp/wedding_almanac/
    spec.json / src/server.py / tests/cases.json   ← 已有
                ▼
  补三个文件 (publish-mcp-package skill 引导):
    manifest.json / capability_card.md / README.md
                ▼
[CLI] mcp-publish pack ./generated_mcp/wedding_almanac
   - 校验 manifest schema + 文件齐全
   - 本地强制重跑 mcp-acceptance（不过不让打）
   - 确定性 tar.gz 打包 (固定 mtime/uid/order)
   - 算 tarball_sha256
   - 用私钥签 (tarball_sha256 || canonical(manifest_no_sig))
   - 回写 manifest.tarball_sha256 + manifest.signature
   输出: dist/<slug>-<ver>.tar.gz + manifest.json
                ▼
[CLI] mcp-publish push dist/<slug>-<ver>.tar.gz
   - 读 ~/.mcp-publisher/token
   - multipart POST /v1/packages
   返回 {review_id, status:"pending"}
```

为什么 publish 走 CLI 而不是 driver MCP tool：publish 与"消费市场"是两个角色，不要塞同一会话。CLI = `cmd/mcp-publish/`，独立二进制。

### 7.2 manifest 区域划分
- **作者手写区**：`slug, version, publisher_id, software, hardware, sla_hint, tags, license, spec_ref, entrypoint, card_ref, cases_ref`
- **CLI 自动填区（pack 时擦掉重算）**：`schema_version, tarball_sha256, signature, created_at`

`mcp-publish pack` 主动擦自动填区，避免改源码忘重签。

### 7.3 审核（admin CLI）
```
mcp-admin queue list                # 列 pending
mcp-admin queue show <slug> <ver>   # 展开 manifest + diff vs 上一版 + review_notes
mcp-admin approve <slug> <ver>
mcp-admin reject  <slug> <ver> --reason "..."
mcp-admin yank    <slug> <ver> --reason "..."
mcp-admin publisher add --id alice@labs --pubkey ./alice.pub
```

`approve` 时服务端：
1. UPDATE package_versions SET status='approved'
2. 算 embedding，写 pkg_embed + pkg_embed_ref
3. 按 semver 更新 packages.current_version
4. 写 audit_log

### 7.4 版本与覆盖语义
- 同 `(slug, version)` **一次写定**，永不覆写。要修就发新 semver
- `yank` 软撤回：search 不再返回；已 fork 的不动
- `rejected` 不删 blob/行，留底；publisher 修包发新版本号即可

---

## 8. 安全

### 8.1 信任模型
> **Driver 不信 registry。** Registry 是个方便的索引 + blob 存储，所有"代码能不能跑到我的 slave"决策权在 driver。

延伸：
- 验签必须在 driver 端发生
- pubkey TOFU：第一次见 publisher 记下到 `.driver/known_publishers.json`；下次 pubkey 不一致弹警告，不自动接受
- 即使 publisher "verified"，driver 仍走 diff/approve（签名 ≠ 内容安全）

### 8.2 静态扫描清单（pull 时跑，写入 risk_flags）
| 扫描 | 实现 | 触发条件 |
|---|---|---|
| import vs `software.packages + stdlib whitelist` | 复用 `executor.ValidateImports` | 多余 import → flag |
| subprocess/os.system/eval/exec | AST walk | 出现 → flag |
| 非 stdin/stdout/stderr `open()` | AST walk | 出现 → flag |
| import urllib/socket/http.client/requests 但 `network_egress=[]` | AST + manifest | 矛盾 → flag（强标红） |
| 源码 > 50 KB 或 > 2000 行 | 文件大小 | flag（异常包） |

driver 与 registry 共享同一份扫描代码（go module 同一包），口径一致。

### 8.3 兜底兼容性
- Registry 离线：driver 用 `~/.cache/mcp-market/` 已 pull 包（sha256 寻址）仍可 fork
- Embedding provider 离线：FTS5 兜底
- Publisher key 丢失：新建 keypair → 找 admin 重新 onboarding；旧版本不受影响（旧 pubkey 在 publishers 表里仍可验旧签名）

### 8.4 速率与配额（MVP 简版）
- Publish：单 publisher 24h ≤ 50 次
- Search/Fetch：默认无限；IP 维度 "60s 内 ≥ 300 次" 软封禁
- 硬编码，不上配额表

---

## 9. 测试策略

### 9.1 三个组件三套测
| 组件 | 测试形态 |
|---|---|
| `cmd/mcp-registry` | Go 单元 + httptest e2e：临时 SQLite + 临时 blob dir，跑 publish→review→search→fetch；embedding provider 用确定性哈希向量 mock |
| `cmd/mcp-publish` / `cmd/mcp-admin` | 表驱动 + e2e：内嵌 registry，断言 tarball 字节级 sha256 稳定 |
| `internal/driver/{search,pull,approve}_mcp_*_tool.go` | mock SDK；新增 e2e：内嵌 registry + 内嵌 slave，从 search 跑到 register_slave_mcp 成功，断言 slave 的 dynamic_mcp.yaml 出现 entry |

### 9.2 金标测试
1. **打包确定性**：同目录连续 `pack` 10 次，字节相同
2. **签名往返**：sign(私钥, X) → verify(公钥, X) 通过；改 X 一字节验签必败
3. **TOFU pubkey 漂移**：第一次记下 pubkey；第二次不同 → 报错且不进 fork
4. **risk_flags 真触发**：fixture `import urllib` + `network_egress=[]` → flag 含 `network_egress_undeclared`
5. **yank 半软撤回**：yank 后 search 看不到，已 approved 本地缓存仍可 register
6. **embedding fallback**：关 embedding provider，search 仍可，`search_mode="fts5_fallback"`
7. **审核状态隔离**：pending 不出现在匿名 search/list；只有 admin token 能列

### 9.3 不必测
- Registry 自身 LLM 推理质量（本期 registry 内部无 LLM）
- Driver 重构后产物（acceptance 已覆盖）
- Slave 端 register 持久化（现有覆盖率已够）

---

## 10. 代码组织（新增）

```
multi-agent/
├── cmd/
│   ├── mcp-registry/        # 新；HTTP server 二进制
│   │   └── main.go
│   ├── mcp-publish/         # 新；publisher CLI
│   └── mcp-admin/           # 新；admin CLI
├── internal/
│   ├── registry/            # 新；server 内部实现
│   │   ├── api/             # chi handlers
│   │   ├── store/           # MetaStore (SQLite + vec + FTS5)
│   │   ├── blob/            # BlobStore (sha256 寻址 FS)
│   │   ├── embedding/       # EmbeddingProvider iface + http impl + nil impl
│   │   ├── onboard/         # PublisherOnboarder iface + ManualOnboarder
│   │   └── review/          # ReviewQueue
│   ├── mcpmarket/           # 新；driver 与 registry 共用
│   │   ├── manifest/        # manifest.json schema + 校验
│   │   ├── pack/            # 确定性 tar.gz 打包/解包
│   │   ├── scanner/         # 静态扫描 (driver/registry 共用)
│   │   └── sig/             # ed25519 sign/verify
│   └── driver/
│       ├── search_mcp_market_tool.go    # 新
│       ├── pull_mcp_package_tool.go     # 新
│       ├── approve_mcp_package_tool.go  # 新
│       └── marketclient/                # 新；HTTP client + 本地缓存 + TOFU
└── skills/
    └── publish-mcp-package/             # 新；引导 publisher 写 manifest + push
        └── SKILL.md
```

**与现有代码的接口**
- `internal/buildspec` 沿用，不改
- `internal/executor/registermcp.go`、`unregistermcp.go` 沿用，不改
- `internal/executor/dynamicmcp.go` 沿用，不改
- `internal/driver/register_mcp_tool.go`、`unregister_mcp_tool.go` 沿用，不改
- 复用 `executor.ValidateImports` 作为 scanner 的底层

---

## 11. 实施顺序建议（给后续 plan 用）

1. `internal/mcpmarket/{manifest,pack,sig,scanner}` —— 共享基础，driver/registry 都依赖
2. `internal/registry/{store,blob,sig,onboard,review,embedding}` —— server 内部
3. `internal/registry/api` + `cmd/mcp-registry/main.go` —— HTTP 接口
4. `cmd/mcp-admin` —— 否则没法 onboard publisher 也没法 approve
5. `cmd/mcp-publish` —— 才能产生测试用包
6. `internal/driver/marketclient` + 三个 MCP tool —— 消费侧
7. `skills/publish-mcp-package/SKILL.md` —— authoring 引导
8. e2e 测：full path search→pull→approve→scaffold→acceptance→register_slave_mcp

每步可独立交付 + 测试通过再下一步。

---

## 12. 与《算力封装的价值》的对应

| 文档原文 | 本设计 |
|---|---|
| 方案 A 能力卡 + 语义匹配器 | `manifest.json` + `capability_card.md` + 语义检索 |
| 方案 C 双向翻译层 | driver 端 fork + LLM 重写 handler（在现有 scaffold 框架内） |
| 方案 F 静态类型 + LLM 精化 | manifest 硬字段做结构过滤；card_md 给 LLM 软排序 |
| 6.6 长尾算力 | 任何 publisher 都能用 ed25519 自签 + 走审核进市场 |
| 9.0b #9 隐私边界 | `hardware.network_egress` + 静态扫描的"声明 vs 实际"对比 |
| §8 分层原则（AI 不上热路径） | registry 与 driver 都把 LLM 限在冷路径（embedding 算一次 / search 重排）；fetch / 验签 / register 全确定性 |
