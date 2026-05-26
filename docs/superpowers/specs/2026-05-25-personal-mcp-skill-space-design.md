# 用户私有 MCP & Skill Space — Observer Extension

**日期**: 2026-05-26（v2，supersedes the 2026-05-25 standalone-service draft）
**状态**: 设计草案，待写 plan
**前置已落地**: `2026-05-25-observer-user-workspace-design.md`（observer 单用户 / 多 workspace 重塑，已 merge 至 master）
**关系**:
- 本期把私有"个人作品仓"折进已有的 observer-server 进程，作为 `/api/userspace/*` 路由 + `internal/userspace/*` 子包，不再起独立服务
- 共享 `internal/mcpmarket/*`（manifest schema / 确定性 tar.gz / 静态 scanner）—— 来自 marketplace spec，依赖那边的基础包先落
- 现有 observer 业务表（events / tasks / artifacts / mcp_servers / ...）一行不动；本 feature 全部走新表
- Pip + venv 心智模型：slug 在用户级全局唯一；每个 workspace 装一个版本；任何 workspace 可以基于已有版本做"重构"再 push 一个新版本

---

## 0. 目标与非目标

### 0.1 目标
- **多设备同步**：driver A 上写的 MCP / skill 可以在 driver B、笔记本、远程开发机上 pull 下来直接用
- **多 workspace 独立重构**：同一 slug 在不同 workspace 可以演化出各自的 fork 版本，但共享身份与历史
- **自然语言找回**：embedding 检索"我半年前写的处理 PDF 表格的 skill 叫啥来着"
- **私有默认**：所有读写需要 observer agent token；不向外公开
- **渐进上市**：私有作品成熟后可一键 `promote` 推到 marketplace（翻译 manifest + 用 marketplace publisher 身份重签）

### 0.2 非目标（本期）
- 多用户 / 团队 ACL —— observer 本身就是单用户假设，本 feature 不破例
- 端到端加密 —— 服务端持明文（你信任你自己跑的 observer）；E2EE 留 §10.4 蓝图
- 跨设备 signature / TOFU / diff-approve —— 单用户 + agent token 已是设备等效凭证，多一层签名边际安全有限（详见 §1 决策）
- 完整版本回滚 UI —— 服务端保留所有版本，回滚走 `pull --version X`，CLI-only
- Realtime / push sync —— 拉取模式，不做长连接推送
- 把 marketplace 公开包"装"进 userspace —— marketplace 自有 pull 链路，userspace 只做单向 promote（私有→公开）
- `promote --to-marketplace` —— marketplace 本身延期，本 round 不实施（接口 / 代码位置预留，§7.5 仍保留设计，标 v1.1）
- `risk_flags` 静态扫描器 —— §10.3 标信息性、不阻塞 install；本 round 推迟，CLI 占位"无 scan_report"

---

## 1. 决策摘要

| 维度 | 决策 |
|---|---|
| 部署形态 | **折入** observer-server 进程；路由 `/api/userspace/*`；不起独立 daemon |
| 鉴权 | 复用 observer agent token（observer §4 register 流程已完成）；不引入新 token 层 |
| 设备模型 | 复用 observer 的 `agents` 表；不加 device_pubkey 列；不做 TOFU |
| 包签名 | **不做**（单用户信任根 = 你自己运维的 observer）|
| pull 后 install 流程 | **不弹 diff/approve**；信任自家代码；可重跑 `scaffold-mcp-server` + `mcp-acceptance` 仍是硬闸 |
| slug 唯一性 | **用户全局唯一**（pip 模型）；workspace 维度选 install 的具体版本 |
| 版本来源 | 每个版本 row 带 `created_in_workspace` 字段做 provenance；版本本身全局可见 |
| 主题域 | 同时承载 MCP 包与 skill 包（`manifest.kind: "mcp"\|"skill"`） |
| Blob 存储 | 文件系统 sha256 寻址 + 引用计数；与 observer 现有 `artifacts` blob 路径分开（namespace `userspace/`），不混淆责任 |
| Embedding 检索 | 保留；FTS5 兜底；EmbeddingProvider 同 marketplace iface |
| 加密 | 服务端明文（同 observer 现状） |
| Promote 通路 | 单向 userspace → marketplace；本 spec 仅设计，marketplace 那边需有 publisher 身份 |

