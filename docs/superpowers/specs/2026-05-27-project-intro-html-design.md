# 项目介绍站（HTML, docs/intro/）— 设计

**日期：** 2026-05-27
**状态：** draft, awaiting user review
**范围：** 在 `docs/intro/` 下产出一组静态 HTML 页面，把 agentserver + loom 两段栈以"系统抽象 + 编程抽象"为主线讲清楚，并通过 ask-search 拿到的对标项目突出本栈的创新性。零运行时依赖（纯 HTML/CSS + 内联 SVG，本地双击 `index.html` 即可看）。

## 目标

把 agentserver + loom 这一对来自同一组织的双层栈讲成一个**有立意的工程系统**，而不是一堆 README 拼起来的 wiki。

需要兑现的三件事：

1. **立意（system abstraction）**——把"算力封装"作为继程序、数据之后的第三种被编码对象的论点完整复述（已有材料：`docs/算力封装的价值.md`），并显式说明 agentserver 给"算力被命名/被寻址/被隔离/被穿透"，loom 给"算力被组合/被动态扩展/被中途介入"。
2. **三种视角（programming abstraction）**——layer（水平分层）、tier（角色分层）、cycle（时间维度的反馈循环），每个视角一页深入。
3. **创新性背书（related work）**——3 组对标 ≈ 12 个项目，统一对比表 9 个维度；标注 agentserver-alone vs. agentserver+loom 的 delta，让 loom 在 agentserver 之上贡献的列一眼可见。

## 非目标

- 不写营销文案、不放团队照片或产品截图引导注册。
- 不实现 JS 框架、单页路由、动态搜索。每页都是独立 HTML，左右键导航靠固定 nav bar。
- **不复制粘贴 spec/plan/skill/README 的原文**（具体见下方"凝练纪律"——这条不是"少写"，恰恰要求**写得更精**）。
- 不为多语言做工程支持（只做中文版；如未来要 i18n 再开第二个 spec）。
- 不做 mermaid 或 D3 的运行时渲染；图全部离线渲好为 SVG 嵌入。

## 凝练纪律（写作哲学）

每个页面都按下面这个标准写——这是这套 intro 的灵魂：

1. **不是导航器、不是 wiki**：读者点进 intro 不是为了拿到一份"项目里所有 spec/plan/skill 的目录"，而是为了在 30 分钟里**理解这个项目在做什么、为什么这么做、它的智识贡献是什么**。
2. **凝练 ≠ 摘要**：每页要提炼出 spec/skill 文档里**没有显式写但散落各处的核心观点**——把工程决策背后的设计模式、把多个 spec 共享的隐含哲学、把别人看不见的工程权衡，提炼成几句话或一张图。
3. **重复就是失败**：如果某段话能直接被 spec 原文替代，就是失败——这段话本应在 intro 里说出比 spec 更"高一层"的东西。
4. **链接的角色**：`[n]` 引到 spec/plan/skill/源码，是给"想验证我们说的对不对、想看实现细节"的读者准备的，**不是给"想要更详细解释"的读者准备的**——后者应该在 intro 自身就被满足。
5. **每页一个"非显然 takeaway"**：写完每页问自己一句话："读者关掉这页之后，能复述出来的、之前没意识到的新东西是什么？"如果说不出来，重写。

各页要提炼的核心观点（写作时这条要兑现）：

| 页面 | 必须凝练出的核心观点 |
|---|---|
| `index.html` | "为什么是两段栈，不是一段"——agentserver 给寻址/隔离/穿透，loom 给组合/演化/人介入；缺一不可。 |
| `system-abstraction.html` | "为什么算力是第三种被编码对象"——从 Liskov 数据抽象的精神类比出，今天调用算力的方式还是 1970 前调用数据的方式；本栈是把"约定"变"接口"的具体实现。 |
| `platform-stack.html` | "agentserver 真正的发明不是 sandboxproxy 或 cc-broker 任何一个组件，而是**让 agent 成为 workspace 的一等公民**——可以被命名、被发现、被反向连进、可以互相 peer-proxy"。子组件只是这个发明的物化。 |
| `layer.html` | "为什么是 7 层而不是 3 层或 10 层"——每一层都对应一个"下一层不够"的问题；每删一层都会失去一个具体能力（举例：删 ② 网络层 → slave 不能反向通；删 ⑥ 应用层 → 没法 humanloop）。 |
| `tier.html` | "tier 不是层，而是同一段代码扮演的角色"——driver 既是 agentserver agent 又是 MCP server；slave 既是被远程调度的执行体又是 chat 模型的运行时；observer 既是遥测又是 capability 公告板。一个进程同时跨多 tier。 |
| `cycle.html` | "四个循环背后的共同模式：每个 cycle 都是'失败 → 自修复 → 学习'"——注册过期就重 OAuth，能力缺失就 scaffold，人介入就 pause-resume。集群的弹性是 cycle 数量乘出来的，不是单点容错给出来的。 |
| `related-work.html` | "为什么没人做出来 agentserver+loom 这个组合"——上层 multi-agent 框架普遍假设单机/同 process；底层 coding-agent 平台普遍假设 1:1 用户↔机器；本栈唯一同时打破了这两个假设。 |
| `references.html` | n/a（参考文献页本身不需要凝练观点） |

