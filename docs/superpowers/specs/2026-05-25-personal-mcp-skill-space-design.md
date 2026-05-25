# 用户个人 MCP & Skill Space 设计

**日期**: 2026-05-25
**状态**: 设计草案，待评审
**思想根**: `2026-05-25-mcp-marketplace-design.md`（市场版）的"用户私有版"——把同一套打包/签名/检索机制收窄成单一用户的多设备同步面
**关系**:
- 与 marketplace 并行存在，**不复用同一个服务**，但**复用 `internal/mcpmarket/*` 共享代码**（manifest / pack / sig / scanner）
- 与本仓现有 driver/slave/skill 链路正交：只新增"个人 space 存取通道"，不动现有 MCP 注册 / skill 加载机制
- 与 CLAUDE.md auto-memory 是两件事：memory 存对话上下文记忆，space 存**可复用的执行体**（MCP 包、skill 包）

---

## 0. 目标与非目标

**目标**
- 个人作品的多设备同步：driver A 上写的 MCP / skill 可以在 driver B、笔记本、远程开发机上 pull 下来直接用
- 个人搜索：用自然语言找回"我半年前写的那个处理 PDF 表格的 skill 叫啥来着"
- 隐私默认：内容只属于本人，**任何 list/search/fetch 都需要用户 token**；服务端不向外公开
- 渐进上市：私有作品成熟后可以一键 `promote` 推到 marketplace（翻译 manifest + 签名走 marketplace 的 publisher 身份）

**非目标（本期）**
- 多人协作 / 团队共享 / share-link —— 留扩展位（`visibility` 字段 + `acl` 表），本期只 `private`
- 端到端加密（E2EE）—— 服务端持有明文（实现/embed 简单）；E2EE 留 §9.4 蓝图
- 完整版本回滚 UI —— 服务端保留所有版本，回滚走 `pull --version X`，无 UI
- Realtime sync —— 拉取模式，不做 push 推流
- 设备管理 dashboard —— CLI 列设备就够

---

## 1. 决策摘要

| 维度 | 决策 |
|---|---|
| 部署形态 | 独立二进制 `cmd/mcp-userspace`，多租户；用户可自建实例只放自己 |
| 与 marketplace 关系 | **不**共服务、**共代码**（`internal/mcpmarket/*`）；marketplace 公网 + 审核，userspace 私有 + 自审 |
| 主题域 | 同时承载 MCP 包与 skill 包（`manifest.kind: "mcp"\|"skill"`） |
| 鉴权 | User token（同 marketplace publisher_token 结构，但语义=账号 ≠ 身份证）+ 可选 per-device 签名 |
| 信任 | 自己信自己；跨设备 pull 时校验 `published_by_device_id`，新设备弹一次性确认 |
| 审核 | 无审核队列，publish 即 ready；search 默认只看自己 |
| 检索 | 同 marketplace（embedding + FTS5 兜底），但 query 永远 scoped 到 `user_id` |
| 安装目标 | MCP → 走现有 `scaffold/acceptance/register_slave_mcp` 链路；skill → 拷贝到 `~/.claude/skills/<name>/` 或 `<project>/.claude/skills/<name>/` |
| 加密 | MVP 服务端明文（自托管前提）；E2EE 留蓝图 |
| 上市通路 | `mcp-userspace promote <slug> --to-marketplace`，重新签 marketplace 身份 + 走审核 |

**用户可重定向的点**：如果你想"完全自托管 = 每用户一个进程"或"加密优先 / 接受功能阉割"，告诉我，会调整 §3 与 §9。

---

## 2. 系统组件总图