---

## 2. 与 observer / marketplace 的关系

### 2.1 复用 observer（已落地）
- HTTP server / chi mux / agent token 中间件
- `*observerstore.Store` 暴露的 `*sql.DB`（同一 SQLite 文件）
- 现有 `agents`、`workspaces`、`api_keys` 表（用作 scope 与权限）
- `observerweb` 把 userspace 路由挂在同一 mux 上（一个端口、一套 token、一套日志）

### 2.2 共享 `internal/mcpmarket/*`（marketplace 延期 → 由本 spec 先落）
原 marketplace spec 计划把 manifest / pack / scanner / sig 四个子包做成 driver 与 registry 共用的基础。因 marketplace 延期，本 spec 直接**落 manifest 与 pack 两个**（personal-space 必需），marketplace 未来作为下游消费者复用同一份代码。
- `internal/mcpmarket/manifest` —— manifest schema 校验 + JCS canonicalize（userspace 用同款 manifest.json，扩展 `kind` 字段）；本 plan 第 1 步建
- `internal/mcpmarket/pack` —— 确定性 tar.gz 打包/解包（USTAR / mtime=0 / 字典序 / mode 标准化）；本 plan 第 2 步建
- `internal/mcpmarket/scanner` —— 静态扫描（risk_flags，信息性，**不作为 install 阻塞闸**）；§10.3 起 install 时只显示不阻塞，**本 plan 推迟到 v1.1 再加**
- `internal/mcpmarket/sig` —— 签名相关；本期 userspace 不签名（§1 决策），完全不实现

### 2.3 不复用 / 独有
- 审核队列（无，自审自信）
- Slug 跨用户冲突逻辑（用户级唯一，不需要）
- Publisher onboarding / token rotation（agent token 已经够用）
- diff/approve 流程（信任自家代码）

---

## 3. 数据模型（新增；全部 `WHERE workspace_id` 由 observer 现有中间件保证）

### 3.1 三张核心表（pip + venv 心智）

```sql
-- slug 是用户全局身份（一个 observer 实例 = 一个用户）；description/tags 反映最新版本
CREATE TABLE userspace_packages (
  slug         TEXT PRIMARY KEY,           -- 'wedding_almanac'
  kind         TEXT NOT NULL,              -- 'mcp' | 'skill'
  description  TEXT NOT NULL DEFAULT '',
  tags_json    TEXT NOT NULL DEFAULT '[]', -- JSON array of string
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

-- 版本全局唯一于 slug；任何 workspace push 都加入这张表，标记出处
CREATE TABLE userspace_package_versions (
  slug                      TEXT NOT NULL REFERENCES userspace_packages(slug),
  version                   TEXT NOT NULL,            -- semver
  created_in_workspace      TEXT NOT NULL,            -- provenance: which workspace pushed it
  created_by_agent_id       TEXT NOT NULL,            -- provenance: which agent inside that workspace
  manifest_json             TEXT NOT NULL,            -- the JCS-canonical manifest
  spec_json                 TEXT,                     -- present when kind=mcp
  card_md                   TEXT NOT NULL,            -- capability_card.md
  tarball_sha256            TEXT NOT NULL,
  blob_sha256               TEXT NOT NULL,            -- = tarball_sha256; FK to blob_objects
  status                    TEXT NOT NULL DEFAULT 'ready',  -- 'ready' | 'yanked'
  created_at                TEXT NOT NULL,
  PRIMARY KEY (slug, version),
  FOREIGN KEY (blob_sha256) REFERENCES userspace_blobs(sha256)
);
CREATE INDEX idx_uspv_workspace ON userspace_package_versions(created_in_workspace);

-- 每个 workspace 装了哪个版本 —— "venv 状态"
CREATE TABLE userspace_workspace_installations (
  workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
  slug              TEXT NOT NULL REFERENCES userspace_packages(slug),
  installed_version TEXT NOT NULL,
  installed_at      TEXT NOT NULL,
  installed_by_agent_id TEXT NOT NULL,
  PRIMARY KEY (workspace_id, slug),
  FOREIGN KEY (slug, installed_version) REFERENCES userspace_package_versions(slug, version)
);

-- Blob：fs 寻址 + 引用计数；与 observer artifacts 共用 *sql.DB 但不共表
CREATE TABLE userspace_blobs (
  sha256       TEXT PRIMARY KEY,
  size_bytes   INTEGER NOT NULL,
  blob_path    TEXT NOT NULL,                          -- 相对 blob_root，sha256[:2]/sha256 寻址
  refcount     INTEGER NOT NULL DEFAULT 0,             -- delete version → -1；归零 GC
  created_at   TEXT NOT NULL
);

-- Embedding（同 marketplace 风格，但 scope 收窄为用户级——本 observer 整体就是单用户）
CREATE TABLE userspace_embedding_meta ( ... );         -- 同 marketplace §4.2 的 embedding_meta
CREATE VIRTUAL TABLE userspace_pkg_embed_1024 USING vec0(
  rowid INTEGER PRIMARY KEY,
  embedding FLOAT[1024]
);
CREATE TABLE userspace_pkg_embed_ref(
  rowid INTEGER PRIMARY KEY,
  slug TEXT NOT NULL, version TEXT NOT NULL,
  embedding_meta_id INTEGER NOT NULL
);

-- FTS5 兜底
CREATE VIRTUAL TABLE userspace_pkg_fts USING fts5(
  slug, description, card_md,
  content='userspace_package_versions'
);
```

