# 项目介绍 HTML 站（docs/intro/）— Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a self-contained static-HTML intro at `docs/intro/` that explains agentserver + loom as a 2-layer stack through system + programming abstraction (layer / tier / cycle), with a related-work comparison table sourced from ask-search and references kept inline.

**Architecture:** 8 hand-written HTML pages + one CSS file + a directory of pre-rendered SVG diagrams. No JS framework, no build step, no runtime dependencies — opens by double-clicking `index.html` in any browser. Each page distills core insights (per the spec's 凝练纪律 section), uses `<sup>[N]</sup>` to cite into `references.html`. SVGs are written as Mermaid source for record but committed as static SVG for runtime independence.

**Tech Stack:** Plain HTML5, CSS3 (no preprocessor), inline SVG (no JS chart libs), Markdown → HTML by hand (no SSG). ask-search tool for related-work research. Mermaid CLI optional for diagram source if convenient; the committed artifact is always SVG.

**Spec:** [docs/superpowers/specs/2026-05-27-project-intro-html-design.md](../specs/2026-05-27-project-intro-html-design.md)

---

## File Structure

**Created:**

```
docs/intro/
├── index.html
├── system-abstraction.html
├── platform-stack.html
├── layer.html
├── tier.html
├── cycle.html
├── related-work.html
├── references.html
└── assets/
    ├── style.css
    └── diagrams/
        ├── stack-overview.svg       (used by index + system-abstraction)
        ├── layer-stack.svg          (used by layer)
        ├── tier-triangle.svg        (used by tier)
        ├── cycle-a-registration.svg
        ├── cycle-b-task.svg
        ├── cycle-c-capability.svg
        └── cycle-d-humanloop.svg
```

**Not modified:** No code or other docs change. The single exception is `docs/superpowers/ROADMAP.md` if it exists in the current branch — append a link to the new intro under a "Docs" section (Task 12).

---

## Task 0: Run ask-search queries and record raw findings

**Files:**
- Create: `docs/intro/.work/references-draft.md` (working file, **not committed** — added to `.gitignore` for `docs/intro/.work/`)

This task gathers the external sources needed for `related-work.html` and `references.html`. The findings are recorded raw (URLs + 1-line summaries) so later tasks can cite them without re-running searches.

- [ ] **Step 1: Create the working dir + gitignore**

```bash
mkdir -p docs/intro/.work
echo ".work/" >> docs/intro/.gitignore
git add docs/intro/.gitignore
git commit -m "chore(intro): gitignore .work/ scratch dir"
```

- [ ] **Step 2: Run the 17 ask-search queries from the spec § 4 — record results**

Use the `ask-search` skill / tool. For each query, capture: top 1–2 official URLs (project repo, docs site, paper), 1-line summary of what's authoritative, and the relevant fact for our comparison table.

Queries (copy from spec § 4):

```
AutoGen multi-agent framework Microsoft architecture site:github.com OR site:microsoft.com
CrewAI multi-agent orchestration architecture site:crewai.com OR site:github.com
LangGraph multi-agent state machine site:langchain.com
OpenHands OpenDevin coding agent architecture
Letta MemGPT agent memory architecture paper
MetaGPT software company multi-agent paper arxiv
Cognition Devin AI software engineer architecture
aider AI pair programming repo map architecture
smol-developer architecture readme
code-server architecture site:coder.com
opencode AI coding terminal architecture
GitHub Codespaces architecture remote development site:github.com
Cursor IDE remote SSH machine architecture
agentserver self-hosted coding agent platform site:github.com
Liskov Zilles CLU abstract data types 1974
Model Context Protocol specification 2024-11-05 site:modelcontextprotocol.io
RFC 8628 OAuth 2.0 device authorization grant
```

Record one entry per query in `docs/intro/.work/references-draft.md` with this shape:

```markdown
## AutoGen
- official-repo: https://github.com/microsoft/autogen
- docs: https://microsoft.github.io/autogen/
- arch-relevant-doc: <URL to architecture page>
- comparison-fact: "Multi-agent conversation in single Python process; no built-in distribution beyond chat-completion API; supports human-in-the-loop via UserProxyAgent."
```

- [ ] **Step 3: Verify no obvious gaps**

Cross-check the working file against the 15 rows of the comparison table in spec § 4 (`related-work.html` design). Every row must have at least one URL backing it.

- [ ] **Step 4: Do NOT commit the working file**

```bash
git status   # should show no new tracked files; only the gitignored .work/ dir
```

---

## Task 1: Scaffold `docs/intro/` + write `assets/style.css`

**Files:**
- Create: `docs/intro/assets/style.css`
- Create: `docs/intro/assets/diagrams/.gitkeep` (so the empty dir is committed)

- [ ] **Step 1: Write `assets/style.css`**

Per spec § 6 (视觉风格). One file, ≤ 250 lines. Concrete content:

```css
/* docs/intro/assets/style.css
 * Project intro — agentserver + loom
 * Academic style, zero runtime deps.
 */

:root {
  --bg: #fbf9f4;
  --bg-code: #efebe2;
  --fg: #222;
  --fg-mute: #555;
  --accent: #2c4a7c;
  --rule: #d6d0c4;
  --max-content: 760px;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg: #1a1a1a;
    --bg-code: #2a2a2a;
    --fg: #e8e4d8;
    --fg-mute: #b0a89c;
    --accent: #7ea8e0;
    --rule: #3a3a3a;
  }
  /* Invert SVG diagrams in dark mode */
  img[src$=".svg"], svg { filter: invert(0.9) hue-rotate(180deg); }
}

* { box-sizing: border-box; }
html { scroll-behavior: smooth; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font-family: "Source Han Serif SC", "Songti SC", Georgia, "Times New Roman", serif;
  font-size: 17px;
  line-height: 1.7;
}

/* Top nav (fixed) */
nav.intro-nav {
  position: sticky;
  top: 0;
  background: var(--bg);
  border-bottom: 1px solid var(--rule);
  padding: 0.6em 1em;
  font-family: "Source Han Sans SC", "Helvetica Neue", sans-serif;
  font-size: 14px;
  z-index: 10;
}
nav.intro-nav a {
  margin-right: 1em;
  color: var(--fg-mute);
  text-decoration: none;
}
nav.intro-nav a:hover { color: var(--accent); }
nav.intro-nav a.current { color: var(--accent); font-weight: 600; }

main {
  max-width: var(--max-content);
  margin: 2em auto 4em;
  padding: 0 1.2em;
}

h1, h2, h3, h4 {
  font-family: "Source Han Sans SC", "Helvetica Neue", sans-serif;
  font-weight: 600;
  line-height: 1.3;
  margin-top: 1.6em;
}
h1 { font-size: 1.9em; border-bottom: 2px solid var(--rule); padding-bottom: 0.3em; }
h2 { font-size: 1.4em; }
h3 { font-size: 1.15em; }

p { margin: 0.8em 0; }
a { color: var(--accent); }

/* Lead paragraph (the "core takeaway" first paragraph of each page) */
p.lead {
  font-size: 1.1em;
  font-style: italic;
  border-left: 3px solid var(--accent);
  padding: 0.6em 1em;
  background: rgba(44, 74, 124, 0.04);
  margin: 1.5em 0;
}

/* Tables */
table {
  border-collapse: collapse;
  width: 100%;
  margin: 1em 0;
  font-size: 0.92em;
}
th, td {
  border: 1px solid var(--rule);
  padding: 0.4em 0.6em;
  text-align: left;
  vertical-align: top;
}
th {
  background: var(--bg-code);
  font-family: "Source Han Sans SC", "Helvetica Neue", sans-serif;
}
td.highlight, th.highlight {
  background: rgba(44, 74, 124, 0.08);
  font-weight: 600;
}

/* Code */
code {
  font-family: "JetBrains Mono", "SF Mono", Menlo, monospace;
  font-size: 0.88em;
  background: var(--bg-code);
  padding: 0.1em 0.3em;
  border-radius: 3px;
}
pre {
  background: var(--bg-code);
  padding: 0.8em 1em;
  border-radius: 4px;
  overflow-x: auto;
  font-size: 0.86em;
  line-height: 1.5;
}
pre code { background: none; padding: 0; }

/* Sidebar cards */
aside.card {
  border-left: 3px solid var(--accent);
  background: rgba(44, 74, 124, 0.04);
  padding: 0.8em 1em;
  margin: 1.2em 0;
  font-size: 0.95em;
}
aside.card h4 {
  margin: 0 0 0.4em;
  color: var(--accent);
  font-size: 0.95em;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}

/* References */
ol.refs { padding-left: 0; list-style: none; }
ol.refs li {
  margin: 0.6em 0;
  padding-left: 2.5em;
  text-indent: -2.5em;
  font-size: 0.95em;
}
.ref-num { font-weight: 600; color: var(--accent); margin-right: 0.4em; }

/* Citation superscript */
sup a {
  text-decoration: none;
  color: var(--accent);
  font-size: 0.75em;
  padding: 0 0.15em;
}
sup a:hover { background: rgba(44, 74, 124, 0.15); }

/* Page-bottom prev/next */
.pager {
  display: flex;
  justify-content: space-between;
  margin-top: 4em;
  padding-top: 1em;
  border-top: 1px solid var(--rule);
  font-family: "Source Han Sans SC", "Helvetica Neue", sans-serif;
  font-size: 0.9em;
}

/* Index page cards */
.toc-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 1em;
  margin: 1.5em 0;
}
.toc-cards a {
  display: block;
  padding: 1em;
  border: 1px solid var(--rule);
  border-radius: 4px;
  text-decoration: none;
  color: var(--fg);
  transition: border-color 0.15s, transform 0.15s;
}
.toc-cards a:hover { border-color: var(--accent); transform: translateY(-2px); }
.toc-cards a strong { color: var(--accent); display: block; margin-bottom: 0.3em; }
.toc-cards a small { color: var(--fg-mute); font-size: 0.85em; }

@media print {
  nav.intro-nav, .pager { display: none; }
  main { max-width: none; }
}
```

- [ ] **Step 2: Create the diagrams dir placeholder**

```bash
mkdir -p docs/intro/assets/diagrams
touch docs/intro/assets/diagrams/.gitkeep
```

- [ ] **Step 3: Quick visual sanity**

```bash
# Open in browser via file:// to confirm no syntax errors and dark mode works.
echo "Open file://$PWD/docs/intro/assets/style.css in browser; should render as text without errors."
```

(Manual visual check — there's nothing to test until the first HTML page lands.)

- [ ] **Step 4: Commit**

```bash
git add docs/intro/assets/
git commit -m "feat(intro): scaffold docs/intro/ + style.css (academic, zero deps)"
```

---

## Task 2: Write `references.html` (skeleton + initial known refs)

**Files:**
- Create: `docs/intro/references.html`

This is written early so that all subsequent page tasks can cite `<sup>[N]</sup>` into it. New refs are appended at the end of references.html as later pages need them (the citation number is permanent once assigned).

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>参考文献 — agentserver + loom intro</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html" class="current">参考文献</a>
</nav>
<main>
<h1>参考文献</h1>

<p>编号即被引位置。学术参考用 <code>[N]</code> 引到下面，工程参考给出官方仓库或文档站。
未来若有新增引用，编号顺延，不要重排已有编号（其他页面已经引到了）。</p>

<h2>学术参考</h2>
<ol class="refs">
  <li id="ref-1">
    <span class="ref-num">[1]</span>
    Barbara Liskov, Stephen Zilles.
    <em>Programming with Abstract Data Types.</em>
    ACM SIGPLAN Notices, Vol. 9, No. 4, April 1974, pp. 50–59.
    <a href="https://doi.org/10.1145/942572.807045">DOI</a>
  </li>
  <li id="ref-2">
    <span class="ref-num">[2]</span>
    Anthropic.
    <em>Model Context Protocol Specification (protocolVersion 2024-11-05).</em>
    <a href="https://modelcontextprotocol.io/specification">modelcontextprotocol.io</a>
  </li>
  <li id="ref-3">
    <span class="ref-num">[3]</span>
    W. Denniss, J. Bradley, M. Jones, H. Tschofenig.
    <em>RFC 8628: OAuth 2.0 Device Authorization Grant.</em>
    IETF, August 2019.
    <a href="https://datatracker.ietf.org/doc/html/rfc8628">RFC 8628</a>
  </li>
</ol>

<h2>工程参考(本组织)</h2>
<ol class="refs" start="4">
  <li id="ref-4">
    <span class="ref-num">[4]</span>
    agentserver organization.
    <em>agentserver — self-hosted coding-agent platform.</em>
    <a href="https://github.com/agentserver/agentserver">github.com/agentserver/agentserver</a>
  </li>
  <li id="ref-5">
    <span class="ref-num">[5]</span>
    agentserver organization.
    <em>loom — multi-agent fabric on top of agentserver.</em>
    <a href="https://github.com/agentserver/loom">github.com/agentserver/loom</a>
  </li>
</ol>

<h2>工程参考(对标项目)</h2>
<ol class="refs" start="6">
  <!-- Task 9 (related-work) will append entries here, starting at id="ref-6". -->
  <!-- DO NOT remove this comment until Task 9 fills the list. -->
</ol>

<div class="pager">
  <a href="related-work.html">← 相关工作</a>
  <a href="index.html">首页 →</a>
</div>
</main>
</body>
</html>
```

- [ ] **Step 2: Visual check in browser**

```bash
# file://$PWD/docs/intro/references.html — should render with nav + 5 numbered refs.
```

- [ ] **Step 3: Commit**

```bash
git add docs/intro/references.html
git commit -m "feat(intro): references.html skeleton (Liskov, MCP, RFC 8628, org repos)"
```

---

## Task 3: `index.html`

**Files:**
- Create: `docs/intro/index.html`
- Modify: `docs/intro/assets/diagrams/stack-overview.svg` (created in Task 10; for now reference the path even though the file doesn't exist — visual placeholder shows broken img only until Task 10)

**Core takeaway this page distills** (per spec § "凝练纪律"): *"为什么是两段栈，不是一段"——agentserver 给寻址/隔离/穿透，loom 给组合/演化/人介入；缺一不可。*

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>agentserver + loom — 项目介绍</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html" class="current">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>agentserver + loom</h1>

<p class="lead">
本项目是同一组织的两段栈:agentserver 把算力变成网络上的可寻址单元;
loom 在其上织出一张能动态生长的能力网。两段都不是新轮子,而是把
"算力调用"这一行为从写死的约定升格为机器强制的接口——缺一不可。
</p>

<h2>TL;DR</h2>
<table>
  <tr>
    <th></th>
    <th>agentserver</th>
    <th class="highlight">loom (本项目)</th>
  </tr>
  <tr>
    <td><strong>比喻</strong></td>
    <td>算力的 socket + DNS</td>
    <td class="highlight">算力的包管理器 + 编排器</td>
  </tr>
  <tr>
    <td><strong>核心抽象</strong></td>
    <td>workspace / agent / task / session</td>
    <td class="highlight">capability / contract / dynamic-MCP / humanloop</td>
  </tr>
  <tr>
    <td><strong>类比已有事物</strong></td>
    <td>code-server <sup><a href="references.html#ref-15">[15]</a></sup></td>
    <td class="highlight">没有完整对标(见<a href="related-work.html">相关工作</a>)</td>
  </tr>
</table>

<h2>章节导览</h2>
<div class="toc-cards">
  <a href="system-abstraction.html">
    <strong>系统抽象</strong>
    <small>把算力当作继程序、数据之后的第三种被编码对象;Liskov 风格的论证。</small>
  </a>
  <a href="platform-stack.html">
    <strong>agentserver 平台</strong>
    <small>agent 成为 workspace 的一等公民——sandboxproxy、cc-broker 等子组件
    都是这个发明的物化。</small>
  </a>
  <a href="layer.html">
    <strong>Layer (水平 7 层)</strong>
    <small>从物理算力一路抬到模型层,每一层都对应"下一层不够"的具体问题。</small>
  </a>
  <a href="tier.html">
    <strong>Tier (角色分层)</strong>
    <small>driver / slave / observer 三个角色;tier 不是层,是同一段代码扮演的角色。</small>
  </a>
  <a href="cycle.html">
    <strong>Cycle (反馈循环)</strong>
    <small>4 个时间循环共享同一模式:失败 → 自修复 → 学习。</small>
  </a>
  <a href="related-work.html">
    <strong>相关工作</strong>
    <small>15 项目 × 9 维度对标;为什么没人做出来 agentserver+loom 这个组合。</small>
  </a>
</div>

<h2>仓库与许可</h2>
<ul>
  <li>agentserver: <a href="https://github.com/agentserver/agentserver">github.com/agentserver/agentserver</a> <sup><a href="references.html#ref-4">[4]</a></sup></li>
  <li>loom: <a href="https://github.com/agentserver/loom">github.com/agentserver/loom</a> <sup><a href="references.html#ref-5">[5]</a></sup></li>
</ul>

<div class="pager">
  <span></span>
  <a href="system-abstraction.html">系统抽象 →</a>
</div>
</main>
</body>
</html>
```

**Note about `[15]`**: this references code-server, which will be assigned `id="ref-15"` in Task 9. Until Task 9 lands, the link 404s on click — that's expected; Task 11 lint catches it after Task 9.

- [ ] **Step 2: Visual check**

```bash
# file://$PWD/docs/intro/index.html
# Verify: nav links visible, TL;DR table renders with highlight column, 6 toc cards laid out in grid.
```

- [ ] **Step 3: Commit**

```bash
git add docs/intro/index.html
git commit -m "feat(intro): index.html — landing with TL;DR + 6 toc cards"
```

---

## Task 4: `system-abstraction.html`

**Files:**
- Create: `docs/intro/system-abstraction.html`

**Core takeaway this page distills**: *"为什么算力是第三种被编码对象"——从 Liskov 数据抽象的精神类比出,今天调用算力的方式还是 1970 前调用数据的方式;本栈是把"约定"变"接口"的具体实现。*

This page distills from `docs/算力封装的价值.md` but does NOT copy-paste it. The intro version is tighter (~1500 words), reframed to land the *engineering* implications (what changes when compute becomes encoded), not the *philosophical* implications (which the long-form thesis handles).

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>系统抽象 — 算力作为第三种被编码对象</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html" class="current">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>系统抽象:算力作为第三种被编码对象</h1>

<p class="lead">
今天调用算力的方式(写死 GPU 型号、写死区域、写死并发数、写死框架版本),
正是 1970 年前调用数据的方式:依赖约定、依赖文档、依赖人记得。
本栈做的事很简单:把这一层写死的"约定"变成机器强制的"接口"。
</p>

<h2>三次编码的历史类比</h2>

<table>
  <tr>
    <th>被编码对象</th><th>编码形态</th><th>新能力</th><th>由此带来的新世界</th>
  </tr>
  <tr>
    <td>算法</td><td>程序(源码、字节码)</td><td>调度、迁移、热更新、版本化</td><td>操作系统、多任务、跨平台</td>
  </tr>
  <tr>
    <td>信息</td><td>数据(结构化/非结构化)</td><td>查询、变换、复制、加密、流转</td><td>数据库、互联网、AI</td>
  </tr>
  <tr>
    <td><strong>算力</strong></td><td><strong>?</strong></td><td><strong>?</strong></td><td><strong>?</strong></td>
  </tr>
</table>

<p>规律是清晰的:<strong>一个对象一旦被编码,就从"被使用的现场"变成"可被独立操作的对象",
新的产业和方法论随即出现。</strong></p>

<h2>Liskov 数据抽象在算力上的复现</h2>

<p>1974 年 Liskov 与 Zilles<sup><a href="references.html#ref-1">[1]</a></sup>论证:
<em>"对象的行为完全由一组操作刻画,使用者无需知道表示。"</em>
这一句话改写过数据访问的写法。同样的精神在算力上同样成立:</p>

<aside class="card">
<h4>Liskov 风格的算力定义</h4>
算力的行为应由一组操作刻画,使用者无需知道它是 H100 还是 A100、
是本地还是云端、是单机还是集群、甚至是 LLM 还是人类。
</aside>

<p>Liskov 还说了一句更狠的:<em>"惯例无法替代强制约束。"</em>
这正是本栈出现的理由——把"调用算力时写死的约定"变成"机器强制的接口"。</p>

<h2>栈的两段定位</h2>

<h3>agentserver = 算力底层网络</h3>

<p>解决四件事,缺一不可:</p>
<ul>
  <li><strong>命名</strong>:算力被分配 <code>agent_id</code> 与 <code>display_name</code>;集群中可寻址。</li>
  <li><strong>隔离</strong>:每个 agent 在自己的 sandbox 里跑,文件、进程、网络都受限。</li>
  <li><strong>穿透</strong>:slave 在 NAT 后面也能被 driver 调用——靠反向 WS tunnel + peer-proxy。</li>
  <li><strong>身份</strong>:OAuth 2.0 Device Flow<sup><a href="references.html#ref-3">[3]</a></sup>给每个 agent 颁发 sandbox token + proxy token;调用即鉴权。</li>
</ul>

<p>没有这一层,算力还是"哪台机、IP 多少、能不能 SSH 进去"的运维问题,不是工程问题。</p>

<h3>loom = 算力应用层与编排</h3>

<p>在 agentserver 之上,loom 把"算力的接口"再向上抬了一层:</p>
<ul>
  <li><strong>能力命名</strong>:每个 slave 通过 <code>AgentCard.skills</code> 公告自己有 <code>chat/bash/file/register_mcp</code> 等能力;driver 用 <code>inspect_capabilities</code> 发现。</li>
  <li><strong>能力组合</strong>:driver 用 <code>TaskContract</code> 把多个 slave 的能力组合成一个任务的 DAG。</li>
  <li><strong>能力扩展</strong>:slave 缺能力时,driver 可以让 slave 在线 scaffold 一个新的 MCP server,过 acceptance gate,<code>register_mcp</code> 接入;下次任务可直接调。</li>
  <li><strong>能力中断</strong>:chat 中模型可以调 <code>ask_user</code> 暂停;driver 把问题透给真人,真人回答后用 <code>resume_task</code> 续上 backend session。</li>
</ul>

<p>没有这一层,agentserver 还只是一个"分布式 coding-agent 平台"——能远程跑 Claude,
但任务的形状、能力的命名、跨任务的能力沉淀都不存在。</p>

<h2>合栈的承诺</h2>

<p>两段栈合起来兑现的一句话承诺:<strong>用户可以把一段意图(自然语言)交给驱动方,
后者用集群中现有的、被命名的、随时可被扩展的算力把它执行了,中途可以介入,
执行的痕迹被记录,新增的能力被复用。</strong></p>

<p>本栈不是另一个 multi-agent 框架;multi-agent 框架普遍假设单机 in-process。
本栈也不是另一个 coding-agent 平台;coding-agent 平台普遍假设 1:1 用户↔机器。
本栈把这两层合在一起做(见<a href="related-work.html">相关工作</a>的对比表)。</p>

<h2>三种视角入口</h2>

<p>把"算力被编码"这个抽象命题拆成三个可工程化的视角,后续三页分别展开:</p>

<ul>
  <li><a href="layer.html"><strong>Layer</strong>:水平 7 层堆叠</a> — 物理算力到模型层,每一层都对应"下一层不够"。</li>
  <li><a href="tier.html"><strong>Tier</strong>:角色分层</a> — driver / slave / observer 各扮演什么,如何同一段代码跨多 tier。</li>
  <li><a href="cycle.html"><strong>Cycle</strong>:反馈循环</a> — 4 个时间维度的循环共享同一模式:失败 → 自修复 → 学习。</li>
</ul>

<p><img src="assets/diagrams/stack-overview.svg" alt="agentserver + loom 栈全景图" style="max-width:100%;"></p>

<div class="pager">
  <a href="index.html">← 首页</a>
  <a href="platform-stack.html">agentserver 平台 →</a>
</div>
</main>
</body>
</html>
```

- [ ] **Step 2: Visual check**

```bash
# file://$PWD/docs/intro/system-abstraction.html
# Verify: lead paragraph styled with italic + left border, table renders with bold "?" row,
# aside.card visible with accent left border.
```

- [ ] **Step 3: Commit**

```bash
git add docs/intro/system-abstraction.html
git commit -m "feat(intro): system-abstraction.html — 算力作为第三种被编码对象"
```

---

## Task 5: `platform-stack.html`

**Files:**
- Create: `docs/intro/platform-stack.html`

**Core takeaway**: *"agentserver 真正的发明不是 sandboxproxy 或 cc-broker 任何一个组件,而是让 agent 成为 workspace 的一等公民——可以被命名、被发现、被反向连进、可以互相 peer-proxy"。子组件只是这个发明的物化。*

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>agentserver 平台层</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html" class="current">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>agentserver 平台层</h1>

<p class="lead">
agentserver 真正的发明不是任何一个子组件——sandboxproxy、cc-broker、credentialproxy
都只是物化。它真正做对的事是:<strong>让 agent 成为 workspace 的一等公民</strong>。
可以被命名、被发现、被反向连进、可以互相 peer-proxy。这件事一旦成立,
"分布式 coding-agent"才有可能。
</p>

<h2>五个核心概念</h2>

<table>
  <tr><th>概念</th><th>是什么</th><th>解决的问题</th></tr>
  <tr>
    <td><code>workspace</code></td>
    <td>多 agent 共享的隔离域,有一个 UUID + 一个人类可读的别名</td>
    <td>多团队/多项目并存的边界</td>
  </tr>
  <tr>
    <td><code>agent</code></td>
    <td>workspace 中的命名成员,有 <code>agent_id</code> + <code>display_name</code> + <code>card</code>(能力公告)</td>
    <td>算力的可寻址性 + 可发现性</td>
  </tr>
  <tr>
    <td><code>task</code></td>
    <td>跨 agent 的异步 RPC;有 <code>task_id</code>、<code>skill</code>、<code>prompt</code>、生命周期状态</td>
    <td>算力的可调用性 + 可追踪性</td>
  </tr>
  <tr>
    <td><code>session</code></td>
    <td>chat 类 task 的连续上下文;由 backend(Claude/Codex)持有</td>
    <td>跨 task 续接 LLM 状态(humanloop 的基础)</td>
  </tr>
  <tr>
    <td><code>tunnel + peer-proxy</code></td>
    <td>反向 WebSocket(slave→server)+ agent↔agent HTTP 直连(<code>/api/agent/peer/&lt;short_id&gt;/proxy</code>)</td>
    <td>NAT 穿透 + workspace 内成员互通</td>
  </tr>
</table>

<aside class="card">
<h4>为什么 peer-proxy 是关键</h4>
没有 peer-proxy,driver 给 slave 派任务时,如果任务带文件,slave 要"取文件"
就得知道 driver 的公网地址——driver 通常在用户笔记本上,根本没有公网地址。
peer-proxy 让 slave 通过 agentserver 反向回连 driver 自己,文件流借道走过去。
这是"workspace 内一等公民"的具象体现。
</aside>

<h2>子组件:都是上面这个发明的物化</h2>

<p>agentserver 仓<sup><a href="references.html#ref-4">[4]</a></sup>里这些 Dockerfile 不是"功能堆叠",
而是把"agent 是一等公民"这个抽象拆解到工程可实现的边界。每一个解决一个具体的"不是一等公民则会破"的痛点:</p>

<table>
  <tr><th>子组件</th><th>不存在它,什么破?</th></tr>
  <tr>
    <td><code>sandboxproxy</code></td>
    <td>同 workspace 多 agent 共用文件系统/进程空间 → 一个 agent 越权改另一个 agent 的状态</td>
  </tr>
  <tr>
    <td><code>credentialproxy</code></td>
    <td>API key 暴露给 agent 本体 → agent 一旦被 prompt-inject 就能把 key 外发</td>
  </tr>
  <tr>
    <td><code>llmproxy</code></td>
    <td>所有 agent 写死 api.openai.com / api.anthropic.com → 不能换成自建网关/合规端点</td>
  </tr>
  <tr>
    <td><code>cc-broker</code></td>
    <td>Claude Code 多实例没有中转 → 同 host 跑多个 chat 时各自占端口冲突</td>
  </tr>
  <tr>
    <td><code>executor-registry</code></td>
    <td>不同形态 agent(nanoclaw/openclaw/opencode)各自暴露不同 API → 上层无法统一调用</td>
  </tr>
  <tr>
    <td><code>nanoclaw</code> / <code>openclaw</code> / <code>opencode</code></td>
    <td>不同重量级的 coding-agent 形态都要重复实现 sandbox/proxy/registry 的对接</td>
  </tr>
  <tr>
    <td><code>imbridge</code></td>
    <td>外部 IM(聊天)消息没法接入 agent 任务流</td>
  </tr>
</table>

<h2>接口快照</h2>

<p>"agent 是一等公民"这件事在 Go SDK<sup><a href="references.html#ref-4">[4]</a></sup>里长成这样
(<code>github.com/agentserver/agentserver/pkg/agentsdk</code>):</p>

<pre><code>type AgentCard struct {
    AgentID     string          // 跨 workspace 唯一
    DisplayName string          // 人类可读的别名
    Status      string          // available / busy / offline
    Card        json.RawMessage // 自定义公告:skills/tools/resources/...
}

type DelegateTaskRequest struct {
    TargetID       string  // 派给谁
    Skill          string  // 路由用的能力名
    Prompt         string  // 任务体
    TimeoutSeconds int
}

type DelegateTaskResponse struct {
    TaskID    string
    SessionID string  // chat backend 的 session,跨 task 续接的钩子
    Status    string  // 初始状态
}

type TaskInfo struct {
    TaskID, Status, SessionID, TargetID string
    Result    json.RawMessage  // 结构化输出
    Output    string           // 人类可读输出
    // ... 加 timing / cost / failure_reason 等
}
</code></pre>

<p>注意:<code>SessionID</code> 已经是 agentserver 的一等字段——本栈的 humanloop pause/resume<sup><a href="references.html#ref-2">[2]</a></sup>
就是踩在这个字段上立起来的。</p>

<h2>位置对比:agentserver vs 已有方案</h2>

<table>
  <tr><th>方案</th><th>能远程跑代码吗</th><th>同 workspace 多机吗</th><th>agent 互相能调用吗</th></tr>
  <tr><td>仅本地 IDE</td><td>×</td><td>×</td><td>×</td></tr>
  <tr><td>仅 chatbot</td><td>(无落地执行)</td><td>×</td><td>×</td></tr>
  <tr><td>k8s/docker swarm</td><td>✅</td><td>✅(无命名)</td><td>(裸 IP)</td></tr>
  <tr><td>code-server <sup><a href="references.html#ref-15">[15]</a></sup></td><td>✅</td><td>×(1 实例 1 机)</td><td>×</td></tr>
  <tr><td>GitHub Codespaces <sup><a href="references.html#ref-17">[17]</a></sup></td><td>✅(cloud)</td><td>×</td><td>×</td></tr>
  <tr><td><strong>agentserver</strong></td><td>✅</td><td>✅(named, discovered)</td><td>✅(peer-proxy)</td></tr>
</table>

<p>agentserver 占的格子,前 5 行任何一行都不占——它不是"那 5 个里某一个的改进版",
而是占了一个之前空着的位置。</p>

<div class="pager">
  <a href="system-abstraction.html">← 系统抽象</a>
  <a href="layer.html">Layer →</a>
</div>
</main>
</body>
</html>
```

**Note**: this page cites `[15]` (code-server) and `[17]` (GitHub Codespaces). Both will be added in Task 9 from the ask-search results. Lint in Task 11 catches the dangling refs after Task 9 fills them.

- [ ] **Step 2: Visual check + commit**

```bash
# file://$PWD/docs/intro/platform-stack.html — check tables, aside.card, code block.
git add docs/intro/platform-stack.html
git commit -m "feat(intro): platform-stack.html — agent as workspace 一等公民"
```

---

## Task 6: `layer.html`

**Files:**
- Create: `docs/intro/layer.html`

**Core takeaway**: *"为什么是 7 层而不是 3 层或 10 层"——每一层都对应一个"下一层不够"的问题;每删一层都会失去一个具体能力。*

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>Layer — 水平分层视图</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html" class="current">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>Layer — 水平分层视图</h1>

<p class="lead">
本栈 7 层,从物理算力一路抬到模型层。<strong>不是 3 层、不是 10 层——每一层都对应一个
"下一层不够"的具体问题。</strong>下面的删层思想实验:每删一层都会失去一个真实能力。
</p>

<p><img src="assets/diagrams/layer-stack.svg" alt="7 层堆叠图(物理→模型)" style="max-width:100%;"></p>

<h2>7 层一览(自底向上)</h2>

<table>
  <tr><th>层</th><th>名字</th><th>关键模块</th><th>对外契约</th></tr>
  <tr>
    <td>①</td><td>物理层</td>
    <td>host / docker / Jetson Orin (arm64) / Termux (Android)</td>
    <td>OS + cgroup</td>
  </tr>
  <tr>
    <td>②</td><td>网络层</td>
    <td>agentserver tunnel(slave 反向 WS)+ peer-proxy(agent↔agent HTTP)+ broker</td>
    <td>gw portal HTTP/WS</td>
  </tr>
  <tr>
    <td>③</td><td>协议层</td>
    <td>workspace / agent / task / session 的 REST + WS<sup><a href="references.html#ref-4">[4]</a></sup></td>
    <td><code>agentsdk</code> Go SDK</td>
  </tr>
  <tr>
    <td>④</td><td>调度层</td>
    <td>dispatch + driver MCP tools(<code>submit_task</code> / <code>wait_task</code> / <code>resume_task</code> / driver-fanout)</td>
    <td>routing by <code>Task.Skill</code></td>
  </tr>
  <tr>
    <td>⑤</td><td>能力层</td>
    <td><code>chat</code> / <code>chat_resume</code> / <code>bash</code> / <code>file</code> / <code>mcp</code> / <code>register_mcp</code> / <code>unregister_mcp</code> / <code>permissions</code></td>
    <td><code>executor.Executor.Run(ctx,t,sink)</code></td>
  </tr>
  <tr>
    <td>⑥</td><td>应用层</td>
    <td>TaskContract envelope、capability journal、humanloop ask_user/request_permission</td>
    <td><code>TaskContract</code> JSON / <code>result.kind</code> marker</td>
  </tr>
  <tr>
    <td>⑦</td><td>模型层</td>
    <td>Claude / Codex CLI(任意 LLM 通过 <code>llmproxy</code>)</td>
    <td>stream-json</td>
  </tr>
</table>

<h2>删层思想实验</h2>

<p>每删一层,失去什么能力?这是判断一层"必要"的最直接方法。</p>

<aside class="card">
<h4>删 ② 网络层(tunnel + peer-proxy)</h4>
slave 在 NAT 后面则不能被 driver 调用。无法跨家庭/办公网部署 slave。
集群必须全在同一公网 + 同一防火墙策略。
</aside>

<aside class="card">
<h4>删 ③ 协议层(workspace/agent/task)</h4>
没有 workspace 隔离,多团队/多项目共享一台 agentserver 会互相看见任务;
没有 task 抽象,所有调用退化成裸 HTTP RPC,丢失生命周期追踪。
</aside>

<aside class="card">
<h4>删 ④ 调度层(driver MCP tools + dispatch)</h4>
用户的 Claude Code 没有统一的"派任务"接口,要为每个 slave 写一个 MCP;
任务 routing/fanout/wait/resume 全要 caller 自己实现。
</aside>

<aside class="card">
<h4>删 ⑤ 能力层</h4>
slave 的"能做什么"只能靠 chat 模型即兴决定;集群无法对外公告能力清单,
driver 无法做容量规划/能力发现/dry-run。
</aside>

<aside class="card">
<h4>删 ⑥ 应用层(contract + capability journal + humanloop)</h4>
任务边界不清(没有 contract 校验),能力增长不持久(没有 journal),
模型中途要人决策时只能"猜"(没有 humanloop pause/resume)。
本栈相对竞品的所有"上层智识贡献"都集中在这一层。
</aside>

<h2>多平台支持矩阵</h2>

<p>分层抽象的兑现:同一份能力代码在物理层的多种平台上跑。</p>

<table>
  <tr><th>平台</th><th>arch</th><th>chat backend</th><th>本栈支持状态</th></tr>
  <tr><td>Linux host (host-native)</td><td>amd64</td><td>Claude / Codex</td><td>✅</td></tr>
  <tr><td>Linux docker container</td><td>amd64</td><td>Claude / Codex</td><td>✅(loom-slave-local-prod 灰度跑过)</td></tr>
  <tr><td>NVIDIA Jetson Orin (host-native)</td><td>arm64</td><td>Claude</td><td>✅(case 5 e2e 跨节点验过)</td></tr>
  <tr><td>Android (Termux, host-native)</td><td>aarch64</td><td>Claude</td><td>✅(bootstrap-slave.sh 支持)</td></tr>
</table>

<div class="pager">
  <a href="platform-stack.html">← agentserver</a>
  <a href="tier.html">Tier →</a>
</div>
</main>
</body>
</html>
```

- [ ] **Step 2: Visual check + commit**

```bash
# file://$PWD/docs/intro/layer.html
# Note the SVG src is the placeholder path; img will show broken until Task 10.
git add docs/intro/layer.html
git commit -m "feat(intro): layer.html — 7 层 + 删层思想实验"
```

---

## Task 7: `tier.html`

**Files:**
- Create: `docs/intro/tier.html`

**Core takeaway**: *"tier 不是层,而是同一段代码扮演的角色"——driver 既是 agentserver agent 又是 MCP server;slave 既是被远程调度的执行体又是 chat 模型的运行时;observer 既是遥测又是 capability 公告板。一个进程同时跨多 tier。*

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>Tier — 角色分层视图</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html" class="current">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>Tier — 角色分层视图</h1>

<p class="lead">
<strong>Tier 不是层,是角色。</strong>同一个进程可以同时扮演多个 tier;
"driver/slave/observer"不是物理上的三类机器,而是工程上的三种职责。
这是本栈和"3-tier client/server"传统架构最大的区别。
</p>

<p><img src="assets/diagrams/tier-triangle.svg" alt="driver/slave/observer 三角图" style="max-width:100%;"></p>

<h2>三个角色</h2>

<h3>driver-tier — 用户面</h3>

<p>双重身份:</p>
<ul>
  <li>在 <strong>agentserver</strong> 看:它是个 agent,有自己的 <code>sandbox_id</code>、出现在 <code>list_agents</code> 输出里。</li>
  <li>在 <strong>loom</strong> 看:它是个 MCP server,被用户的 Claude Code(或 Codex CLI)当作 tool provider 调用。</li>
</ul>

<p>用户的"我想要 X"自然语言,通过 Claude Code → driver MCP → driver 用 <code>resolveTarget</code>
挑 slave → <code>DelegateTask</code> 派出去。driver 自己还负责 fanout 编排——不需要单独的 master agent。</p>

<h3>slave-tier — 执行</h3>

<p>双重身份:</p>
<ul>
  <li>在 <strong>agentserver</strong> 看:它是 agent,公告 <code>skills: [chat, bash, file, ...]</code>;poll task queue。</li>
  <li>在 <strong>loom</strong> 看:它是 chat 模型(Claude/Codex)的运行时 + 一堆 MCP server 的 host。</li>
</ul>

<p>关键:<strong>slave 的能力可以临时长出来</strong>。driver 看到 slave 缺某个 MCP 工具,
可以让 slave <code>scaffold-mcp-server</code> 写一个,过 <code>mcp-acceptance</code> 验真,
<code>register_mcp</code> 接入,然后 republish 自己的 AgentCard。下次任务可以直接调
(详见 <a href="cycle.html">Cycle C</a>)。</p>

<h3>observer-tier — 遥测 + 回放</h3>

<p>双重身份:</p>
<ul>
  <li>在 <strong>agentserver</strong> 看:它是 events sink + workspace registry(也兼用户/工作区管理面)。</li>
  <li>在 <strong>loom</strong> 看:它是 lazy-artifact relay(大文件不走 driver↔slave,走 observer 中转)+ capability publish 的接收方(slave 注册了新 MCP 后把规格抄送给 observer)。</li>
</ul>

<p>关键:<strong>observer 与 control-plane 解耦</strong>。observer 短暂离线,driver 和 slave 照样能跑任务;
只是遥测会暂时丢,以及 lazy-artifact 模式下大文件传输会暂停。这个解耦是 lab/产品环境分离的基础。</p>

<h2>tier 与 layer 的关系</h2>

<p>这是一个常见混淆点:</p>

<table>
  <tr><th>Layer</th><th>Tier</th></tr>
  <tr><td>水平,自底向上,每层是一种抽象</td><td>垂直,按职责,每 tier 是一种角色</td></tr>
  <tr><td>每层都被所有 tier 用到</td><td>每个 tier 都跨多层(物理→模型一路用)</td></tr>
  <tr><td>"调度层在 chat 之下"</td><td>"driver 是调度+应用+模型的协奏者"</td></tr>
</table>

<aside class="card">
<h4>同一进程跨多 tier 的具体例子</h4>
driver-fanout 模式下,driver 进程既扮演 <strong>driver-tier</strong>(接受用户意图)
又扮演 <strong>master 编排器的角色</strong>(派子任务到多个 slave)——这两件事在
其他 multi-agent 框架里通常要两个进程,本栈一个进程做完。
</aside>

<aside class="card">
<h4>slave 能力动态长出来</h4>
"slave 是固定能力的执行节点"是错的。任务 N 用了 slave A 的 <code>weather_forecast</code>;
任务 N 之前,这个 MCP 不存在——是任务 N-1 临时让 slave A scaffold 出来的。
slave 是<strong>会学习的执行节点</strong>(详见 <a href="cycle.html">Cycle C</a>)。
</aside>

<aside class="card">
<h4>observer 离线不影响业务面</h4>
observer 是异步 sink,所有 driver/slave 的事件都是 fire-and-forget。
observer 重启时 driver 不会卡;事件丢失是可接受的代价。这是把
"control-plane 高可用"和"observability 高可用"故意分开的设计。
</aside>

<h2>消息流</h2>

<table>
  <tr><th>方向</th><th>协议</th><th>用途</th></tr>
  <tr><td>driver → slave</td><td><code>DelegateTask</code> + TASK_CONTRACT envelope</td><td>派任务(带 contract 校验)</td></tr>
  <tr><td>slave → driver</td><td><code>peer-proxy /files/blob/&lt;sha&gt;</code></td><td>取用户在 driver 上的文件</td></tr>
  <tr><td>slave → observer</td><td>events stream</td><td>遥测、capability 公告</td></tr>
  <tr><td>driver → observer</td><td>events stream + artifact create/sync</td><td>遥测、lazy-artifact 注册</td></tr>
  <tr><td>slave ↔ observer</td><td>lazy-artifact PUT/GET</td><td>大文件中转(避免 driver↔slave 直传)</td></tr>
</table>

<div class="pager">
  <a href="layer.html">← Layer</a>
  <a href="cycle.html">Cycle →</a>
</div>
</main>
</body>
</html>
```

- [ ] **Step 2: Visual check + commit**

```bash
git add docs/intro/tier.html
git commit -m "feat(intro): tier.html — driver/slave/observer + 同进程跨多 tier"
```

---

## Task 8: `cycle.html`

**Files:**
- Create: `docs/intro/cycle.html`

**Core takeaway**: *"四个循环背后的共同模式:每个 cycle 都是'失败 → 自修复 → 学习'。集群的弹性是 cycle 数量乘出来的,不是单点容错给出来的。"*

- [ ] **Step 1: Write the page**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>Cycle — 反馈循环视图</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html" class="current">Cycle</a>
  <a href="related-work.html">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>Cycle — 反馈循环视图</h1>

<p class="lead">
四个 cycle 都遵循同一个模式:<strong>失败 → 自修复 → 学习</strong>。
注册过期就重 OAuth,能力缺失就 scaffold,人介入就 pause-resume。
集群的弹性不是某个单点的容错给的,是 cycle 数量乘出来的。
</p>

<h2>Cycle A — agentserver 注册/续约循环</h2>

<p><img src="assets/diagrams/cycle-a-registration.svg" alt="OAuth Device Flow 循环图" style="max-width:100%;"></p>

<p>失败:slave 的 <code>tunnel_token</code> 服务端被轮换 → WS handshake 401。<br>
自修复:slave 检测到 401 → 触发 OAuth 2.0 Device Authorization Grant<sup><a href="references.html#ref-3">[3]</a></sup>
→ 把新的 device-code URL 打到 stderr → 真人浏览器批准 → agentserver 颁新 token
→ slave 把新 token 写回 <code>config.yaml</code> → tunnel reconnect 成功。<br>
学习:新 token 持久化;下次重启不需要再走一遍。</p>

<pre><code>2026/05/26 06:16:34 agentsdk: connecting to https://agent.cs.ac.cn
2026/05/26 06:16:34 agentsdk: tunnel disconnected: 401
2026/05/26 06:16:34 agentsdk: reconnecting in 1s...
...
Open this URL to authenticate:
    https://agent.cs.ac.cn/oauth2/device/verify?user_code=ChuX9Nfn
...
2026/05/26 06:17:13 agentsdk: tunnel connected (sandbox: 47d4d7ba-...)</code></pre>

<h2>Cycle B — 任务生命周期循环</h2>

<p><img src="assets/diagrams/cycle-b-task.svg" alt="task 生命周期循环图" style="max-width:100%;"></p>

<p>失败:driver 给一个任务但 slave 处理时崩了,或超时。<br>
自修复:agentserver 给 task 标 <code>failed</code> 并写 <code>failure_reason</code>;
driver <code>wait_task</code> 返回失败原因;driver 可以重试、降级、或交回用户。<br>
学习:失败原因和耗时通过 observer 沉淀;后续任务可以根据这些数据做能力规划。</p>

<pre><code>submit_task(prompt="Reply HELLO", target="slave-local-prod", skill="chat")
  → agentserver task_id=task_831eafaf-...
  → slave poll → dispatch → backend.Run
  → backend session_id=33ec8be3-1573-47c0-...
  → store.Complete + observer EventSlaveTaskCompleted
  → driver wait_task returns status:"completed", output:"HELLO"</code></pre>

<h2>Cycle C — 能力演化循环(本栈最有原创性的一环)</h2>

<p><img src="assets/diagrams/cycle-c-capability.svg" alt="capability 演化循环图" style="max-width:100%;"></p>

<p>失败:driver 想做 X,但发现没有任何 slave 公告了 X 这个能力。<br>
自修复:driver 命令一个有 <code>register_mcp</code> 能力的 slave 去
<code>scaffold-mcp-server</code> 写一个新 MCP server → 用
<code>mcp-acceptance</code> skill 跑 <code>cases.jsonl</code> 验真 → 通过则
<code>register_mcp</code> 接入 → slave republish AgentCard。<br>
学习:新 MCP 写入 slave 的 <code>dynamic_mcp.yaml</code> 持久化;
下次重启自动加载;后续任意 driver 任意 task 都可以直接调。</p>

<aside class="card">
<h4>真实例子</h4>
线上 <code>slave-jetson-prod</code> 现在公告的 MCP 工具:
<code>add</code>(基础算术,作为 scaffold 模板)、
<code>weather_probability</code>(基于 Open-Meteo 历史档案估天气概率)、
<code>weather_forecast</code>(未来 N 天天气)。这些都不在 slave 部署时存在——
是某次任务 ad-hoc 长出来的,从此被复用。
</aside>

<p>这个 cycle 是本栈对"算力封装"立意的最强落地:<strong>能力本身被编码成一个可被
discovery/添加/版本化/移除的对象</strong>。和其他 multi-agent 框架"tool 是开发期硬编码"
的写法形成本质区别。</p>

<h2>Cycle D — humanloop pause/resume 循环</h2>

<p><img src="assets/diagrams/cycle-d-humanloop.svg" alt="humanloop pause/resume 循环图" style="max-width:100%;"></p>

<p>失败:chat 模型跑到一半,需要用户的判断/授权才能继续(比如不确定该写哪个文件)。<br>
自修复:模型调内置 <code>ask_user</code> / <code>request_permission</code> MCP 工具<sup><a href="references.html#ref-2">[2]</a></sup>
→ humanloop server 拦截 → 通过 unix socket 把问题透给 chat executor
→ executor 关 stdin,backend 优雅退出 → dispatch 把结果包成
<code>result.kind="awaiting_user"</code> → driver <code>wait_task</code> 返回
<code>status:"awaiting_user"</code> + question + session_id → 用户答复
→ <code>resume_task</code> 起 <code>chat_resume</code> 子任务 →
<code>claude --resume &lt;session_id&gt;</code> 把 "User answered: ..." 当下一个 user turn 喂进去
→ 模型可继续,或再 awaiting_user,或 final。<br>
学习:session_id 由 backend 持久,可以无限多轮;agentserver task 状态机不动。</p>

<pre><code>submit_task → wait_task → status:"awaiting_user" (session_id=53a1de6a-...)
                                                  question={kind:"ask_user", "pick a color", ["red","blue"]}
        ↓ 用户答 "blue"
resume_task(last_task_id, "blue") → 新 task task_d94c1765-... → status:"completed" output:"blue"</code></pre>

<h2>四个 cycle 的共同模式</h2>

<table>
  <tr><th>Cycle</th><th>失败</th><th>自修复</th><th>学习</th></tr>
  <tr><td>A 注册</td><td>token 过期 (401)</td><td>device-code OAuth</td><td>新 token 写回 config</td></tr>
  <tr><td>B 任务</td><td>backend 崩/超时</td><td>失败原因回传 driver</td><td>遥测沉淀</td></tr>
  <tr><td>C 能力</td><td>能力缺失</td><td>scaffold + acceptance + register</td><td>MCP 持久化到 dynamic_mcp.yaml</td></tr>
  <tr><td>D humanloop</td><td>模型不确定</td><td>ask_user pause/resume</td><td>session_id 跨任务续接</td></tr>
</table>

<p>这种"每一种失败都对应一种自修复路径"的密度,是本栈在工程上对竞品形成代差的关键。</p>

<div class="pager">
  <a href="tier.html">← Tier</a>
  <a href="related-work.html">相关工作 →</a>
</div>
</main>
</body>
</html>
```

- [ ] **Step 2: Visual check + commit**

```bash
git add docs/intro/cycle.html
git commit -m "feat(intro): cycle.html — 4 cycle 共享 失败→自修复→学习 模式"
```

---

## Task 9: `related-work.html` + append refs to `references.html`

**Files:**
- Create: `docs/intro/related-work.html`
- Modify: `docs/intro/references.html` (append refs 6–22)

**Core takeaway**: *"上层 multi-agent 框架普遍假设单机/同 process;底层 coding-agent 平台普遍假设 1:1 用户↔机器;本栈唯一同时打破了这两个假设。"*

This task uses Task 0's `references-draft.md` to populate both the comparison table and the references appendix.

- [ ] **Step 1: Append refs 6–22 to `references.html`**

Open `docs/intro/references.html`. Find the comment `<!-- Task 9 (related-work) will append entries here ... -->` and replace it with:

```html
  <li id="ref-6">
    <span class="ref-num">[6]</span>
    Microsoft Research. <em>AutoGen — Multi-agent conversation framework.</em>
    <a href="https://github.com/microsoft/autogen">github.com/microsoft/autogen</a>
  </li>
  <li id="ref-7">
    <span class="ref-num">[7]</span>
    crewAIInc. <em>CrewAI — Framework for orchestrating role-playing autonomous AI agents.</em>
    <a href="https://github.com/crewAIInc/crewAI">github.com/crewAIInc/crewAI</a>
  </li>
  <li id="ref-8">
    <span class="ref-num">[8]</span>
    LangChain. <em>LangGraph — Build resilient language agents as graphs.</em>
    <a href="https://github.com/langchain-ai/langgraph">github.com/langchain-ai/langgraph</a>
  </li>
  <li id="ref-9">
    <span class="ref-num">[9]</span>
    All-Hands-AI. <em>OpenHands (formerly OpenDevin) — A platform for software development agents powered by AI.</em>
    <a href="https://github.com/All-Hands-AI/OpenHands">github.com/All-Hands-AI/OpenHands</a>
  </li>
  <li id="ref-10">
    <span class="ref-num">[10]</span>
    Letta (formerly MemGPT). <em>Letta — Agent server with stateful memory.</em>
    <a href="https://github.com/letta-ai/letta">github.com/letta-ai/letta</a>;
    Packer et al., <em>MemGPT: Towards LLMs as Operating Systems</em>, arXiv:2310.08560, 2023.
  </li>
  <li id="ref-11">
    <span class="ref-num">[11]</span>
    Hong et al. <em>MetaGPT: Meta Programming for a Multi-Agent Collaborative Framework.</em>
    arXiv:2308.00352, 2023.
    <a href="https://github.com/geekan/MetaGPT">github.com/geekan/MetaGPT</a>
  </li>
  <li id="ref-12">
    <span class="ref-num">[12]</span>
    smol-ai. <em>developer — the first library to let you embed a developer agent in your own app.</em>
    <a href="https://github.com/smol-ai/developer">github.com/smol-ai/developer</a>
  </li>
  <li id="ref-13">
    <span class="ref-num">[13]</span>
    Paul Gauthier. <em>aider — AI pair programming in your terminal.</em>
    <a href="https://github.com/Aider-AI/aider">github.com/Aider-AI/aider</a>
  </li>
  <li id="ref-14">
    <span class="ref-num">[14]</span>
    Cognition AI. <em>Devin — AI software engineer.</em>
    <a href="https://www.cognition.ai/blog/introducing-devin">cognition.ai/blog/introducing-devin</a>
  </li>
  <li id="ref-15">
    <span class="ref-num">[15]</span>
    Coder. <em>code-server — VS Code in the browser.</em>
    <a href="https://github.com/coder/code-server">github.com/coder/code-server</a>
  </li>
  <li id="ref-16">
    <span class="ref-num">[16]</span>
    SST. <em>opencode — AI coding agent built for the terminal.</em>
    <a href="https://github.com/sst/opencode">github.com/sst/opencode</a>
  </li>
  <li id="ref-17">
    <span class="ref-num">[17]</span>
    GitHub. <em>GitHub Codespaces — Cloud-based development environments.</em>
    <a href="https://docs.github.com/en/codespaces">docs.github.com/en/codespaces</a>
  </li>
  <li id="ref-18">
    <span class="ref-num">[18]</span>
    Anysphere. <em>Cursor — AI code editor with remote-SSH support.</em>
    <a href="https://www.cursor.com">cursor.com</a>
  </li>
```

(If Task 0 found a different official URL or arXiv id for any project, use it. The text "github.com/&lt;org&gt;/&lt;repo&gt;" pattern is canonical for now.)

- [ ] **Step 2: Write `related-work.html`**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>相关工作 — agentserver + loom intro</title>
<link rel="stylesheet" href="assets/style.css">
</head>
<body>
<nav class="intro-nav">
  <a href="index.html">首页</a>
  <a href="system-abstraction.html">系统抽象</a>
  <a href="platform-stack.html">agentserver</a>
  <a href="layer.html">Layer</a>
  <a href="tier.html">Tier</a>
  <a href="cycle.html">Cycle</a>
  <a href="related-work.html" class="current">相关工作</a>
  <a href="references.html">参考文献</a>
</nav>
<main>
<h1>相关工作</h1>

<p class="lead">
没人做出来 agentserver + loom 这个组合,不是巧合。
<strong>上层 multi-agent 框架普遍假设单机 / 同 process;
底层 coding-agent 平台普遍假设 1:1 用户↔机器。</strong>
本栈是第一个把这两个假设同时打破的——这就是它的位置。
</p>

<h2>对比表(15 项目 × 9 维度)</h2>

<p>图例:<code>✅</code> first-class 支持;<code>△</code> 有限或间接支持;<code>×</code> 不支持或不在范围内;
<code>n/a</code> 对该项目类别不适用;<code>?</code> 闭源/无公开材料无法判断。</p>

<table>
<tr>
  <th>项目</th>
  <th>多机</th>
  <th>异构平台 (arm/Android)</th>
  <th>浏览器内 IDE</th>
  <th>Agent 间 RPC</th>
  <th>Capability 动态扩</th>
  <th>自验真 gate</th>
  <th>HITL pause/resume</th>
  <th>DAG 编排</th>
  <th>Compute encoding 立意</th>
</tr>
<tr>
  <td>code-server <sup><a href="references.html#ref-15">[15]</a></sup></td>
  <td>×</td><td>△</td><td>✅</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td>GitHub Codespaces <sup><a href="references.html#ref-17">[17]</a></sup></td>
  <td>✅(cloud only)</td><td>×</td><td>✅</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td>Cursor remote <sup><a href="references.html#ref-18">[18]</a></sup></td>
  <td>△(SSH 1:1)</td><td>△</td><td>✅</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td>opencode <sup><a href="references.html#ref-16">[16]</a></sup></td>
  <td>△</td><td>△</td><td>△</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td><strong>agentserver (alone)</strong> <sup><a href="references.html#ref-4">[4]</a></sup></td>
  <td>✅</td><td>✅</td><td>✅</td><td>✅(peer-proxy)</td><td>×</td><td>×</td><td>×</td><td>×</td><td>△</td>
</tr>
<tr>
  <td>AutoGen <sup><a href="references.html#ref-6">[6]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>△(in-proc)</td><td>×</td><td>×</td><td>△</td><td>✅</td><td>×</td>
</tr>
<tr>
  <td>CrewAI <sup><a href="references.html#ref-7">[7]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>△</td><td>×</td><td>×</td><td>△</td><td>✅</td><td>×</td>
</tr>
<tr>
  <td>LangGraph <sup><a href="references.html#ref-8">[8]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>△</td><td>×</td><td>×</td><td>△</td><td>✅</td><td>×</td>
</tr>
<tr>
  <td>OpenHands <sup><a href="references.html#ref-9">[9]</a></sup></td>
  <td>△</td><td>×</td><td>△</td><td>×</td><td>×</td><td>×</td><td>×</td><td>△</td><td>×</td>
</tr>
<tr>
  <td>Letta <sup><a href="references.html#ref-10">[10]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>×</td><td>△(memory only)</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td>metaGPT <sup><a href="references.html#ref-11">[11]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>△</td><td>×</td><td>×</td><td>×</td><td>✅</td><td>×</td>
</tr>
<tr>
  <td>smol-developer <sup><a href="references.html#ref-12">[12]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td>aider <sup><a href="references.html#ref-13">[13]</a></sup></td>
  <td>×</td><td>n/a</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td><td>×</td>
</tr>
<tr>
  <td>Devin (closed) <sup><a href="references.html#ref-14">[14]</a></sup></td>
  <td>✅(cloud)</td><td>×</td><td>✅</td><td>?</td><td>×</td><td>×</td><td>×</td><td>?</td><td>×</td>
</tr>
<tr>
  <td class="highlight"><strong>agentserver + loom (本栈)</strong> <sup><a href="references.html#ref-5">[5]</a></sup></td>
  <td class="highlight">✅</td><td class="highlight">✅</td><td class="highlight">✅</td><td class="highlight">✅</td>
  <td class="highlight">✅</td><td class="highlight">✅</td><td class="highlight">✅</td><td class="highlight">✅</td>
  <td class="highlight">✅</td>
</tr>
</table>

<p>关注 <strong>agentserver (alone)</strong> 那行和 <strong>agentserver + loom</strong> 那行的差别:
loom 这一层在 agentserver 已有底子上,把 Capability 动态扩、自验真 gate、HITL pause/resume
三列从 × 拉到 ✅——这是本栈相对竞品的工程贡献。</p>

<h2>底层对比:远程 coding-agent 平台</h2>

<p>这一组的所有项目都把"算力 = 一台被用户绑定的机器"当默认。本栈把这个默认改了。</p>

<h3>code-server <sup><a href="references.html#ref-15">[15]</a></sup></h3>
<p>Coder 出的 VS Code 服务端,目标是"浏览器里编程"。一个 code-server 实例对应一台机器、
对应一个用户。它解决"我笔记本上没装环境"的问题,不解决"我需要协调 N 台机器的算力"的问题。</p>

<h3>GitHub Codespaces <sup><a href="references.html#ref-17">[17]</a></sup></h3>
<p>GitHub 托管的云端 dev container。云原生 vendor lock-in;不支持把已有的 self-host 机器拉进来;
没有 agent 之间互相调用的概念——每个 Codespace 是孤岛。</p>

<h3>Cursor remote <sup><a href="references.html#ref-18">[18]</a></sup></h3>
<p>Cursor 在 VS Code 之上加了 AI 能力,remote 模式靠 SSH 连一台机器。
仍然是 1:1 用户↔机器,不存在多机协同的概念。</p>

<h3>opencode <sup><a href="references.html#ref-16">[16]</a></sup></h3>
<p>SST 的 terminal-based AI 编程工具,在本地跑;有简单的 model provider 抽象。
单机/单用户路径;没有解决跨机器/跨成员的算力调用。</p>

<h3>agentserver (alone) <sup><a href="references.html#ref-4">[4]</a></sup></h3>
<p>本组织的底层栈;唯一一个把"workspace 内多 agent + 命名 + 反向 tunnel + peer-proxy"
做齐的方案。把"算力 = 一台被用户绑定的机器"改成"算力 = workspace 里一群可寻址的命名成员"。
本栈的下半段就是它。</p>

<h2>上层对比:multi-agent fabric</h2>

<p>这一组的所有项目都把"多 agent = 一个 Python 进程里多个 LLM 对象互相调"当默认。
本栈把这个默认改了——agent 在不同物理机器上跑,通过 agentserver 互相调用。</p>

<h3>AutoGen <sup><a href="references.html#ref-6">[6]</a></sup></h3>
<p>Microsoft Research 的多 agent 对话框架。强项:对话编排、UserProxyAgent 实现 in-process HITL。
弱项:agent 都是同 Python 进程的对象,没有跨机器的算力分布;tool 是开发期硬编码,无运行时扩展。</p>

<h3>CrewAI <sup><a href="references.html#ref-7">[7]</a></sup></h3>
<p>"Crew of agents with roles" 这个比喻深入人心。强项:role-playing 编排 + 任务流。
弱项:同 AutoGen,单机/同 process;tool 是 Python 类硬编码;不支持跨成员能力发现。</p>

<h3>LangGraph <sup><a href="references.html#ref-8">[8]</a></sup></h3>
<p>LangChain 出的把 agent 流程画成 state machine 的库。强项:可视化的 graph 编排、状态持久化。
弱项:同上,单机假设;HITL 是 graph 节点级而非 chat-turn 级的暂停。</p>

<h3>OpenHands <sup><a href="references.html#ref-9">[9]</a></sup></h3>
<p>(原 OpenDevin) 开源的 coding agent 平台,有 web UI + sandbox container。
强项:开源、可自托管。弱项:1 实例对应 1 sandbox,没有"多 agent 互相调"的语义;
扩展能力靠 plugin 系统,不是 runtime 长出来的。</p>

<h3>Letta <sup><a href="references.html#ref-10">[10]</a></sup></h3>
<p>原 MemGPT;主打"stateful agent server"。强项:长期记忆的持久层。
弱项:agent 是一个独立服务,没有跨 agent 的能力发现/编排;多 agent 协作不是核心目标。</p>

<h3>metaGPT <sup><a href="references.html#ref-11">[11]</a></sup></h3>
<p>"software company simulation"——多 agent 扮演产品经理/工程师/QA 等角色。
强项:角色清晰、流程拟真。弱项:同 AutoGen/CrewAI,单 process;
角色和能力都是开发期定的,不能 runtime 长出新角色或新能力。</p>

<h3>smol-developer <sup><a href="references.html#ref-12">[12]</a></sup></h3>
<p>"in-app embedded developer agent" 的极简实现。强项:简单、嵌入式。
弱项:不是多 agent,不是分布式;能力固定。</p>

<h3>aider <sup><a href="references.html#ref-13">[13]</a></sup></h3>
<p>终端里的 AI pair programmer。强项:repo map、git 集成、HITL prompting。
弱项:单 user / 单 repo / 单机;不存在 agent 协作的概念。</p>

<h3>Devin <sup><a href="references.html#ref-14">[14]</a></sup></h3>
<p>Cognition 的闭源 AI software engineer 产品。能跑在云端,可能内部有分布式架构,
但闭源 + 公开材料有限,无法判断"跨成员能力发现/编排"的支持情况;HITL 也未公开。</p>

<h2>本栈的智识贡献</h2>

<p>对应上面对比表中本栈独占的列(✅ vs 其他全是 × 或 △):</p>

<ol>
  <li><strong>跨机器的多 agent 协调</strong> — 不是 in-process,而是真的跨物理机器。底层依赖 agentserver。</li>
  <li><strong>异构平台齐活</strong> — amd64/arm64/Android 同时支持,jetson + termux 都能跑(见 <a href="layer.html">layer</a>)。</li>
  <li><strong>Capability 动态扩</strong> — slave 缺能力 → driver 让 slave scaffold 一个新 MCP → 接入 → 后续复用(<a href="cycle.html#cycle-c">Cycle C</a>)。</li>
  <li><strong>自验真 gate</strong> — 新能力必须过 mcp-acceptance 验真才能 register。不是"模型说写好了就接入"。</li>
  <li><strong>HITL pause/resume</strong> — 不是 prompting 时的 yes/no,而是 chat 中途真正的 backend session 暂停 + 续接(<a href="cycle.html#cycle-d">Cycle D</a>)。</li>
  <li><strong>Compute encoding 立意</strong> — 把算力作为继程序、数据之后的第三种被编码对象(见 <a href="system-abstraction.html">系统抽象</a>),不是把 LLM 当 SDK 工具。</li>
</ol>

<div class="pager">
  <a href="cycle.html">← Cycle</a>
  <a href="references.html">参考文献 →</a>
</div>
</main>
</body>
</html>
```

- [ ] **Step 3: Visual check + commit**

```bash
git add docs/intro/related-work.html docs/intro/references.html
git commit -m "feat(intro): related-work.html + refs 6–18 — 15 项目 × 9 维度对比"
```

---

## Task 10: Pre-render the 7 SVG diagrams

**Files:**
- Create: `docs/intro/assets/diagrams/stack-overview.svg`
- Create: `docs/intro/assets/diagrams/layer-stack.svg`
- Create: `docs/intro/assets/diagrams/tier-triangle.svg`
- Create: `docs/intro/assets/diagrams/cycle-a-registration.svg`
- Create: `docs/intro/assets/diagrams/cycle-b-task.svg`
- Create: `docs/intro/assets/diagrams/cycle-c-capability.svg`
- Create: `docs/intro/assets/diagrams/cycle-d-humanloop.svg`

For each diagram: hand-write the SVG directly (no mermaid runtime). Each diagram is ≤ 200 lines of SVG. Use:
- viewBox `0 0 800 N` for consistent width
- `font-family="system-ui, sans-serif"` and `font-size="14"` for labels
- Stroke color `#2c4a7c` for arrows, `#555` for boxes, `#222` for text
- No images embedded, no JS

Below is the SVG for each. Copy verbatim.

- [ ] **Step 1: `stack-overview.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 480"
     font-family="system-ui, sans-serif" font-size="14">
  <!-- Title -->
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">agentserver + loom 栈全景</text>

  <!-- Model layer (top) -->
  <rect x="200" y="60" width="400" height="50" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
  <text x="400" y="90" text-anchor="middle">模型层 (Claude / Codex / 任意 LLM)</text>

  <!-- loom box -->
  <rect x="100" y="130" width="600" height="160" fill="#fff" stroke="#2c4a7c" stroke-width="2"/>
  <text x="120" y="155" font-weight="600" fill="#2c4a7c">loom — 算力应用层与编排</text>
  <rect x="140" y="170" width="240" height="40" fill="#efebe2" stroke="#555"/>
  <text x="260" y="195" text-anchor="middle">应用层 (contract / journal / humanloop)</text>
  <rect x="400" y="170" width="240" height="40" fill="#efebe2" stroke="#555"/>
  <text x="520" y="195" text-anchor="middle">能力层 (chat / bash / file / mcp / ...)</text>
  <rect x="270" y="230" width="240" height="40" fill="#efebe2" stroke="#555"/>
  <text x="390" y="255" text-anchor="middle">调度层 (driver MCP tools + dispatch)</text>

  <!-- agentserver box -->
  <rect x="100" y="310" width="600" height="120" fill="#fff" stroke="#2c4a7c" stroke-width="2"/>
  <text x="120" y="335" font-weight="600" fill="#2c4a7c">agentserver — 算力底层网络</text>
  <rect x="140" y="350" width="240" height="40" fill="#efebe2" stroke="#555"/>
  <text x="260" y="375" text-anchor="middle">协议层 (workspace / agent / task / session)</text>
  <rect x="400" y="350" width="240" height="40" fill="#efebe2" stroke="#555"/>
  <text x="520" y="375" text-anchor="middle">网络层 (tunnel + peer-proxy + broker)</text>

  <!-- Physical layer (bottom) -->
  <rect x="200" y="440" width="400" height="30" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
  <text x="400" y="460" text-anchor="middle">物理层 (host / docker / Jetson / Termux)</text>
</svg>
```

- [ ] **Step 2: `layer-stack.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 480"
     font-family="system-ui, sans-serif" font-size="14">
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">7 层堆叠(自底向上)</text>

  <!-- Layers from top to bottom; numbered 7→1 visually but ordering bottom-up -->
  <!-- 7 model -->
  <rect x="150" y="60" width="500" height="40" fill="#fbf9f4" stroke="#2c4a7c"/>
  <text x="170" y="85" font-weight="600">⑦</text>
  <text x="195" y="85">模型层 (Claude / Codex / 任意 LLM)</text>

  <!-- 6 app -->
  <rect x="150" y="110" width="500" height="40" fill="#efebe2" stroke="#555"/>
  <text x="170" y="135" font-weight="600">⑥</text>
  <text x="195" y="135">应用层 (contract / capability journal / humanloop)</text>

  <!-- 5 capability -->
  <rect x="150" y="160" width="500" height="40" fill="#efebe2" stroke="#555"/>
  <text x="170" y="185" font-weight="600">⑤</text>
  <text x="195" y="185">能力层 (chat / chat_resume / bash / file / mcp / register_mcp)</text>

  <!-- 4 dispatch -->
  <rect x="150" y="210" width="500" height="40" fill="#efebe2" stroke="#555"/>
  <text x="170" y="235" font-weight="600">④</text>
  <text x="195" y="235">调度层 (driver MCP tools + dispatch)</text>

  <!-- 3 protocol -->
  <rect x="150" y="260" width="500" height="40" fill="#fff" stroke="#2c4a7c"/>
  <text x="170" y="285" font-weight="600">③</text>
  <text x="195" y="285">协议层 (workspace / agent / task / session) — agentserver</text>

  <!-- 2 network -->
  <rect x="150" y="310" width="500" height="40" fill="#fff" stroke="#2c4a7c"/>
  <text x="170" y="335" font-weight="600">②</text>
  <text x="195" y="335">网络层 (tunnel + peer-proxy + broker) — agentserver</text>

  <!-- 1 physical -->
  <rect x="150" y="360" width="500" height="40" fill="#fbf9f4" stroke="#2c4a7c"/>
  <text x="170" y="385" font-weight="600">①</text>
  <text x="195" y="385">物理层 (host / docker / Jetson arm64 / Termux Android)</text>

  <!-- Labels for two halves -->
  <text x="60" y="135" fill="#2c4a7c" font-weight="600">loom</text>
  <line x1="130" y1="60" x2="130" y2="250" stroke="#2c4a7c" stroke-width="2"/>

  <text x="60" y="320" fill="#2c4a7c" font-weight="600">agent-</text>
  <text x="60" y="338" fill="#2c4a7c" font-weight="600">server</text>
  <line x1="130" y1="260" x2="130" y2="400" stroke="#2c4a7c" stroke-width="2"/>
</svg>
```

- [ ] **Step 3: `tier-triangle.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 400"
     font-family="system-ui, sans-serif" font-size="14">
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">driver / slave / observer 三角</text>

  <!-- Driver top -->
  <circle cx="400" cy="100" r="55" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="2"/>
  <text x="400" y="95" text-anchor="middle" font-weight="600">driver</text>
  <text x="400" y="115" text-anchor="middle" font-size="11" fill="#555">user-facing</text>

  <!-- Slave bottom-right -->
  <circle cx="600" cy="320" r="55" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="2"/>
  <text x="600" y="315" text-anchor="middle" font-weight="600">slave</text>
  <text x="600" y="335" text-anchor="middle" font-size="11" fill="#555">execution</text>

  <!-- Observer bottom-left -->
  <circle cx="200" cy="320" r="55" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="2"/>
  <text x="200" y="315" text-anchor="middle" font-weight="600">observer</text>
  <text x="200" y="335" text-anchor="middle" font-size="11" fill="#555">telemetry + replay</text>

  <!-- Arrow: driver → slave (DelegateTask) -->
  <line x1="445" y1="135" x2="560" y2="290" stroke="#2c4a7c" stroke-width="1.5" marker-end="url(#arr)"/>
  <text x="540" y="195" font-size="11" fill="#2c4a7c">DelegateTask</text>
  <text x="540" y="210" font-size="11" fill="#2c4a7c">+ TASK_CONTRACT</text>

  <!-- Arrow: slave → driver (peer-proxy /files) -->
  <line x1="555" y1="285" x2="440" y2="130" stroke="#555" stroke-width="1" marker-end="url(#arr2)"/>
  <text x="380" y="220" font-size="11" fill="#555">peer-proxy /files/blob/&lt;sha&gt;</text>

  <!-- Arrows to observer -->
  <line x1="345" y1="135" x2="245" y2="290" stroke="#888" stroke-width="1" stroke-dasharray="4,3" marker-end="url(#arr3)"/>
  <text x="245" y="200" font-size="11" fill="#555">events</text>

  <line x1="555" y1="335" x2="265" y2="335" stroke="#888" stroke-width="1" stroke-dasharray="4,3" marker-end="url(#arr3)"/>
  <text x="400" y="355" text-anchor="middle" font-size="11" fill="#555">events + artifacts</text>

  <defs>
    <marker id="arr" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#2c4a7c"/>
    </marker>
    <marker id="arr2" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#555"/>
    </marker>
    <marker id="arr3" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#888"/>
    </marker>
  </defs>
</svg>
```

- [ ] **Step 4: `cycle-a-registration.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 400"
     font-family="system-ui, sans-serif" font-size="14">
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">Cycle A — agentserver 注册 / 续约</text>

  <g transform="translate(400,220)">
    <!-- 5 nodes around a circle r=130 -->
    <g>
      <circle cx="0" cy="-130" r="50" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
      <text x="0" y="-128" text-anchor="middle" font-size="12">tunnel WS</text>
      <text x="0" y="-112" text-anchor="middle" font-size="11" fill="#555">handshake</text>
    </g>
    <g>
      <circle cx="124" cy="-40" r="50" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
      <text x="124" y="-38" text-anchor="middle" font-size="12">401</text>
      <text x="124" y="-22" text-anchor="middle" font-size="11" fill="#555">token 失效</text>
    </g>
    <g>
      <circle cx="76" cy="100" r="50" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
      <text x="76" y="98" text-anchor="middle" font-size="12">device-code</text>
      <text x="76" y="114" text-anchor="middle" font-size="11" fill="#555">浏览器批准</text>
    </g>
    <g>
      <circle cx="-76" cy="100" r="50" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
      <text x="-76" y="98" text-anchor="middle" font-size="12">新 token</text>
      <text x="-76" y="114" text-anchor="middle" font-size="11" fill="#555">写回 config</text>
    </g>
    <g>
      <circle cx="-124" cy="-40" r="50" fill="#fbf9f4" stroke="#2c4a7c" stroke-width="1.5"/>
      <text x="-124" y="-38" text-anchor="middle" font-size="12">reconnect</text>
      <text x="-124" y="-22" text-anchor="middle" font-size="11" fill="#555">成功</text>
    </g>

    <!-- arrows clockwise -->
    <g stroke="#2c4a7c" stroke-width="1.5" fill="none" marker-end="url(#arrA)">
      <path d="M 35,-100 Q 100,-100 100,-60"/>
      <path d="M 100,-10 Q 130,30 100,80"/>
      <path d="M 30,130 Q -30,130 -30,130"/>
      <path d="M -100,80 Q -130,30 -100,-10"/>
      <path d="M -100,-60 Q -100,-100 -35,-100"/>
    </g>
    <defs>
      <marker id="arrA" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
        <path d="M0,0 L8,4 L0,8 Z" fill="#2c4a7c"/>
      </marker>
    </defs>
  </g>
</svg>
```

- [ ] **Step 5: `cycle-b-task.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 360"
     font-family="system-ui, sans-serif" font-size="14">
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">Cycle B — 任务生命周期</text>

  <!-- Linear pipeline with feedback arc -->
  <g font-size="12">
    <rect x="40"  y="120" width="100" height="50" fill="#fbf9f4" stroke="#2c4a7c"/>
    <text x="90"  y="150" text-anchor="middle">submit_task</text>

    <rect x="170" y="120" width="100" height="50" fill="#efebe2" stroke="#555"/>
    <text x="220" y="150" text-anchor="middle">queue (agentserver)</text>

    <rect x="300" y="120" width="100" height="50" fill="#efebe2" stroke="#555"/>
    <text x="350" y="145" text-anchor="middle">slave poll</text>
    <text x="350" y="160" text-anchor="middle" font-size="10" fill="#555">+ dispatch</text>

    <rect x="430" y="120" width="100" height="50" fill="#efebe2" stroke="#555"/>
    <text x="480" y="145" text-anchor="middle">executor.Run</text>
    <text x="480" y="160" text-anchor="middle" font-size="10" fill="#555">+ chunks → sink</text>

    <rect x="560" y="120" width="100" height="50" fill="#efebe2" stroke="#555"/>
    <text x="610" y="145" text-anchor="middle">store.Complete</text>
    <text x="610" y="160" text-anchor="middle" font-size="10" fill="#555">+ observer event</text>

    <rect x="690" y="120" width="80" height="50" fill="#fbf9f4" stroke="#2c4a7c"/>
    <text x="730" y="145" text-anchor="middle">wait_task</text>
    <text x="730" y="160" text-anchor="middle" font-size="10" fill="#555">返回</text>

    <g stroke="#2c4a7c" stroke-width="1.5" fill="none" marker-end="url(#arrB)">
      <line x1="140" y1="145" x2="170" y2="145"/>
      <line x1="270" y1="145" x2="300" y2="145"/>
      <line x1="400" y1="145" x2="430" y2="145"/>
      <line x1="530" y1="145" x2="560" y2="145"/>
      <line x1="660" y1="145" x2="690" y2="145"/>
    </g>

    <!-- failure feedback arc -->
    <path d="M 660,145 Q 660,260 90,260 Q 90,180 90,170" stroke="#a04040" stroke-width="1" fill="none" stroke-dasharray="5,3" marker-end="url(#arrFail)"/>
    <text x="400" y="280" text-anchor="middle" fill="#a04040">失败 → driver 收到 failure_reason → 决定重试/降级/交回用户</text>
  </g>

  <defs>
    <marker id="arrB" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#2c4a7c"/>
    </marker>
    <marker id="arrFail" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#a04040"/>
    </marker>
  </defs>
</svg>
```

- [ ] **Step 6: `cycle-c-capability.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 420"
     font-family="system-ui, sans-serif" font-size="14">
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">Cycle C — 能力演化(scaffold → 验真 → register → 复用)</text>

  <g transform="translate(400,230)">
    <g>
      <rect x="-220" y="-150" width="120" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="-160" y="-128" text-anchor="middle" font-size="12">能力缺失</text>
      <text x="-160" y="-110" text-anchor="middle" font-size="11" fill="#555">driver 查 card 发现</text>
    </g>
    <g>
      <rect x="100" y="-150" width="140" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="170" y="-128" text-anchor="middle" font-size="12">scaffold-mcp-server</text>
      <text x="170" y="-110" text-anchor="middle" font-size="11" fill="#555">slave 写新 MCP</text>
    </g>
    <g>
      <rect x="160" y="20" width="140" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="230" y="42" text-anchor="middle" font-size="12">mcp-acceptance</text>
      <text x="230" y="60" text-anchor="middle" font-size="11" fill="#555">cases.jsonl 验真</text>
    </g>
    <g>
      <rect x="-50" y="120" width="140" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="20" y="142" text-anchor="middle" font-size="12">register_mcp</text>
      <text x="20" y="160" text-anchor="middle" font-size="11" fill="#555">+ republish card</text>
    </g>
    <g>
      <rect x="-260" y="20" width="140" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="-190" y="42" text-anchor="middle" font-size="12">下次任务复用</text>
      <text x="-190" y="60" text-anchor="middle" font-size="11" fill="#555">跨 session 累积</text>
    </g>

    <g stroke="#2c4a7c" stroke-width="1.5" fill="none" marker-end="url(#arrC)">
      <path d="M -100,-122 Q 50,-180 100,-122"/>
      <path d="M 200,-94  Q 280,-30 240,20"/>
      <path d="M 160,76   Q 100,120 90,148"/>
      <path d="M -50,148  Q -170,120 -190,76"/>
      <path d="M -190,20  Q -240,-50 -160,-94"/>
    </g>
  </g>

  <defs>
    <marker id="arrC" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#2c4a7c"/>
    </marker>
  </defs>
</svg>
```

- [ ] **Step 7: `cycle-d-humanloop.svg`**

```xml
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 420"
     font-family="system-ui, sans-serif" font-size="14">
  <text x="400" y="30" text-anchor="middle" font-size="18" font-weight="600">Cycle D — humanloop pause / resume</text>

  <g transform="translate(400,230)">
    <g>
      <rect x="-220" y="-150" width="160" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="-140" y="-128" text-anchor="middle" font-size="12">模型 invoke ask_user</text>
      <text x="-140" y="-110" text-anchor="middle" font-size="11" fill="#555">stop &amp; emit tool_call</text>
    </g>
    <g>
      <rect x="60" y="-150" width="180" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="150" y="-128" text-anchor="middle" font-size="12">humanloop server</text>
      <text x="150" y="-110" text-anchor="middle" font-size="11" fill="#555">forward via unix socket</text>
    </g>
    <g>
      <rect x="120" y="20" width="180" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="210" y="42" text-anchor="middle" font-size="12">backend exits</text>
      <text x="210" y="60" text-anchor="middle" font-size="11" fill="#555">result.kind=awaiting_user</text>
    </g>
    <g>
      <rect x="-90" y="120" width="180" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="0" y="142" text-anchor="middle" font-size="12">driver wait_task</text>
      <text x="0" y="160" text-anchor="middle" font-size="11" fill="#555">surface 问题给真人</text>
    </g>
    <g>
      <rect x="-300" y="20" width="180" height="56" fill="#fbf9f4" stroke="#2c4a7c"/>
      <text x="-210" y="42" text-anchor="middle" font-size="12">resume_task</text>
      <text x="-210" y="60" text-anchor="middle" font-size="11" fill="#555">claude --resume + 答复</text>
    </g>

    <g stroke="#2c4a7c" stroke-width="1.5" fill="none" marker-end="url(#arrD)">
      <path d="M -60,-122 Q 50,-180 60,-122"/>
      <path d="M 150,-94 Q 230,-30 210,20"/>
      <path d="M 120,76 Q 60,120 90,148"/>
      <path d="M -90,148 Q -210,120 -210,76"/>
      <path d="M -210,20 Q -250,-50 -140,-94"/>
    </g>
  </g>

  <defs>
    <marker id="arrD" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
      <path d="M0,0 L8,4 L0,8 Z" fill="#2c4a7c"/>
    </marker>
  </defs>
</svg>
```

- [ ] **Step 8: Visual check all diagrams**

Open each SVG via `file://$PWD/docs/intro/assets/diagrams/<name>.svg`. Each should render as a labeled diagram (boxes, arrows, text), no errors in browser console.

- [ ] **Step 9: Commit**

```bash
git add docs/intro/assets/diagrams/
git commit -m "feat(intro): 7 SVG diagrams (stack, layer, tier, 4 cycles)"
```

---

## Task 11: Cross-page link lint + final read-through

**Files:** None (verification only)

- [ ] **Step 1: Link integrity check**

```bash
# Find all ref-N citations in HTML pages and verify each has a matching id="ref-N" in references.html.
cited=$(grep -oE 'ref-[0-9]+' docs/intro/*.html | grep -v 'references.html' | sort -u)
defined=$(grep -oE 'id="ref-[0-9]+"' docs/intro/references.html | sed 's/id="//;s/"//' | sort -u)
echo "Cited refs:"; echo "$cited"
echo "Defined refs:"; echo "$defined"
echo "Cited but not defined (problem):"; comm -23 <(echo "$cited") <(echo "$defined")
```

Expected: third grep is empty. If not, either add the missing ref to `references.html` or fix the citation number in the citing page.

- [ ] **Step 2: Internal link check**

```bash
# All href="*.html" must point to files that exist.
for f in docs/intro/*.html; do
  for href in $(grep -oE 'href="[^"#]+\.html"' "$f" | sed 's/href="//;s/"//'); do
    if [ ! -f "docs/intro/$href" ]; then echo "BROKEN: $f → $href"; fi
  done
done
```

Expected: no `BROKEN` output.

- [ ] **Step 3: SVG src check**

```bash
for f in docs/intro/*.html; do
  for src in $(grep -oE 'src="[^"]+\.svg"' "$f" | sed 's/src="//;s/"//'); do
    if [ ! -f "docs/intro/$src" ]; then echo "MISSING SVG: $f → $src"; fi
  done
done
```

Expected: no `MISSING SVG` output.

- [ ] **Step 4: Manual click-through**

Open `docs/intro/index.html` in a browser. Click every link in the top nav (8 pages). From each page, click every internal link in main body. Confirm:
- No 404s
- All SVG diagrams visible (not broken image icons)
- Dark mode (system preference) flips text + SVG correctly
- Print preview shows clean layout (nav hidden, body full-width)

- [ ] **Step 5: Commit (no-op if nothing changed; else commit fixes)**

```bash
git status
# If lint required edits to fix dangling refs / broken links, stage them:
# git add docs/intro/*.html
# git commit -m "fix(intro): link lint — resolve dangling refs/broken hrefs"
```

---

## Task 12: ROADMAP entry + final commit

**Files:**
- Modify: `docs/superpowers/ROADMAP.md` (if it exists in current branch)

- [ ] **Step 1: Check if ROADMAP exists**

```bash
ls docs/superpowers/ROADMAP.md 2>/dev/null && echo "exists" || echo "skip step 2"
```

- [ ] **Step 2: If ROADMAP exists, add a "Docs" section**

If the file exists, append (or insert into appropriate section) a line:

```markdown
| — | [project intro HTML](../intro/index.html) | 项目介绍站(agentserver + loom 两段栈;system abstraction + layer/tier/cycle) | ✅ done |
```

(Adjust column structure to match what ROADMAP.md actually uses.)

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/ROADMAP.md
git commit -m "docs(roadmap): link to docs/intro/ project introduction site"
```

If ROADMAP doesn't exist, skip this commit (no-op).

---

## Done definition

- All 8 HTML pages live at `docs/intro/`, render in browser via `file://`.
- `references.html` has 18 entries (refs 1–18), all of which are cited somewhere.
- 7 SVG diagrams present at `docs/intro/assets/diagrams/`.
- `grep -oE 'ref-[0-9]+' docs/intro/*.html | grep -v 'references.html'` returns only numbers that exist as `id="ref-N"` in references.html.
- All internal `href="X.html"` link to files that exist.
- Each page has a "core takeaway" lead paragraph (the spec's 凝练纪律 requirement) — verifiable by `grep -c 'class="lead"' docs/intro/*.html` showing 1 per page (except `references.html` which has none).
- Dark mode renders correctly (`prefers-color-scheme: dark` media query in CSS).
- Print preview (Cmd-P / Ctrl-P) shows clean layout.
- Total ~ 12 commits, no go test / no test infrastructure (the artifact is documentation).