```
┌──────────── Driver (Claude Code)、笔记本、远程机 等多台设备 ────────────┐
│  CLI: mcp-userspace                                                   │
│   userspace login                  ← 一次性贴 user token + 命名设备   │
│   userspace push <generated_mcp/X> ← 把当前 driver 的成果推上去      │
│   userspace push <skill_dir>       ← 把当前 skill 推上去             │
│   userspace search "..."           ← 自然语言找回                    │
│   userspace pull <slug>[@<ver>]    ← 拉到本机 staging                │
│   userspace install <slug> --as mcp|skill                            │
│   userspace promote <slug> --to-marketplace                          │
│                                                                       │
│  Driver MCP tools (可选, 本期只装 search/pull):                       │
│   search_userspace(query) → top-K cards (own only)                   │
│   pull_userspace_package(slug, ver) → 同 marketplace 流程             │
└──────────────────────────────┬────────────────────────────────────────┘
                               │ HTTPS (Bearer user token, 全程必带)
                               ▼
┌──────────── cmd/mcp-userspace (single Go binary, 多租户) ───────────┐
│  HTTP API (chi):                                                    │
│   全部 /v1/* 需要 user token，**没有匿名路由**                       │
│   POST /v1/auth/devices          (注册当前设备 + 拿 device token)   │
│   GET  /v1/me                                                       │
│   GET  /v1/search?q=&limit=...   (scoped 到 user_id)                │
│   GET  /v1/packages              (列自己所有)                       │
│   GET  /v1/packages/{slug}                                          │
│   GET  /v1/packages/{slug}/versions/{ver}                           │
│   GET  /v1/packages/{slug}/versions/{ver}/source.tar.gz             │
│   POST /v1/packages              (publish; 无审核, 直接 ready)      │
│   POST /v1/packages/{slug}/tags  (rename/move/tag)                  │
│   POST /v1/packages/{slug}/yank/{ver}                               │
│   DELETE /v1/packages/{slug}     (硬删，含所有版本和 blob)          │
│   GET  /v1/devices               (本人所有设备)                     │
│   DELETE /v1/devices/{id}        (撤销丢失设备的 token)             │
│                                                                     │
│  Admin (运维, 用 admin token):                                      │
│   POST /v1/admin/users                                              │
│   POST /v1/admin/users/{id}/tokens                                  │
│   GET  /v1/admin/usage                                              │
│                                                                     │
│  Internals (大量复用 mcpmarket):                                    │
│    MetaStore (SQLite + sqlite-vec + FTS5；表内一律带 user_id)       │
│    BlobStore (fs sha256-addressed, 内容寻址跨用户去重)              │
│    EmbeddingProvider (同 marketplace, iface)                        │
│    SigVerifier (复用; userspace 里签名"可选", 见 §7)                │
└─────────────────────────────────────────────────────────────────────┘
```

**关键边界**
- 服务端不主动评估包内容、不跑代码、不做 LLM 推理（同 marketplace §2）
- 多租户隔离靠**所有 query 必带 `WHERE user_id = ?`**，SQL 层强约束（不依赖业务代码）
- Blob 全局内容寻址 + 跨用户去重：两用户传同样的 tarball 共用一个 blob，但 `package_versions` 行各自一份（用户看到的"我有这个版本"独立）。删用户时 GC 走引用计数
- userspace 不暴露任何不带 user token 的路由——离线 fallback 走客户端本地 `~/.cache/mcp-userspace/<user>/`

---

## 3. 与 marketplace 的代码复用

**共用（直接 import `internal/mcpmarket/*`，禁止 fork）**
- `mcpmarket/manifest` —— manifest schema 校验、JCS canonicalize（userspace 加 `kind` 字段）
- `mcpmarket/pack` —— 确定性 tar.gz 打包（§marketplace §3 同款规范）
- `mcpmarket/sig` —— ed25519 sign/verify（userspace 用 device key 签，可选）
- `mcpmarket/scanner` —— 静态扫描（自己写的代码自己也想知道有哪些 risk）

**独有**
- `internal/userspace/api/` —— chi handlers，全部强制 user token
- `internal/userspace/store/` —— SQLite schema（带 user_id 的多租户表）
- `internal/userspace/blob/` —— 复用 marketplace 同款 sha256 寻址，但加跨用户引用计数
- `internal/userspace/auth/` —— user token + device token 双层
- `internal/userspace/promote/` —— 翻译到 marketplace manifest

**不复用**
- 审核队列（无）
- Publisher onboarding（无 publisher 概念）
- Slug 所有权跨用户冲突（每用户自有 namespace，slug 只在用户内唯一）

---

## 4. 包格式