写作时，每页**第一段就是这个"核心观点"的一句话表达**，之后所有 sub-section 都是为这一句话做支撑。如果发现一个 sub-section 偏离了核心观点，要么删掉，要么把它的洞见折叠到主线里。

## 设计

### 1. 文件结构

```
docs/intro/
├── index.html                  # 项目定位、TL;DR 表、章节导航
├── system-abstraction.html     # 三次编码 + Liskov 类比 + 栈两段定位
├── platform-stack.html         # agentserver 子组件拆解 + 五大概念 + 接口快照
├── layer.html                  # 水平分层视图（7 层堆叠）
├── tier.html                   # 角色分层视图（driver / slave / observer 三 tier）
├── cycle.html                  # 反馈循环（4 个 cycle：注册续约 / 任务生命周期 / 能力演化 / humanloop）
├── related-work.html           # 12 项目 × 9 维度对比 + agentserver-alone vs +loom delta
├── references.html             # 集中参考文献
└── assets/
    ├── style.css               # 一套简洁学术风样式
    └── diagrams/               # 离线渲好的 SVG（layer 堆叠、tier 三角、各 cycle 环、stack 全景）
```

### 2. 内容弧 — 前三页

#### `index.html`

约 1 屏可读完，定位 + 入口。

- **顶部一句话定位（约 30 字）**：*"agentserver 把算力变成网络上的可寻址单元；loom 在其上织出一张能动态生长的能力网。"*
- **TL;DR 表（3 列 × 3 行）**

  | | agentserver | loom |
  |---|---|---|
  | 比喻 | 算力的"socket + DNS" | 算力的"包管理器 + 编排器" |
  | 核心抽象 | workspace / agent / task / session | capability / contract / dynamic-MCP / humanloop |
  | 类比已有事物 | code-server | 没有完整对标（见 related-work） |

- **章节导览**：6 张卡片（system-abstraction、platform-stack、layer、tier、cycle、related-work），每张 ≈ 50 字摘要 + 一个简笔图标
- **致谢/license/repo** 链接放底部

#### `system-abstraction.html`

约 1500–2500 字。

1. **三次编码的历史类比** —— 复用 `docs/算力封装的价值.md` 的核心表（算法→程序、信息→数据、算力→？），用 HTML 重排
2. **Liskov 数据抽象在算力上的复现** —— 引 *"惯例无法替代强制约束"*（Liskov CLU 论文，`[1]`）
3. **栈的两段定位**：
   - agentserver = "算力**底层网络**"：让分散的算力变成 workspace 内可寻址的 agent；解决"算力的 socket 在哪、谁的、能不能进"
   - loom = "算力**应用层与编排**"：让 agent 的能力可以被命名、组合、动态扩展、可被人类在中途介入
4. **三种视角入口段** —— layer / tier / cycle 各 100 字引出，链到深入页面
5. **结尾大图** —— 堆叠图：agentserver 在底，loom 在上，模型在最顶；左侧标 layer 编号，右侧标 cycle 箭头

#### `platform-stack.html`

约 1200–2000 字。

1. **agentserver 工程拆解**——一段一段讲 agentserver 仓里的子系统（不要堆 Dockerfile 名字，要讲它们解决什么问题）：
   - `sandboxproxy` — 每个 agent 一个隔离沙箱
   - `cc-broker` / `claudecode` 容器 — Claude Code 多实例长驻 + 端口前转
   - `credentialproxy` — API key 不暴露给 agent 本体，由 proxy 注入
   - `llmproxy` — 任何 OpenAI/Anthropic 兼容端点都可代理
   - `executor-registry` — 执行器注册中心
   - `nanoclaw` / `openclaw` / `opencode` — 不同重量级的 coding-agent 容器形态
   - `imbridge` — inbound message bridge
