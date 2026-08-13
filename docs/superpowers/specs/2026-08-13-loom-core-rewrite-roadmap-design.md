# Loom 核心重写 Roadmap 与开发方法论

> **性质**：这是一份**总纲（roadmap）**，不是实施计划。它定义 demo 目标、开发方法论（分层精化 + 规范先行）、阶段路线、文档组织策略与约束。每个阶段的详细实施计划另行用 `writing-plans` 产出。
>
> **权威规范来源**：`refactored-driver-lifecycle-spec.html`（v1 范围已冻结：单成员 Workspace，成员生命周期延后）。本文不重述该 spec 的内容，只定义**如何把它落地**。

---

## 1. 目标与 demo 成功标准

**总目标**：按 `refactored-driver-lifecycle-spec.html` 近乎重写 Loom 的核心——那条条件 DAG 执行链（`TaskContract → child nodes → runs → RunEvent/RunProjection`）、driver daemon ↔ 隔离 coding agent、observer 作为唯一 control plane + Task Bus。现有 `internal/` 代码作为**参考与评分基线**，不保持可部署。

**Demo 成功标准（P2 完成时）**：在一个**真实分布式任务测试集**上，新系统的**任务通过率 > 旧系统 baseline**。这是量化、对比性的验收标准，也是驱动 P1/P2 迭代方向的北极星（见 §4）。

---

## 2. 方法论：分层精化 + 规范先行

把验证从"写完代码之后"**前移到设计阶段**。每一层：**先建模 → 用 TLC 验证不变量 → 确认它精化了上一层 → 再往下展开**；代码是最后一层的产物。理由：分布式重写几乎总是死在"组件间的并发/契约假设错了"，而这类 bug 正是 TLA+ 能在写一行代码之前抓出来的。

### 抽象层级（映射到 Loom）

| 层 | 建模什么 | 工具 |
|---|---|---|
| **L0 系统级抽象机** | User/Observer/Driver/Slave 抽象成角色，一次 run 的全局生命周期：Prompt → TaskContract → 条件 DAG → child runs → 终态收敛 | TLA+（高层 spec 风格） |
| **L1 组件级状态机**（精化 L0） | ① child node / run 状态机（被取代 / 部分完成 / 显式终止）② RunEvent/RunProjection 写事务—读接口开闭对称 ③ Task Bus 派发可靠性 + 幂等去重 ④ observer 授权裁决 / 撤销后审批失效 ⑤ driver daemon ↔ 隔离 coding agent 会话绑定单例 | PlusCal |
| **L2 详细设计** | 把验证过的状态机翻成模块划分、接口签名、数据结构、错误路径 | 设计文档 |
| **代码** | 按 L2 实现 | Go |

### 验证什么（两类属性，来自 spec 的"不变量"）

- **安全性 / Invariants**：没有 run 同时处于两个终态；projection 与事件流一致；派发不重复生效；owner 撤销后旧审批立即失效；authority 边界不被越权。
- **活性 / Temporal**：每个 run 最终到达终态（无卡死）；ready child 最终被派发；崩溃后 reconciliation 最终收敛到唯一当前视图。

### 建模范围要克制（单人开发的护栏）

TLA+ **只**建模并发/分布式控制逻辑与不变量。**不**建模：LLM 语义决策、能力文档内容、MCP 的 Python 代码生成、UI 排版。分清这条，模型才小到 TLC 跑得动、也维护得起。

---

## 3. 阶段路线与文档组织

### 3.1 阶段路线（每阶段结束时系统端到端可跑）

```
L0 全局抽象机（一次成型，给全局护栏）
        │  精化
        ▼
L1 中"P1 核心相关"的机器（DAG生命周期/事件溯源/派发/授权）
        │  必须在写 P1 代码前验证通过
        ▼
P1 代码  ──►  P2 时轻量建模能力相关部分  ──►  P2 代码  ──►  DEMO
        （P0 骨架只需 L0 草图）
                                              P3 外围（demo 之后）
```