复用 marketplace §3 的确定性 tar.gz 规范，仅 `manifest.json` 加新字段。

```
mcp-package-<slug>-<version>.tar.gz
└── mcp-package-<slug>-<version>/
    ├── manifest.json            ← kind=mcp|skill 由它声明
    ├── capability_card.md       ← 同上
    ├── spec.json                ← 仅 kind=mcp 时存在
    ├── src/server.py            ← 仅 kind=mcp
    ├── skill/SKILL.md           ← 仅 kind=skill；其他 skill 资产同目录
    ├── tests/cases.json         ← 仅 kind=mcp
    └── README.md
```

`mkill=skill` 时去掉 `spec.json` + `src/`，加 `skill/` 子树。所有 skill 文件（SKILL.md / reference scripts / dot graphs / 子模板）打入 `skill/`，install 时整目录拷贝到目标 skill 路径。

### 4.1 `manifest.json` 扩展

相对 marketplace 的字段，userspace 新增 / 改动：

```json
{
  "schema_version": 1,
  "kind": "mcp",                         // 新；"mcp" | "skill"
  "slug": "wedding_almanac",
  "version": "1.0.0",
  "owner_user_id": "u_alice",            // 替代 publisher_id；语义=账号
  "published_by_device_id": "d_laptop_2026",  // 新；写入设备指纹
  "tarball_sha256": "<...>",
  "signature": "<可选；见 §7.3>",         // device 私钥签的；缺省=user token 已是身份
  "visibility": "private",               // 新；MVP 只允许 "private"，预留 "team"|"public_via_marketplace"
  "tags": ["...", "personal"],
  // skill 专属
  "skill_meta": {
    "install_scope_hint": "user|project",   // 期望安装到 ~/.claude/skills 还是 <proj>/.claude/skills
    "depends_on_skills": ["debugging", "..."]  // 软提示，不阻塞 install
  },
  // mcp 字段同 marketplace
  "spec_ref": "spec.json",
  "card_ref": "capability_card.md",
  "cases_ref": "tests/cases.json",
  "software": { ... },
  "hardware": { ... },
  "sla_hint": { ... },
  "license": "MIT",
  "created_at": "2026-05-25T..."
}
```

**字段大小硬上限**：同 marketplace §4.1。

---

## 5. 数据模型（SQLite）

