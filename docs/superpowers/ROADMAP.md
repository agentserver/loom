# Roadmap

每条目对应 `docs/superpowers/specs/` 下一份 design + （已动工的）`docs/superpowers/plans/` 下一份 plan。状态以代码与 plan 是否合入为准，非以 spec 是否写完。

| 状态 | 含义 |
|---|---|
| ✅ DONE | spec + plan 已合入，主链路代码上线 |
| 🚧 IN-PROGRESS | plan 存在但仍在迭代 / 有未完成任务 |
| 📝 TODO | 仅 spec，尚未写 plan，也未动代码 |

---

## 📝 TODO

| Spec | 主题 | 备注 |
|---|---|---|
| [2026-05-25-mcp-marketplace-design.md](specs/2026-05-25-mcp-marketplace-design.md) | MCP server 市场（registry + publish + driver 端 fork/remix） | 仅 spec；下一步：写 plan（按 §11 实施顺序拆 8 步），起点 `internal/mcpmarket/{manifest,pack,sig,scanner}` |
| [2026-05-25-personal-mcp-skill-space-design.md](specs/2026-05-25-personal-mcp-skill-space-design.md) | 用户私有 MCP + Skill space（observer extension; pip+venv 心智；MCP 与 skill 同收） | spec v2 + plan: [2026-05-26-personal-mcp-skill-space](plans/2026-05-26-personal-mcp-skill-space.md)（10 tasks；自带 `internal/mcpmarket/{manifest,pack}`，marketplace 未来共享）|

---

## ✅ DONE

| Spec | Plan | 主题 |
|---|---|---|
| [slave-agent-design](specs/2026-04-27-slave-agent-design.md) | [slave-agent](plans/2026-04-28-slave-agent.md) | Slave agent 骨架 |
| [master-agent-design](specs/2026-04-28-master-agent-design.md) | [master-agent](plans/2026-04-28-master-agent.md) | Master agent 骨架 |
| [image-pipeline-e2e](specs/2026-05-08-image-pipeline-e2e-design.md) | [image-pipeline](plans/2026-05-08-image-pipeline.md) | 图像 pipeline e2e |
| [dynamic-mcp](specs/2026-05-09-dynamic-mcp-design.md) | [dynamic-mcp](plans/2026-05-09-dynamic-mcp.md) | Slave 端 `dynamic_mcp.yaml` 动态注册 |
| [generic-driver-agent](specs/2026-05-09-generic-driver-agent-design.md) | [generic-driver-agent](plans/2026-05-09-generic-driver-agent.md) | 通用 driver agent |
| [compute-packaging-repair](specs/2026-05-11-compute-packaging-repair-design.md) | [compute-packaging-repair](plans/2026-05-11-compute-packaging-repair.md) | 算力封装链路修复 |
| [typed-buildmcp-progress](specs/2026-05-13-typed-buildmcp-progress-design.md) | [typed-buildmcp-progress](plans/2026-05-13-typed-buildmcp-progress.md) | buildmcp 进度类型化 |
| [distributed-driver-master-contract](specs/2026-05-14-distributed-driver-master-contract-design.md) | [phase1 plan](plans/2026-05-14-distributed-driver-master-contract-phase1.md) | Driver↔Master 协议 phase 1 |
| [observer-artifact-relay-temporary](specs/2026-05-14-observer-artifact-relay-temporary-design.md) | [observer-lazy-artifact-relay](plans/2026-05-14-observer-lazy-artifact-relay.md) | Observer artifact 中继 |
| — | [bash-driven-mcp-registration](plans/2026-05-19-bash-driven-mcp-registration.md) | bash 驱动的 MCP 注册（无独立 spec） |
| [agent-observer-bootstrap](specs/2026-05-20-agent-observer-bootstrap-design.md) | [agent-observer-bootstrap](plans/2026-05-20-agent-observer-bootstrap.md) | Observer bootstrap |
| [observer-api-key-registration](specs/2026-05-20-observer-api-key-registration-design.md) | [observer-api-key-registration](plans/2026-05-20-observer-api-key-registration.md) | Observer API key 注册 |
| [codex-backend](specs/2026-05-23-codex-backend-design.md) | [codex-backend](plans/2026-05-23-codex-backend.md) | 可插拔 coding-agent（Claude + Codex） |
| [unregister-mcp](specs/2026-05-25-unregister-mcp-design.md) | [unregister-mcp](plans/2026-05-25-unregister-mcp.md) | `unregister_mcp` skill + driver tool |
| [observer-user-workspace](specs/2026-05-25-observer-user-workspace-design.md) | [observer-user-workspace](plans/2026-05-25-observer-user-workspace.md) | Observer 单用户 / 多 workspace 重塑（api_key 顶层化 + agent 自带 workspace_id + lazy 建 workspace）|
| [humanloop-resumable-chat](specs/2026-05-26-humanloop-resumable-chat-design.md) | [humanloop-resumable-chat](plans/2026-05-26-humanloop-resumable-chat.md) | Mid-chat human-in-the-loop（slave chat 内 ask_user/request_permission → pause → driver resume_task → claude --resume 续接 session）|
| [project-intro-html](specs/2026-05-27-project-intro-html-design.md) | [project-intro-html](plans/2026-05-27-project-intro-html.md) | 项目介绍 HTML 站（`docs/intro/`；agentserver+loom 双段栈 × layer/tier/cycle 三视角 × 15 项目对标）→ [打开 intro/index.html](../intro/index.html) |

---

## 🚧 IN-PROGRESS

（当前无；下次有 plan 落地但代码未完时挪到这里）

---

## 维护约定

- 新 spec 默认进 **TODO**
- 一旦对应 plan 合入：移到 **IN-PROGRESS**
- plan 全部任务完成 + 主链路代码 merge：移到 **DONE**
- 表里日期前缀与文件名保持一致，便于按时间排序
- 弃用的 spec 不删除文件，挪到本文件末尾 "Deprecated" 节并说明原因