| 阶段 | 内容 | TLA 层级要求 |
|---|---|---|
| **P0 骨架** | 最细端到端：prompt → observer session → driver 形成 **1 节点** trivial contract → observer 准入 → Task Bus 派发到 slave 跑 echo → 结构化结果 → RunEvent → RunProjection → 终态 → 回显。组件可为桩。**无**条件 DAG / 恢复 / 能力协商。 | 仅 L0 草图 |
| **P1 DAG 核心（重头）** | 真正的多节点条件 DAG、事件溯源读写开闭对称、child node 状态机、reconciliation/崩溃恢复、幂等、authority 边界。**test-first**。 | L0 定稿 + 相关 L1 验证通过 |
| **P2 能力真实性** | capability snapshot v1、按需能力查询、动态 MCP 注册、slave worker session 鉴权、大结果走 objectstore。 | 能力/注册相关部分轻量建模 |
| **DEMO** | 在真实测试集上打分，超越 baseline。 | — |
| **P3 外围** | 用户系统 / device-code / workspace 管理 / Chat Web、release governance、成员生命周期。 | 按需 |

**核心原则**：不是"核心做完 → 端到端"，而是"**极薄端到端（P0）→ 加厚核心（P1）**"；也不是"把整套 TLA+ 从顶到底全验证完再写代码"，而是**按风险切片前移**——L0 一次成型，L1 只在对应阶段写代码前验证到位。

### 3.2 文档组织策略

**两种视角，明确分工**（架构文档需同时有静态视图与动态视图）：

| 视角 | 是什么 | 对应 TLA 层 | 角色 |
|---|---|---|---|
| **功能条状 / flows** | 少数几篇端到端流程文档，承载跨组件不变量与时序 | L0 | **索引** |
| **组件块状 / components** | 每个组件独占其状态机/接口/数据结构/错误路径 | L1 + L2 | **权威** |

- **组件文档是权威 artifact**（接口必须有唯一出处，避免两个真相源），代码按它写。
- **流程文档是"阅读顺序"与集成层**——薄、少，把组件串成端到端故事，托管跨组件不变量；它同时就是 **eval 场景定义**。
- **对齐**：L0 spec ↔ 流程文档；L1 PlusCal ↔ 组件文档。

**咬合纽带（策略核心）**：两种视角通过一条规则咬合——**全局流程文档（L0）只引用组件的 §契约 层，禁止深链进内部细节**。因此组件内部设计怎么改都不波及流程文档。

#### 树形组织：组件 = 文件夹；`contract.md` 恒单文件，`internal/`·`detail/` 恒文件夹

不要把一个组件的所有内容塞进单个文件。按抽象层组织：**`contract.md` 恒为单文件；`internal/`、`detail/` 从一开始就是文件夹**（入口 `index.md` + 按需子文件），不做"文件长大再变文件夹"的迁移——这样链接从第一天就稳定，长大只需加兄弟文件。

```
docs/design/                       # 活的设计树（随代码演进，独立于 specs/）
  flows/                           # L0 · 流程条状 · 索引层（少、薄）
    index.md                       #   flows 索引
    submit-run.md                  #   只引用各组件的 contract.md
    reconcile-after-crash.md
    owner-revocation.md
    system.tla                     #   L0 全局抽象机（co-located）
  components/                      # L1/L2 · 组件块状 · 权威层
    run-store/                     #   RunEvent/RunProjection（核心）
      contract.md                  #   §契约 —— 恒单文件；flows 只引用它
      internal/                    #   §内部设计 —— 恒文件夹
        index.md                   #     概览 + 顶层状态机（入口）
        dispatch-loop.md           #     子话题
        reconciler.md
      detail/                      #   §实现细节 —— 恒文件夹
        index.md                   #     入口
        schema.md
        error-paths.md
      run-store.tla                #   L1 PlusCal（组件根下的文件）
      traceability.md              #   不变量 ↔ conformance 测试映射
    task-bus/
      contract.md
      internal/
        index.md
    driver-bridge/                 #   较简单组件，省掉 detail/
      contract.md
      internal/
        index.md
```