```sql
-- 用户与设备
CREATE TABLE users (
  id              TEXT PRIMARY KEY,            -- 'u_alice'
  display_name    TEXT,
  created_at      TEXT NOT NULL,
  quota_bytes     INTEGER NOT NULL DEFAULT 1073741824  -- 1 GiB; admin 可改
);

CREATE TABLE user_tokens (
  token_hash      BLOB PRIMARY KEY,
  user_id         TEXT NOT NULL REFERENCES users(id),
  created_at      TEXT NOT NULL,
  revoked_at      TEXT,
  note            TEXT
);
CREATE INDEX idx_user_tokens_uid ON user_tokens(user_id);

CREATE TABLE devices (
  id              TEXT PRIMARY KEY,            -- 'd_laptop_2026'
  user_id         TEXT NOT NULL REFERENCES users(id),
  display_name    TEXT NOT NULL,               -- "alice's laptop"
  device_pubkey   BLOB,                        -- ed25519, 可选；缺省=不验设备签名
  first_seen      TEXT NOT NULL,
  last_seen       TEXT,
  revoked_at      TEXT
);

-- 包（多租户：slug 在 user_id 内唯一，不同用户可有同 slug）
CREATE TABLE packages (
  user_id         TEXT NOT NULL REFERENCES users(id),
  slug            TEXT NOT NULL,
  kind            TEXT NOT NULL,               -- 'mcp' | 'skill'
  current_version TEXT,
  description     TEXT,
  tags_json       TEXT NOT NULL DEFAULT '[]',
  visibility      TEXT NOT NULL DEFAULT 'private',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  PRIMARY KEY (user_id, slug)
);

CREATE TABLE package_versions (
  user_id         TEXT NOT NULL,
  slug            TEXT NOT NULL,
  version         TEXT NOT NULL,
  kind            TEXT NOT NULL,
  published_by    TEXT NOT NULL,               -- device_id at publish time
  manifest_json   TEXT NOT NULL,
  spec_json       TEXT,                        -- NULL when kind=skill
  card_md         TEXT NOT NULL,
  tarball_sha256  TEXT NOT NULL,
  signature       BLOB,                        -- nullable; 见 §7.3
  blob_ref        TEXT NOT NULL,               -- 指向 blob_objects.sha256 (跨用户去重)
  status          TEXT NOT NULL DEFAULT 'ready',  -- 'ready' | 'yanked'
  created_at      TEXT NOT NULL,
  PRIMARY KEY (user_id, slug, version),
  FOREIGN KEY (user_id, slug) REFERENCES packages(user_id, slug)
);

-- 全局 blob：跨用户去重
CREATE TABLE blob_objects (
  sha256          TEXT PRIMARY KEY,
  size            INTEGER NOT NULL,
  blob_path       TEXT NOT NULL,
  refcount        INTEGER NOT NULL DEFAULT 0,  -- 删 package_versions 时 -1；归零异步 GC
  created_at      TEXT NOT NULL
);

CREATE TABLE audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  ts              TEXT NOT NULL,
  user_id         TEXT NOT NULL,
  device_id       TEXT,
  action          TEXT NOT NULL,               -- 'push'|'pull'|'yank'|'delete'|'device_add'|'device_revoke'|'promote'
  target          TEXT NOT NULL,
  detail_json     TEXT
);

-- Embedding（同 marketplace 风格；scope 必带 user_id）
CREATE TABLE embedding_meta ( ... );           -- 同 marketplace §4.2
CREATE VIRTUAL TABLE pkg_embed_1024 USING vec0(rowid INTEGER PRIMARY KEY, embedding FLOAT[1024]);
CREATE TABLE pkg_embed_ref(
  rowid INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL,                       -- search 时 WHERE user_id = ? + KNN 后过滤
  slug TEXT NOT NULL, version TEXT NOT NULL,
  embedding_meta_id INTEGER NOT NULL
);
CREATE INDEX idx_pkg_embed_ref_user ON pkg_embed_ref(user_id);

-- FTS5 兜底
CREATE VIRTUAL TABLE pkg_fts USING fts5(
  user_id UNINDEXED, slug, description, card_md,
  content='package_versions'
);
```

**多租户隔离硬规则**：每个含 `user_id` 列的表，所有 query 必须以 `WHERE user_id = ?` 起手。store 层封装一层 `Tenant(user_id).Query(...)`，业务代码不暴露未 scope 的 raw query；CI 加 grep 拒 `SELECT ... FROM (packages|package_versions|devices|pkg_embed_ref)` 不带 `WHERE user_id`。

---

## 6. HTTP API（关键路由）

所有 `/v1/*`（除 `/v1/admin/*`）必带 `Authorization: Bearer <user_token>`，无匿名访问。

| Method | Path | 行为 |
|---|---|---|
| `POST` | `/v1/auth/devices` | body=`{display_name, device_pubkey?}`；创建设备记录 + 签发 device_token（短期 token，按需续）|
| `GET` | `/v1/me` | 返回用户基本信息 + 配额使用情况 |
| `GET` | `/v1/devices` | 列本人设备 |
| `DELETE` | `/v1/devices/{id}` | 撤销该设备的 token；服务端拒绝其后续请求 |
| `GET` | `/v1/search?q=&kind=mcp\|skill\|all&limit=` | 自然语言检索，scope 到 user_id |
| `GET` | `/v1/packages?kind=` | 列自己所有包（不分页 MVP；> 1k 才考虑） |
| `GET` | `/v1/packages/{slug}` | 该包所有版本元数据 |
| `GET` | `/v1/packages/{slug}/versions/{ver}` | manifest + card 全文 |
| `GET` | `/v1/packages/{slug}/versions/{ver}/source.tar.gz` | 流式 tarball |
| `POST` | `/v1/packages` | multipart：tarball + manifest；校验后**直接 ready**（无审核） |
| `POST` | `/v1/packages/{slug}/yank/{ver}` | 软撤回（搜不到，已 pull 的不动） |
| `DELETE` | `/v1/packages/{slug}` | 硬删全部版本 + blob refcount -- ；走二次确认 query param `?confirm=<slug>` |