### 3.2 关键设计点

- **slug 全局唯一**（PK = slug，无 user_id / workspace_id 前缀）。observer 就是一个用户。
- **版本全局唯一于 slug**，`created_in_workspace` 是 provenance 标记，**不**是访问控制：任何 workspace 都能 install 任何版本。这就是 pip：你能装别的 venv 用过的版本。
- **`workspace_installations` 是"装了"而不是"拥有"**：一个 workspace 同时能装的 slug 不限数量，但同一 slug 只能有一个 installed_version。重装等价 update。
- **Blob 全局去重**（PK = sha256）：两个 workspace push 同样字节的包共用一个 blob，refcount=2。
- **Embedding scope 不带 workspace_id**：因为 slug 已经是全局唯一，搜索结果"我以前写的处理 PDF 的"自然跨 workspace 返回；UI 上再标"哪些 workspace 装着哪个版本"。

---

## 4. 包格式

复用 marketplace §3 的确定性 tar.gz 规范（USTAR / mtime=0 / 字典序 / mode 0644|0755 / gzip level 9）。manifest 加 `kind` 字段。

```
mcp-package-<slug>-<version>.tar.gz
└── mcp-package-<slug>-<version>/
    ├── manifest.json
    ├── capability_card.md
    ├── spec.json                       ← 仅 kind=mcp
    ├── src/server.py                   ← 仅 kind=mcp
    ├── skill/SKILL.md                  ← 仅 kind=skill；其他 skill 资产同目录
    ├── tests/cases.json                ← 仅 kind=mcp
    └── README.md
```

### 4.1 `manifest.json` 字段

```json
{
  "schema_version": 1,
  "kind": "mcp",                         // "mcp" | "skill"
  "slug": "wedding_almanac",
  "version": "1.0.0",
  "tarball_sha256": "<填写后自校>",

  "spec_ref": "spec.json",               // kind=mcp 时存在
  "card_ref": "capability_card.md",
  "cases_ref": "tests/cases.json",       // kind=mcp 时存在

  "software": { "python": ">=3.10,<3.13", "packages": [] },
  "hardware": { "min_ram_mb": 128, "gpu_class": null, "network_egress": ["api.example.com"] },
  "sla_hint": { "latency_p99_ms": 800, "warmup_ms": 0 },
  "tags": ["divination", "calendar", "zh-cn"],
  "license": "MIT",
  "created_at": "2026-05-26T...",

  // skill 专属
  "skill_meta": {
    "install_scope_hint": "user|project",
    "depends_on_skills": ["debugging", "..."]
  }
}
```

**没有 signature 字段**（§1 决策；单用户信任根）。
**没有 owner / publisher 字段**（隐式 = 本 observer 用户）。
**没有 published_by_device_id**（observer 已记录 `created_by_agent_id`，足够 provenance）。

字段大小硬上限同 marketplace §4.1（card 16KiB / tags 32 项 / packages 64 项等）。

---