2. **五个核心概念**：workspace（隔离域）、agent（成员）、task（异步 RPC）、session（chat 上下文）、tunnel + peer-proxy（穿 NAT 的反向通道 + 同 workspace 内 agent 直连）
3. **对比已有方案的位置**：
   - 仅本地 IDE → 用不上远程算力
   - 仅 chatbot → 没有真正落地执行的手脚
   - 仅 docker swarm / k8s → 没有"agent"这个被命名的成员、没有跨成员的能力发现
4. **接口快照**：贴 `agentsdk.TaskInfo` / `DelegateTaskRequest` / `AgentCard` 的字段表，证明是真接口

### 3. 内容弧 — 三个分析视角页

#### `layer.html` — 水平分层（7 层堆叠）

| 层 | 名 | 关键模块 | 输入/输出契约 |
|---|---|---|---|
| ⑦ | 模型层 | Claude / Codex CLI（任意 LLM 通过 `llmproxy`） | stream-json |
| ⑥ | 应用层 | task contract envelope、capability journal、humanloop | `TaskContract` JSON / `result.kind` marker |
| ⑤ | 能力层 | chat / chat_resume / bash / file / mcp / register_mcp / unregister_mcp / permissions | `executor.Executor.Run(ctx,t,sink)` |
| ④ | 调度层 | dispatch + driver MCP tools (`submit_task` / `wait_task` / `resume_task` / driver-side fanout) | route by `Task.Skill` |
| ③ | 协议层 | agentserver `workspace / agent / task / session` REST + WS | `agentsdk` Go SDK |
| ② | 网络层 | agentserver `tunnel`（slave 反向 WS）+ `peer-proxy`（agent↔agent HTTP）+ broker | `gw portal` |
| ① | 物理层 | host / docker (`loom-slave-local-prod`) / Jetson Orin (arm64) / Termux (Android) | OS/cgroup |

**写法**：从最底层往上读"为什么要这一层"——每一层因为下一层不够才存在。结尾给一张多平台支持矩阵（amd64 / arm64 / android），证明分层抽象兑现了"算力封装"的承诺。

#### `tier.html` — 角色分层（3 tier）

三 tier：

1. **driver-tier**（用户面）—— 在 agentserver 看是 agent；在 loom 看是 MCP server，被用户的 Claude Code 当作 tool provider。driver 自己负责 fanout 编排，不需要单独的 master agent。
2. **slave-tier**（执行）—— 在 agentserver 看是 agent，advertises 具体技能；在 loom 看是 chat backend / MCP host。可临时长出新能力（动态 MCP）。
3. **observer-tier**（遥测+回放）—— 在 agentserver 看是 events sink + workspace registry；在 loom 看是 lazy-artifact relay + capability publish 接收方。observer 离线不影响业务面。

主图：driver（顶）→ slave（底），observer 在侧。箭头：
- `driver → slave`: `DelegateTask` + TASK_CONTRACT envelope
- `slave → driver`: `peer-proxy /files/blob/<sha>` 取用户文件
- `all → observer`: events + artifacts

Sidebar 卡片三条：
- driver 既是 agentserver agent 又是 MCP server（双重身份）
- slave 能力动态长出来（链回 cycle.html 的 Cycle C）
- observer 与 control-plane 解耦

#### `cycle.html` — 4 个反馈循环

每个 cycle 一张环形 SVG + 200–300 字说明 + 一段真实 log/代码片段。

**Cycle A — agentserver 注册/续约循环**

```
device-code OAuth → tunnel WS → 401 retry → re-OAuth → 重新写 config.yaml
```
落点：用 jetson 重授权 e2e 的脱敏日志。

**Cycle B — 任务生命周期**

```
submit_task → agentserver queue → slave poll → dispatch → executor → sink (chunks)
            → store.Complete → observer event → driver wait_task 拿 final_output
```
落点：case 1 happy chat e2e 日志。

**Cycle C — 能力演化循环**（最有原创性）

```
slave 缺能力 → driver 命令 slave: scaffold-mcp-server → cases.jsonl 验真
            → register_mcp → republish AgentCard → 下次任务可直接调
            → ... 跨 session 累积，集群能力随用户实际需求"长出来"
```
落点：从 `list_agents` 输出抓出真已注册的 MCP（calculator、weather_almanac、weather_forecast）。

**Cycle D — humanloop pause/resume 循环**（最新一环）