### Publish 校验流程
```
1. 解 user_token → user_id；token revoked? → 401
2. 解 device_token（如带）→ device_id；revoked? → 401
3. 解 manifest → owner_user_id；必须 == user_id（防误传他人 manifest）→ 否则 403
4. 配额检查：blob size + 用户当前已用 ≤ quota_bytes → 否则 413
5. 解 tarball：路径前缀 / size / zip-slip 防护（同 marketplace §8.4）
6. kind=mcp：buildspec.Validate(spec.json)；kind=skill：SKILL.md 存在 + 头部 frontmatter 解析通过
7. 如 manifest.signature 存在 + device.device_pubkey 已注册：
     ed25519_verify(device_pubkey, SigningInput, sig) → 否则 400
   （未注册 device_pubkey 的设备 = 仅 token 鉴权，跳过签名）
8. 落 blob（按 sha256 去重：已存在则 refcount+1，blob_path 复用）
9. INSERT package_versions(status='ready') + UPSERT packages.current_version
10. 算 embedding 写 vec 表 + FTS5 触发器自动同步
11. 写 audit_log(action='push', device_id=...)
```

publish 没有"pending"阶段，因为自己审自己。

---

## 7. 鉴权 & 设备管理

### 7.1 双层 token

| 层 | 谁颁发 | 作用 |
|---|---|---|
| `user_token` | admin onboarding 时颁发（同 marketplace 风格） | 长期身份，每用户多枚（不同设备各持一枚），可单独 revoke |
| `device_token` | userspace 自身在 `/v1/auth/devices` 颁发 | 短期 token（默认 30 天，续期接 user_token）；丢失/换机时 `DELETE /v1/devices/{id}` 一键失效 |

**为什么不直接拿 user_token 用**：方便丢机器时只撤一台。CLI `mcp-userspace login` 自动跑：贴 user_token → POST /v1/auth/devices → 拿 device_token → 本地落 `~/.mcp-userspace/device.json`，后续命令都用 device_token。

### 7.2 设备指纹（可选）

`POST /v1/auth/devices` 接受可选 `device_pubkey`（ed25519 公钥）。携带 = 后续 publish 时签 tarball，pull 时 driver 端可校验"是这个设备发的"。
- 不携带 = 纯 token 鉴权，所有 publish 不带 `signature` 字段
- 携带 = 额外保护层，即使 user_token 被偷也无法假冒受信设备（攻击者没私钥）

MVP 不强制；CLI 默认生成 device key 但允许 `--no-device-sign` 跳过。

### 7.3 签名（与 marketplace 的差异）

marketplace 用 publisher 的 pubkey；userspace 用 **device 的 pubkey**。SigningInput 完全同 marketplace §5.6：
```
SigningInput = sha256_bytes(tarball) ‖ JCS(manifest_minus_signature)
```
验签方：
- 服务端 publish 时（如带 sig）—— 一道防内部线
- driver 端 pull 时——主要保护：跨设备拉自己的包，校验"published_by_device_id 对应的 pubkey 验得过"
  - 新设备首次见到某 source device → CLI 显式提示 "from device d_laptop_2026 (alice's laptop), trust? y/n"，回答 y 后写入 `~/.mcp-userspace/known_devices.json`
  - 同 marketplace TOFU 但范围 = 自己的设备集合，而非陌生 publisher 集合

---

## 8. 用户旅程

### 8.1 Push（在 driver 里把刚写好的 MCP/skill 推到自己 space）
```
[driver] 写完 generated_mcp/wedding_almanac
   ▼
$ mcp-userspace push ./generated_mcp/wedding_almanac
   - 检测 kind（有 spec.json → mcp；有 skill/SKILL.md → skill；都没有 → 报错）
   - 自动生成 / 校验 manifest.json（kind / owner_user_id / published_by_device_id 由 CLI 填）
   - 跑 acceptance host 子集（kind=mcp）/ frontmatter 校验（kind=skill）
   - 按 §3 确定性 pack
   - 若设备有 device key → 签
   - POST /v1/packages
   - 返回 {slug, version, blob_sha256, dedup: true/false}
```