## 5. HTTP API（挂在 observer-server `/api/userspace/*`）

所有路由复用 observer 现有 `validateAgentToken` 中间件：必须带 `Authorization: Bearer <agent-token>`，中间件 → `(workspace_id, agent_id)` → 注入 request context。

| Method | Path | 行为 |
|---|---|---|
| `GET` | `/api/userspace/search?q=&kind=mcp\|skill\|all&limit=` | 自然语言检索；返回 `[{slug, kind, latest_version, description, my_installed_version}]`，`my_installed_version` 是发请求的 workspace 装的版本（无则空）|
| `GET` | `/api/userspace/packages?kind=&workspace=mine\|all` | 列包；`workspace=mine` 仅返回当前 workspace 装着的；`workspace=all` 返回全部 |
| `GET` | `/api/userspace/packages/{slug}` | 包详情：所有版本 + 各 workspace 当前装的版本 |
| `GET` | `/api/userspace/packages/{slug}/versions/{ver}` | 单版本：manifest + card 全文 + provenance |
| `GET` | `/api/userspace/packages/{slug}/versions/{ver}/source.tar.gz` | 流式 tarball；`ETag = tarball_sha256` |
| `POST` | `/api/userspace/packages` | multipart：tarball + manifest；解包、校验、落 blob、写 version 行、更新当前 workspace 的 installation |
| `POST` | `/api/userspace/packages/{slug}/yank/{ver}` | 软撤回；search 不再返回；已装的 workspace 不动 |
| `POST` | `/api/userspace/workspaces/{ws}/installations/{slug}` | body=`{version}`；切换 workspace 装哪个版本（pull + install 等价一次）|
| `DELETE` | `/api/userspace/workspaces/{ws}/installations/{slug}` | 卸载（不删 version 数据）|
| `DELETE` | `/api/userspace/packages/{slug}` | 硬删：所有版本 + blob refcount-- + 各 workspace installations 级联清。需 `?confirm=<slug>` |

**Workspace 隔离**：写路由（POST/DELETE）默认作用于 token 对应的 workspace；URL 里 `{ws}` 只能等于 token 的 workspace_id，否则 403。读路由按需选 `mine` / `all`，没有跨设备泄露问题（同一用户）。

### 5.1 Push 校验流程

```
1. 中间件解 token → (workspace_id, agent_id)
2. 解 multipart：tarball + manifest（JSON）；分别 size cap 10MiB / 64KiB
3. 解 tarball：路径前缀 / size / zip-slip 防护（mcpmarket/pack 提供）
4. 校验 manifest schema + tarball_sha256 自洽
5. kind=mcp：buildspec.Validate(spec.json) + 入口文件存在
   kind=skill：SKILL.md frontmatter 解析通过
6. 解 packages 看 slug：
     IF userspace_packages[slug] 不存在：INSERT（kind 由首次 push 定，后续 push 必须同 kind 否则 400）
     ELSE IF existing.kind != manifest.kind：→ 400 "kind mismatch with existing slug"
7. INSERT userspace_blobs(sha256, ...) OR refcount++
8. INSERT userspace_package_versions(slug, version, created_in_workspace=ws, created_by_agent_id=ag, ...)
   主键冲突（同 slug+version 重复 push）→ 409
9. UPSERT userspace_workspace_installations(workspace_id=ws, slug, installed_version=version)
10. 算 embedding 写 vec 表 + FTS5 触发器自动同步
11. 200 with { slug, version, blob_sha256, dedup: true/false }
```

---

## 6. CLI: `mcp-userspace`

独立二进制 `cmd/mcp-userspace`，纯 HTTP 客户端，配置走 `~/.mcp-userspace/config.yaml`（指 observer URL + agent_token 或读 driver/slave 的 token state 文件）。