```
chat backend → 模型 invoke ask_user / request_permission
             → humanloop MCP forwards IPC → executor closes stdin
             → dispatch wraps result.kind="awaiting_user"
             → driver wait_task returns status:"awaiting_user"
             → 用户答复 → resume_task → claude --resume <S> "User answered: ..."
             → 可能再 awaiting_user，可能 final
```
落点：case 2 e2e 抓真实的 task_id 链 + session_id。

### 4. `related-work.html` 与对比表

#### 分三组对标

1. **底层"远程 coding-agent 平台"**：code-server、GitHub Codespaces、Cursor remote、opencode、**agentserver (alone)**
2. **上层"multi-agent fabric"**：AutoGen、CrewAI、LangGraph、OpenHands、Letta、metaGPT、smol-developer、aider、Devin
3. **本栈两个层级各自的位置**：agentserver = 底层最完整的方案之一；loom = 上层独有的"compute encoding + 动态扩能力 + humanloop"组合

#### 统一对比表（15 项目 × 9 维度）

**图例**：`✅` = first-class 支持；`△` = 有限或间接支持；`×` = 不支持或不在该项目范围内；`n/a` = 该维度对该项目类别不适用；`unknown` = 闭源/无公开材料无法判断。

| 项目 | 多机 | 异构 (arm/Android) | 浏览器内 IDE | Agent 间 RPC | Capability 动态扩 | 自验真 gate | HITL pause/resume | DAG 编排 | Compute encoding 立意 |
|---|---|---|---|---|---|---|---|---|---|
| code-server | × | △ | ✅ | × | × | × | × | × | × |
| GitHub Codespaces | ✅(cloud-only) | × | ✅ | × | × | × | × | × | × |
| Cursor remote | △ (SSH 1:1) | △ | ✅ | × | × | × | × | × | × |
| opencode | △ | △ | △ | × | × | × | × | × | × |
| **agentserver (alone)** | ✅ | ✅ | ✅ | ✅(peer-proxy) | × | × | × | × | △ |
| AutoGen | × | n/a | × | △ (in-proc) | × | × | △ | ✅ | × |
| CrewAI | × | n/a | × | △ | × | × | △ | ✅ | × |
| LangGraph | × | n/a | × | △ | × | × | △ | ✅ | × |
| OpenHands | △ | × | △ | × | × | × | × | △ | × |
| Letta | × | n/a | × | × | △ (memory only) | × | × | × | × |
| metaGPT | × | n/a | × | △ | × | × | × | ✅ | × |
| smol-developer | × | n/a | × | × | × | × | × | × | × |
| aider | × | n/a | × | × | × | × | × | × | × |
| Devin (closed) | ✅(cloud) | × | ✅ | unknown | × | × | × | unknown | × |
| **agentserver + loom (this stack)** | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

**对比表表头第二列**（"agentserver + loom"）以强调色高亮；"agentserver (alone)" 行作为对照，让读者一眼看见 loom 这层贡献的列从 × 变 ✅。

#### 每个对标项目一段（≈ 80–120 字）

分两组分别讲：
- 与 agentserver 对比的（code-server / Codespaces / opencode / Cursor remote）：突出 agentserver 把"一个浏览器 ↔ 一台机器" 推到 "一个 workspace ↔ N 台异构机器 + 跨成员 RPC"
- 与 loom 对比的（AutoGen / CrewAI / LangGraph / OpenHands / Letta / metaGPT / smol-developer / aider / Devin）：突出 loom 把"in-process 多 agent 协调"推到"跨机器真分布式 + capability 动态长出来 + HITL"

#### ask-search 检索表（实施时执行）

| 项目 | query 模板 |
|---|---|
| AutoGen | `AutoGen multi-agent framework Microsoft architecture site:github.com OR site:microsoft.com` |
| CrewAI | `CrewAI multi-agent orchestration architecture site:crewai.com OR site:github.com` |
| LangGraph | `LangGraph multi-agent state machine site:langchain.com` |
| OpenHands | `OpenHands OpenDevin coding agent architecture` |
| Letta (MemGPT) | `Letta MemGPT agent memory architecture paper` |
| metaGPT | `MetaGPT software company multi-agent paper arxiv` |
| Devin | `Cognition Devin AI software engineer architecture` |
| aider | `aider AI pair programming repo map architecture` |
| smol-developer | `smol-developer architecture readme` |
| code-server | `code-server architecture site:coder.com` |
| opencode | `opencode AI coding terminal architecture` |
| GitHub Codespaces | `GitHub Codespaces architecture remote development site:github.com` |
| Cursor remote | `Cursor IDE remote SSH machine architecture` |
| agentserver | `agentserver self-hosted coding agent platform site:github.com` |
| Liskov CLU | `Liskov Zilles CLU abstract data types 1974` |
| MCP spec | `Model Context Protocol specification 2024-11-05 site:modelcontextprotocol.io` |
| OAuth Device Flow | `RFC 8628 OAuth 2.0 device authorization grant` |