### 8.2 Search（找自己以前写的）
```
$ mcp-userspace search "处理 pdf 表格"
1. invoice_extract@1.2.0      (mcp)   "PDF 发票表格抽取 → JSON"
2. pdf_table_skill@0.3.0      (skill) "教 Claude 怎么把 PDF 表格转成 markdown"
3. ocr_helper@0.1.0           (mcp)   ...
```

### 8.3 Pull + Install（在另一台机器上用）
```
$ mcp-userspace pull invoice_extract
  - GET tarball, 验 sha256
  - 如包含 sig + 设备 pubkey 已在 known_devices: 验签 OK
  - 如设备首次见: 提示 "from alice's laptop (d_laptop_2026), trust? [y/n]"
  - 解到 ~/.cache/mcp-userspace/<user>/staging/

$ mcp-userspace install invoice_extract --as mcp --target jetson-1
  - 把 staging 内容拷到 generated_mcp/invoice_extract/
  - 调用现有 scaffold-mcp-server --spec spec.json --out src/server.py（保留 marker）
  - 调用现有 mcp-acceptance（必过）
  - 调用现有 register_slave_mcp(target=jetson-1, ...)

$ mcp-userspace install pdf_table_skill --as skill --scope user
  - 拷 skill/ 内容到 ~/.claude/skills/pdf_table_skill/
  - 不调用 register_slave_mcp（skill 是 driver 本地资产）

$ mcp-userspace install pdf_table_skill --as skill --scope project
  - 拷到 ./.claude/skills/pdf_table_skill/
```

### 8.4 跨设备同步策略（MVP = 显式 pull，无推送）
- 不做后台 push / file-watch sync；用户运行 `pull` 才生效
- `mcp-userspace sync` 便捷命令 = `for each slug in (我有的 - 本地已有): pull`；幂等
- 未来扩展位：`mcp-userspace watch` 长连接订阅推送

### 8.5 Promote 到 marketplace
```
$ mcp-userspace promote invoice_extract@1.2.0 --to-marketplace \
                        --as publisher alice@labs --version 1.0.0
   - 拉 userspace 的 tarball + manifest
   - 用 promote/ 翻译：userspace manifest → marketplace manifest
     · owner_user_id → publisher_id
     · 删 published_by_device_id / visibility
     · 重新算 tarball_sha256 + 重新用 marketplace publisher key 签
   - 调 cmd/mcp-publish 走 marketplace 正常 publish 流程（pending → admin approve）
   - 同步在 audit_log 记 promote 事件，userspace 端 packages.tags 加 'promoted-to-marketplace'
```
Promote 是单向的；userspace 不接收 marketplace 内容（marketplace 是公开的，按 marketplace 链路 `pull_mcp_package` 即可，不需要再绕道 userspace）。

### 8.6 与 driver 现有链路的关系
- `register_slave_mcp` / `unregister_slave_mcp` / `scaffold-mcp-server` / `mcp-acceptance` —— 全部不动
- userspace install 只是"提供素材"，最终注册仍走现有路径
- Skill 安装不接 driver 现有链路，本期 = 复制目录；Claude Code 启动时自动发现（已有机制）

---

## 9. 安全 & 隐私

### 9.1 信任模型
> userspace 服务持有用户内容的明文。**自托管或托管在你信任的服务方**是安全前提。

### 9.2 多租户隔离
- SQL 层强 scope（§5 末段）
- Blob 路径不可猜（`<sha256[:2]>/<sha256>`）+ 服务端流式吐之前再校验 `package_versions WHERE user_id=? AND blob_ref=?` 存在
- 跨用户去重不泄露元信息：A 不会因为 blob 命中知道 B 也有同包（API 只返回 `dedup: true`，不告诉是谁的）

### 9.3 配额 & DOS
- 用户配额：默认 1 GiB（quota_bytes），admin 可改
- 单包上限同 marketplace §8.4
- Publish 速率：单 user 24h ≤ 200 次
- Search/Fetch：单 user 60s ≤ 600 次软封禁