```bash
mcp-userspace login                       # 把 observer URL + agent_token 落本地配置
mcp-userspace push ./generated_mcp/foo    # 自动检测 kind；pack；POST /api/userspace/packages
mcp-userspace search "处理 pdf"           # GET /api/userspace/search
mcp-userspace list                        # GET /api/userspace/packages?workspace=mine
mcp-userspace pull <slug>[@<ver>]         # GET source.tar.gz → ~/.cache/mcp-userspace/<sha>/
mcp-userspace install <slug>[@<ver>] [--as mcp|skill] [--scope user|project]
                                          # 走现有 scaffold/acceptance/register_slave_mcp（kind=mcp）
                                          # 或拷到 ~/.claude/skills/<name>/ | .claude/skills/<name>/ （kind=skill）
mcp-userspace sync                        # 对当前 workspace 装的每个 slug 检查 latest；不自动升级，只列
mcp-userspace yank <slug> <ver>           # 软撤回
mcp-userspace promote <slug>@<ver> --to-marketplace --as-publisher <pub>
                                          # 翻译 manifest → marketplace 包 → POST marketplace
```

`login` 不需要独立账号——它读 `~/.loom/<agent>/observer.token` 之类的现有 agent token 状态文件，或显式 `--token <hex>` 输入。

---

## 7. 用户旅程

### 7.1 Push（driver 里把刚写好的 MCP 推到自己 space）
```
generated_mcp/wedding_almanac/         ← 已有 spec.json / src/server.py / tests/
   ▼
$ mcp-userspace push ./generated_mcp/wedding_almanac
   - 检测 spec.json 存在 → kind=mcp
   - 自动生成 / 补全 manifest.json（slug 由目录名推断 / 用户传 --slug 覆盖）
   - 跑 mcp-acceptance host 子集（schema/import/dry-run；不依赖 slave 环境）
   - 按 mcpmarket §3 确定性 pack
   - multipart POST /api/userspace/packages
   - 返回 {slug, version, dedup}
   ▼
本 workspace 自动装上新版本
```

### 7.2 Search + Install on a different workspace
```
[driver, in ws-work]
$ mcp-userspace search "处理 pdf 表格"
1. invoice_extract@1.2.0    (mcp, kind=mcp)  "PDF 发票表格抽取 → JSON"  装在 ws-work=未装
2. pdf_table_skill@0.3.0    (skill)          "教 Claude 把 PDF 表格转 markdown"  装在 ws-work=未装

$ mcp-userspace install invoice_extract@1.2.0 --as mcp --target jetson-1
   - GET tarball → 验 sha256
   - 拷到 generated_mcp/invoice_extract/
   - 调 scaffold-mcp-server --spec spec.json --out src/server.py  (现有 skill)
   - 调 mcp-acceptance（现有 skill；必过）
   - 调 register_slave_mcp(target=jetson-1, ...)
   - POST /api/userspace/workspaces/ws-work/installations/invoice_extract { "version": "1.2.0" }
```

### 7.3 Workspace 内重构（同 slug 新版本）
```
[ws-work 里 user 觉得 invoice_extract 不够好，改 spec + handler]
$ mcp-userspace push ./generated_mcp/invoice_extract --bump-minor
   - 自动 bump 到 1.3.0
   - 走 §5.1 校验
   - 新 version row 写入，created_in_workspace=ws-work
   - ws-work 的 installed_version 自动跳到 1.3.0
   - ws-personal 仍装 1.2.0 不动

[ws-personal 想要这个新版本]
$ mcp-userspace install invoice_extract@1.3.0
   - 同 §7.2 路径
```

### 7.4 Skill install scope
```
$ mcp-userspace install pdf_table_skill --as skill --scope user
   - 拷 skill/ 子树到 ~/.claude/skills/pdf_table_skill/
   - 不调 register_slave_mcp（skill 是 driver 本地资产）
$ mcp-userspace install pdf_table_skill --as skill --scope project
   - 拷到 ./.claude/skills/pdf_table_skill/
```

### 7.5 Promote 到 marketplace
```
$ mcp-userspace promote invoice_extract@1.3.0 --to-marketplace \
                       --as-publisher alice@labs --version 1.0.0
   - 拉 userspace tarball + manifest
   - 翻译：
     · 删 kind 字段（marketplace 本期只接 mcp，skill 直接 reject）
     · 加 publisher_id / signature（用本地存的 marketplace publisher key 重签）
     · 删 owner/provenance 字段
     · 重打 tar / 重算 sha256
   - 调 marketplace 的 `mcp-publish push`
   - userspace 端在该 version 行加 tag 'promoted-to-marketplace:<pub>:<ver>'
```
Promote 单向；userspace 不接收 marketplace 内容（marketplace 公开包按 marketplace `pull_mcp_package` 链路使用，无需绕路）。