| 路径 | 层 | 类型 | 谁引用 | 稳定性 | 对齐 |
|---|---|---|---|---|---|
| `contract.md` | §契约（高层） | **恒单文件** | **flows 只引用这个** | 稳定 | L0 接口 / L1 |
| `internal/`（`index.md` + 子文件） | §内部设计（中层） | 恒文件夹 | 实现者 | 中 | L1 PlusCal |
| `detail/`（`index.md` + 子文件） | §实现细节（底层） | 恒文件夹 | 实现者 / 代码 | 易变 | L2 → 代码 |
| `<name>.tla` | L1 规范 | 文件 | 验证 | — | PlusCal |
| `traceability.md` | 追溯桥 | 文件 | 验证 / 测试 | — | 不变量 ↔ 测试 |

**这个结构的收益**：

- **链接从第一天就稳定**：flow 引用 `components/run-store/contract.md`；内部长大时只往 `internal/` 加兄弟文件，**零迁移、零断链**（不会出现 `internal.md` → `internal/` 的改名）。
- **git 历史 = "谁改动谁受影响"信号**：改 `contract.md` 的 commit = 破坏性变更（波及 flows）；只改 `internal/`·`detail/` 下任意文件 = 不波及任何人。
- **追溯桥有物理落点**：`.tla` + `traceability.md` 与组件文档同处一文件夹，一个组件的规范/不变量/对应测试聚在一处。
- **一条规则、可预测**：层的形态固定（contract 是文件、internal/detail 是文件夹），AI/工具遍历生成都简单。

**约定与实操**：

- **`contract.md` 恒单文件，永不变文件夹**：若你觉得需要第二个 contract 文件，那是**组件本身该拆**的信号——由此守住"被引用的稳定面恒定小"。
- **`internal/`·`detail/` 恒文件夹**：入口统一叫 `index.md`（+ 按需子文件）；各层是**扁平兄弟**，不嵌套，靠引用方向表达 `contract ← internal ← detail`。
- **`.tla` / `traceability.md`**：组件根下的文件（不是抽象层，不建文件夹）；仅核心组件需要 `.tla`。
- **按复杂度伸缩**：核心组件（run-store / task-bus / child-node 状态机）三层齐全 + `.tla`；简单组件省掉 `detail/`。
- **just-in-time**：`flows/`（L0）先有；组件文件夹在即将设计/实现时才建，先写 `contract.md`，设计时充实 `internal/`，实现时加 `detail/`。**不要一上来把 30 个组件文件夹全建出来**。
- **两类 artifact 分开放**：`docs/design/`（活的设计树，随代码演进）与 `docs/superpowers/specs/`（带日期的一次性快照）性质不同，不要混放。

---

## 4. eval harness 作为北极星

Demo 标准是量化通过率，因此 **eval harness + 测试集不能等到最后**：

- **最晚 P1 末就存在**，最好 P0 就搭出雏形（哪怕 1–2 个 case）。
- 一旦存在，P1/P2 每次改动都能立刻打分——这是"验证/调试/评估"落地的载体（eval-driven development）。
- **baseline**：旧系统在同一 harness 上先跑一遍得到基线分数，即 demo 要超越的线。

**追溯桥（连接"验证过的设计"与"打分的实现"）**：把 TLA 的每条 invariant / temporal 属性，一一对应成代码里的 **conformance / 场景测试**。TLA 里验证的不变量 = 代码里可跑的断言；eval harness 的通过率是上层业务指标。两者共同构成完整的验证/评估体系，避免"模型与代码两张皮"。

---

## 5. 约束

- **单人开发**：阶段顺序推进，每阶段粒度小到一人可扛并端到端跑通；不做重并行 fan-out；一次一份实施计划。
- **旧系统仅作评分基线**：不需保持可部署，但要能在 eval harness 上跑出 baseline 分数；同时作为重写时的对照 oracle。
- **demo 节点**：P2 完成即 demo，验收 = 真实测试集通过率 > baseline。
- **v1 范围冻结**：单成员 Workspace，成员生命周期与外围延后至 P3。

---

## 下一步

按本 roadmap，下一步用 `writing-plans` 对 **P0（walking skeleton）** 出一份详细实施计划——它是眼下唯一可立即执行的块。之后每个阶段各出一份 plan。