### 9.4 端到端加密（未来）
当前服务端持明文，运维可读。E2EE 蓝图：
- 用户在 CLI 生成 master key（mnemonic 助记词），所有设备共享
- 客户端把 tarball 用 AES-256-GCM 加密后再上传；服务端只见密文
- `embedding` 必须客户端算：每次 publish 时 CLI 自跑一遍小模型 embed，把向量加密后上传一份"trapdoor"；search 时把 query 也加密成同分布的密文向量做近似——这条路径性能 / 准确率均显著下降
- 因此 MVP 不做；先把"自托管 + 多租户隔离"做扎实

---

## 10. 测试策略

### 10.1 组件测试
| 组件 | 测试形态 |
|---|---|
| `cmd/mcp-userspace` | httptest e2e：临时 SQLite + tmp blob + mock embedding；跑 push→search→pull→install→yank→delete |
| `cmd/mcp-userspace` CLI | 表驱动 + 内嵌服务端 e2e |
| `internal/userspace/store` | 多租户隔离单测：用户 A 的 query 永远不返回用户 B 的行（fuzz：随机生成两用户数据 + 跨调 query） |

### 10.2 金标
1. **kind 区分**：push mcp 与 push skill 路径互不污染；wrong kind install 直接拒
2. **多租户隔离**：用户 B 不能 GET 用户 A 的 `/v1/packages/{A 的 slug}`（即使 slug 撞名）→ 404，不是 403（避免存在性泄露）
3. **Blob 跨用户去重**：两用户 push 同 tarball → blob_objects 一行 + refcount=2；任一 yank → refcount=1；都删 → 异步 GC 清盘
4. **TOFU 跨设备**：设备 X push, 设备 Y 首 pull 时弹确认；Y 拒绝则不落 staging
5. **Device token revoke**：撤销后该 device_token 全部 4xx；user_token 与其他 device 仍可用
6. **配额触顶**：用户超 quota 时 publish 413，已存在的包不影响 read
7. **Promote 翻译**：userspace manifest → marketplace manifest 字段映射逐一 assert；signature 用 publisher key 而非 device key
8. **Skill install scope**：`--scope user` 写 ~/.claude/skills；`--scope project` 写 ./.claude/skills；不串
9. **隔离 grep**：CI 跑 `grep -RE "FROM (packages|package_versions|devices|pkg_embed_ref)([^_].*)?WHERE " internal/userspace/store/ | grep -v "user_id"` 必须空
10. **MCP install 回归**：装上后能走 register_slave_mcp，dynamic_mcp.yaml 出现 entry（同 marketplace §9.2）

---

## 11. 代码组织

```
multi-agent/
├── cmd/
│   ├── mcp-userspace/          # 新；HTTP server 二进制
│   │   └── main.go
│   └── mcp-userspace-cli/      # 新；统一 CLI（push/pull/search/install/promote/sync/login）
├── internal/
│   ├── userspace/              # 新
│   │   ├── api/                # chi handlers, 全强制 user_token middleware
│   │   ├── store/              # SQLite 多租户 + 强 scope 封装
│   │   ├── blob/               # sha256 寻址 + 引用计数 + GC
│   │   ├── auth/               # user_token + device_token
│   │   ├── embedding/          # 复用 marketplace iface
│   │   ├── promote/            # → marketplace 翻译
│   │   └── skillpack/          # skill 专属：SKILL.md frontmatter 解析 + install scope 处理
│   ├── mcpmarket/              # 已存在(marketplace spec)；直接 import，禁止 fork
│   └── driver/
│       ├── search_userspace_tool.go    # 新；可选 driver MCP tool
│       └── pull_userspace_package_tool.go  # 新；可选
└── skills/
    └── userspace-publish/      # 新；引导用户在 driver 里把成果 push 上去
        └── SKILL.md
```

**与现有代码接口**
- `internal/buildspec` / `internal/executor/*` / `internal/driver/register_mcp_tool.go` 全部沿用
- `internal/mcpmarket/{manifest,pack,sig,scanner}` 共享代码必须先存在（依赖 marketplace spec 实施）

---

## 12. 实施顺序（依赖 marketplace 先落 `internal/mcpmarket/*`）