### 7.6 与 driver 现有路径的关系
- `scaffold-mcp-server` / `mcp-acceptance` / `register_slave_mcp` / `unregister_slave_mcp` 一行不动
- userspace install 只是"提供素材 + 调度现有 skill"，最终注册仍走现有路径
- Skill 安装是纯文件操作，Claude Code 启动时自动 discovery

---

## 8. 配额 & 限额（继承 observer §8.4 简版）

- 单包 tarball 压缩后 ≤ 10 MiB；解压后 ≤ 50 MiB；单文件 ≤ 5 MiB；解压文件数 ≤ 1024
- `manifest.json` ≤ 64 KiB；`card_md` ≤ 16 KiB；`spec.json` ≤ 32 KiB
- Push 速率：单 agent 24h ≤ 200 次
- Search/Fetch：单 agent 60s ≤ 600 次软封禁
- 用户总配额：默认 1 GiB blob 总和（admin 可改 observer 配置）；blob 超限 → 拒新 push 直到 yank / GC 后腾出

---

## 9. 测试策略

### 9.1 组件测试
| 组件 | 形态 |
|---|---|
| `internal/userspace/store` | unit：所有公开方法走 sqlite-in-memory；覆盖 PK 冲突 / 跨 workspace 装载 / blob 引用计数 |
| `internal/userspace/api` | httptest：覆盖每条路由的 happy + 401 + 403（跨 workspace 写）+ 4xx 各档 |
| `cmd/mcp-userspace` CLI | 表驱动 + 内嵌 observer 进程 e2e |

### 9.2 金标
1. **跨 workspace 同 slug 共享版本**：ws-A push v1 → ws-B 看得见 v1，可 install → ws-B 的 installation 指 v1，与 ws-A 平行
2. **重构产生新版本**：ws-A 装 v1 后 push v2 → version 行 +1，A 的 installation 自动 jump，B 不动
3. **同 slug 不同 kind 拒收**：先 push `foo` kind=mcp，再 push 同 slug kind=skill → 400
4. **kind=skill 与 kind=mcp install 路径完全分离**：skill 不会跑 register_slave_mcp；mcp 不会拷 skill 目录
5. **Blob 跨 workspace 去重**：两个 workspace push 字节相同的 tarball → blobs 行只一个 + refcount=2
6. **Yank 半软撤回**：yank 后 search 不返回，已 install 的 workspace 仍可用（不强清）
7. **Install scope 隔离**：`--scope user` 与 `--scope project` 文件落点不串
8. **Promote 翻译**：userspace manifest → marketplace manifest 字段映射逐一 assert；签名是 marketplace publisher key 不是 device key；skill 的 promote 直接 reject
9. **配额触顶**：push 一个 11 MiB tarball → 413；用户总用量逼近 quota → 拒新 push
10. **跨 workspace 写 403**：用 ws-A token 调 `/api/userspace/workspaces/ws-B/installations/foo` → 403
11. **Embedding fallback**：关掉 EmbeddingProvider → search 仍可，`search_mode="fts5_fallback"`
12. **现有 register_slave_mcp 回归**：install --as mcp 后能走到 slave dynamic_mcp.yaml entry

### 9.3 端到端
- 复用 observer e2e 套路：内嵌 observer-server + 用 observerclient 真客户端 register 多 agent 多 workspace → 跑 push / install / pull / promote 全链路 → 断言 DB 状态与文件落点
- 真 e2e（per memory [[e2e_required_for_features_and_fixes]]）：本机灰度跑 observer-server，CLI push 一个真包，从第二个 workspace pull + install 走通

---

## 10. 安全 & 隐私

### 10.1 信任模型
- **服务端持明文**：你信任你跑的 observer 实例。多人共用 observer 不在本期支持。
- **跨设备没有 device pubkey/TOFU**：agent token 已经是设备等效凭证；多签一层签名提升边际安全有限——攻击者拿到 token 可直接 ingest 假事件，可直接 push 假包，再加一层 device key 也防不住"被偷的设备"
- **未来若要 E2EE**：客户端用 master key 加密 tarball 后再上传；embedding 须客户端算（性能损失明显）；MVP 不做