每条 query 拿回的最重要 1–2 个 URL 写进 `references.html`，对应位置 `[n]` 反引。

### 5. `references.html`

格式仿 ACM 数字图书馆风格：

```html
<li id="ref-1">
  <span class="ref-num">[1]</span>
  <span class="ref-authors">Barbara Liskov, Stephen Zilles.</span>
  <span class="ref-title">Programming with Abstract Data Types.</span>
  <span class="ref-venue">ACM SIGPLAN Notices, 9(4), 1974.</span>
  <a class="ref-link" href="https://doi.org/...">DOI</a>
</li>
```

分两段：
- **学术参考**（Liskov CLU、MCP 协议规范、OAuth 2.0 Device Authorization Grant RFC 8628、相关 multi-agent 论文）
- **工程参考**（agentserver 仓、本仓、各对标项目的官方 GitHub / blog / 文档站）

其他页用 `<sup><a href="references.html#ref-N">[N]</a></sup>` 反引。

### 6. 视觉风格（`assets/style.css`）

- 字体：正文 `"Source Han Serif SC", "Songti SC", Georgia, serif`；标题 `"Source Han Sans SC", "Helvetica Neue", sans-serif`；代码 `"JetBrains Mono", Menlo, monospace`
- 主题：浅米黄底（`#fbf9f4`）+ 深灰文字（`#222`）；强调色用克制的靛蓝（`#2c4a7c`）
- 排版：最大宽 760px 居中（论文版心），TOC 在右侧 sticky；移动端 TOC 折叠
- 引文：`<sup>[3]</sup>` 蓝色无下划线
- 代码块：行号 + 浅灰背景；纯静态，无 syntax highlighter
- 图：所有 SVG 内联，深色模式自动 `filter: invert()`
- 顶部固定 nav：index / system / platform / layer / tier / cycle / related / references，当前页高亮

## 边界与失态

- **离线可读**：每页双击本地文件就能看；不依赖 CDN（字体 fallback 到系统字体即可）
- **未跑 ask-search 时的 fallback**：对比表用我已知信息填一遍 baseline（如 AutoGen / CrewAI 等大家熟悉的对标），ask-search 拿到最新的官方 URL / 版本号后回填 references 和 platform-stack 的引用号
- **agentserver 的代码我没有完整读完**：只读了 `pkg/agentsdk`、`README.md`、`docs/` 摘要、Dockerfile 名字列表。如果 platform-stack.html 的子系统描述与 agentserver 上游实际实现有偏差，标 `[n]` 引到 agentserver 自己的 README/spec，让读者去看权威源
- **图的渲染**：先用 Mermaid 写源码注释、人工渲成 SVG 嵌入；不依赖运行时 mermaid.js

## E2E（这次不需要传统 e2e）

输出是静态文档，验收靠：

1. **本地浏览器双击 `docs/intro/index.html`，从首页走完所有内部链接不死链**
2. **`references.html` 中每个 `[N]` 在至少一个其他页面被引用**（反向不一定全：references 是字典）
3. **`related-work.html` 对比表的 9 列在 layer/tier/cycle 中都有对应章节背书**（不是"无中生有的差异化"）
4. **打印为 PDF 排版不乱**（A4，纸版交付场景）

## 实施顺序

1. 把 ask-search 列表里的 14 条 query 全跑掉，落到一个 `references-draft.md`（暂存）
2. `assets/style.css` 一次性写好
3. `references.html` 先填学术 + agentserver/loom 的官方仓 URL
4. `index.html` + `system-abstraction.html` + `platform-stack.html`（前三页内容弧）
5. `layer.html` + `tier.html` + `cycle.html`（三个视角页）
6. `related-work.html`（含对比表 + 12 段对标摘要）；ask-search 结果回填 `references.html`
7. SVG 图：先写 4–6 张关键图的 Mermaid 源码（堆叠/三角/4 个 cycle 环），人工渲成 SVG，嵌入对应页
8. 把所有 `[N]` 反引落实，跑一次内部链接 lint（grep `<a href=` 看 404）
9. 浏览器打开 `index.html`，从首页点穿到每一页
10. commit + ROADMAP 加一条 link