1. `internal/userspace/{store,blob,auth}` —— 基础
2. `internal/userspace/skillpack` —— skill 包识别 / install scope
3. `internal/userspace/api` + `cmd/mcp-userspace/main.go` —— HTTP
4. `cmd/mcp-userspace-cli`（login / push / pull / search / install / sync / yank） —— 客户端
5. 端到端：driver 写 → push → 换设备 → pull → install → register_slave_mcp 跑通
6. `internal/userspace/promote` + `cmd/mcp-userspace-cli promote` —— marketplace 通路
7. `skills/userspace-publish/SKILL.md` —— authoring 引导
8. 可选：`internal/driver/{search,pull}_userspace_*_tool.go` 让 Claude 在 driver 内直接调

每步独立可测。

---

## 13. 与 marketplace 的字段映射（promote 用）

| userspace `manifest.json` | marketplace `manifest.json` | 翻译规则 |
|---|---|---|
| `owner_user_id` | `publisher_id` | 用户必须先在 marketplace 注册过 publisher，CLI 参数提供 |
| `published_by_device_id` | （丢弃） | 不是 marketplace 概念 |
| `visibility: "private"` | （丢弃） | marketplace 上即视为 approved 候选 |
| `kind: "mcp"` | （隐含；marketplace 本期只接收 mcp） | kind=skill 时拒 promote，给提示"marketplace 暂不收 skill" |
| `version` | `version` | 一致；如 userspace 用 `0.x` 试验版本，CLI 强制要求 promote 时 ≥ 1.0.0 |
| `signature` | `signature` | 重新计算：用 marketplace 的 publisher private key 重签（device key 不能跨域） |
| `tarball_sha256` | `tarball_sha256` | 因 manifest 改了 → 重 pack → 重算 |
| 其他字段 | 同名直接拷 | software / hardware / sla_hint / tags / license / card_md / spec.json / cases.json 全部 1:1 |

Promote 失败的常见原因 + CLI 提示：
- "你没在 marketplace 注册过 publisher" → 引导走 admin onboarding
- "skill 暂不可 promote" → 明确说本期 marketplace 限定
- "marketplace 端已存在同 slug 且 owner ≠ 你" → marketplace §5.6 slug 所有权拒绝；CLI 建议改名 promote

---

## 14. 风险 & Open questions

1. **多租户 vs 完全自托管**：MVP 选多租户，假设运维方可信。若用户要严格自托管（每用户跑一个进程），同样代码 + 配置 `single_user_mode: true`（跳过 user_id WHERE 强 scope，简化 schema）即可，本期可选实现
2. **设备 pubkey 的实际价值**：在自托管/小团队场景，user_token 已足够；device pubkey 主要给"我害怕 token 泄露"的偏执用户。CLI 默认开，可关
3. **Skill 包格式标准化**：本仓现有 skill = `<dir>/SKILL.md + 资产文件`。如果未来 Anthropic 出官方 skill 包格式，userspace 的 `skillpack/` 要做适配层
4. **MCP marketplace 是否也该接收 skill？** 当前 marketplace spec 只设计了 MCP；如果 marketplace 也扩 skill，§13 promote skill 通路才能开。本期 userspace 先承担 skill 主存放点
5. **Quota 默认 1 GiB**：拍脑袋，实测后调

---

## 15. 与 marketplace spec 的对应

| marketplace 设计 | userspace 处理 |
|---|---|
| 中心化 publish + 审核 | 私有 publish + 无审核 |
| publisher 多对一信任 | 单用户多设备信任 |
| Slug 跨 publisher 唯一 | Slug 仅用户内唯一 |
| 验签 = publisher key + TOFU | 验签 = device key + TOFU（自己的设备集） |
| Driver 端 diff/approve 硬闸 | 同设备 publish 的内容默认信；**跨设备 pull 时仍弹 TOFU**（设备可能被偷） |
| `network_egress` 等 risk_flags | 同款 scanner，对自己的代码也跑；high 仍默认拒 |
| 与官方 `server.json` 对齐 | 仅 promote 时翻译，userspace 内部保持自有字段 |