### 10.2 跨 workspace 写隔离
- 写路由的 `{ws}` 必须等于 token 的 workspace_id；否则 403
- 读路由按需开放（同一用户内）

### 10.3 静态扫描（信息性，不阻塞）
- 复用 `mcpmarket/scanner`：import 未声明 / subprocess / 大文件 / `network_egress` 矛盾等
- 结果写入 manifest 旁边的 `scan_report` 字段，CLI install 时显式列出，**不强制拦截**（信任自家代码）
- 用户若想"强阻塞"可以在 CLI 加 `--strict`，scan 触发 high severity 即拒装

### 10.4 端到端加密（蓝图，不实施）
- 占位字段：`manifest.encryption: { algo, key_fingerprint }`；本期值 = null
- 落地路径：客户端在 push 前 AES-256-GCM 加密 blob，服务端只见密文；embedding 客户端算并以 trapdoor 方式上传；search 客户端先加密 query 再发；性能 / 准确率均显著下降，因此推迟到强需求出现

---

## 11. 代码组织

```
multi-agent/
├── cmd/
│   └── mcp-userspace/                    # 新；用户侧 CLI（HTTP client）
│       └── main.go
├── internal/
│   ├── userspace/                        # 新；observer 内挂的业务子包
│   │   ├── store/                        # SQLite 三张表 + blob ref counting；共用 observer 的 *sql.DB
│   │   ├── api/                          # chi handlers，外部由 observerweb 挂到 /api/userspace/*
│   │   ├── blob/                         # fs 寻址 + GC；与 observer artifacts 完全分目录
│   │   ├── pack/                         # 薄包装 mcpmarket/pack（kind 路径分流）
│   │   ├── skillpack/                    # skill 专属：SKILL.md frontmatter 解析 + install scope 处理
│   │   └── promote/                      # → marketplace 翻译层
│   ├── observerweb/                      # 已存在；本 feature 在 mux.HandleFunc 段挂 /api/userspace/*
│   ├── observerstore/                    # 已存在；不动；userspace/store 通过 `Store.DB()` 借 *sql.DB
│   └── mcpmarket/                        # 新；由本 plan 第 1-2 步建（manifest + pack）；marketplace 未来共享
│       ├── manifest/                     # schema 校验 + JCS canonicalize
│       └── pack/                         # 确定性 tar.gz 打包/解包
└── skills/
    └── userspace-publish/                # 新；driver 内引导用户把 generated_mcp/<x> push 上去
        └── SKILL.md
```

**与现有代码的接口**
- `internal/buildspec` 沿用
- `internal/executor/{registermcp,unregistermcp,dynamicmcp}` 沿用
- `internal/driver/{register,unregister}_mcp_tool` 沿用
- 复用 `executor.ValidateImports`（mcpmarket scanner 内部已经走这条）
- 新增：`observerstore.Store.DB() *sql.DB` 一个方法，让 userspace 子包能跑自己的 schema migration + queries（同 sqlite 文件，不同表 namespace）

---

## 12. 实施顺序（marketplace 延期 → 本 plan 自带共享基础）

1. `internal/mcpmarket/manifest` —— manifest.json schema struct + JCS canonicalize；含单测（JCS 稳定性 / kind 字段校验）
2. `internal/mcpmarket/pack` —— 确定性 tar.gz 打包/解包（USTAR / mtime=0 / 字典序 / mode 标准化 / zip-slip 防护）；含单测（10 次 pack 字节稳定、跨平台稳定）
3. `observerstore.Store.DB()` 暴露 + userspace 表 schema 在 `internal/userspace/store/schema.sql`；observer-server 启动调一次 `userspace.store.Migrate(db)` 建表
4. `internal/userspace/store` —— CRUD（packages / package_versions / workspace_installations）+ blob refcount；单测覆盖 §9.2 #1/#2/#5
5. `internal/userspace/blob` —— fs sha256 寻址 + 引用计数 + GC
6. `internal/userspace/skillpack` —— SKILL.md frontmatter 解析 + install scope 处理（user vs project）
7. `internal/userspace/api` —— chi handlers；`observerweb` 在 mux 上挂 `/api/userspace/*`；用 observer 现有 agent token 中间件
8. `cmd/mcp-userspace` —— CLI（login / push / search / pull / install / list / sync / yank）
9. 端到端：本机灰度 observer + CLI 跑 push → 切到另一 workspace → pull + install 跑通；遵守 [[e2e_required_for_features_and_fixes]]
10. `skills/userspace-publish/SKILL.md` —— driver 内 authoring 引导

每步独立可测，每步一组 commits。

**推迟项（v1.1 / 后续 plan）**：
- `internal/mcpmarket/scanner`（risk_flags 信息性扫描）—— 不阻塞 v1 install
- `internal/userspace/promote` + `mcp-userspace promote` —— 依赖 marketplace 已落 + 用户有 publisher 身份；marketplace 不动它就不动

---

## 13. 与 observer + marketplace 字段映射汇总

| 概念 | observer | marketplace | userspace |
|---|---|---|---|
| 顶层多租户单位 | workspace（observer 单用户内多 ns） | publisher_id（多用户） | （隐式 = observer 实例 = 单用户）|
| 包身份 | — | `(publisher_id, slug)` | `slug` （用户全局唯一）|
| 版本身份 | — | `(slug, version)` | `(slug, version)` |
| 设备身份 | `agents.id`（在某 workspace 内）| — | 复用 `agents` |
| 设备凭证 | `agents.token_hash` | publisher_token + ed25519 sig | 复用 agent token，不签名 |
| 内容寻址 | `artifacts.sha256`（场景：任务产物）| `tarball_sha256` | `userspace_blobs.sha256`（独立 namespace）|
| 信任新内容 | n/a | 双闸：签名 + diff/approve | 信任（同用户）|
| 跨实例同步 | n/a（observer 是个人单实例）| 公开 search + pull | 不跨 observer；如要跨设备的"同步"就是用 CLI pull |
| 提升对外 | n/a | n/a | `promote --to-marketplace`（单向）|

---

## 14. 风险 & Open questions

1. **`Store.DB()` 暴露的边界泄漏**：把 `*sql.DB` 让 userspace 直接拿，意味着将来 userspace 可以 select observer 业务表（events 等）。CI 加 grep 拒 `SELECT ... FROM (events|tasks|subtasks|artifacts|agents|workspaces)` 在 `internal/userspace/store/` 出现，把它真正限制在 userspace 表族内
2. **跨 workspace install 同 slug 的版本不一致**：用户在 ws-A 装 v1，ws-B 装 v2 是合法的（pip+venv）。但显示给用户时需要清楚标"哪个 workspace 装哪个版本"避免误以为是 bug
3. **`promote` 的 publisher key 存放**：当前不设计 key 管理；用户得手动把 marketplace publisher key 放到本地某固定路径（CLI 读它来签）。这是 marketplace spec 的责任
4. **Kind 增减是 break change**：如果未来要加 `kind=prompt` / `kind=workflow`，schema 不动（kind 是 TEXT），但 install 路径要分支扩展。先把 mcp + skill 跑稳
5. **没有"装一个 slug 的多版本"**：`workspace_installations` 是 `(workspace_id, slug)` 主键。如果未来想要"同 workspace 装多个版本并存"（罕见），schema 要加 alias 字段。MVP 不支持
6. **Blob GC 时机**：refcount 归零是否立即删 fs 文件？建议异步 GC（标记 deleted + 后台 sweep），避免 race。本期可同步删，量大了再换

---

## 15. 与已废弃旧版（2026-05-25 standalone-service draft）的差异

| 旧版 (standalone) | 新版 (observer extension) |
|---|---|
| `cmd/mcp-userspace` HTTP server 进程 | 折掉；挂 observer-server |
| `users` 表 + `owner_user_id` | 删（observer 隐式单用户）|
| user_token + device_token 双层 | 单层 = observer agent token |
| device_pubkey + TOFU + ed25519 签名 | 全删（§1 决策）|
| pull 后 diff/approve 闸 | 删（信任自家代码）|
| 包 PK `(user_id, slug)` | `slug`（用户全局唯一，pip 模型）|
| `manifest.published_by_device_id` | 行内 `created_by_agent_id` 列代替 |
| §9.2 多租户 SQL 强 scope grep CI | 替换为 §14.1 跨表族 grep CI |
| Promote skill 暂禁 | 仍禁（marketplace 暂只收 mcp）|
