本文件为 paper_outputs/evaluation_v3.md §6.6 外部效度提供依据。
不产 §6.3 主实验数字。
不产 §6.5 消融数字。
核心 metric 仅抽 TaskSuccessRate / TimeToCompletion / WrongContextFailureRate / RoutingAccuracy 四个上表；divergent-unexplained 行数 = 0 是通过外部效度的硬门槛。

---

# WT-3-prod-multidevice — Prod-vs-Stub Analysis (Template)

This template is populated by the follow-up worktree
`paper/v3/p3-prod-multidevice-run`. Each `##` heading below is a
placeholder for a strong-divergence finding; if a metric row in
`prod_vs_stub.csv` reports `divergent-explained`, the analyst appends
the workload name to the heading and fills in the "根因候选" checklist
with the actual root cause identified during the run.

If no divergence occurs for a metric, its section stays empty (just the
heading + the "根因候选" list unchecked). Do NOT delete unused sections
— the presence of all four is what the follow-up worktree's
verification script expects.

---

## TaskSuccessRate abs_diff > 5pp: <workload>

根因候选 (check the one(s) that apply):

- [ ] OAuth round-trip — real device flow adds latency the stub short-
      circuits with an auto-signed token.
- [ ] 真 tunnel — agentserver-signed cross-machine tunnel introduces
      handshake / retry cost invisible to loopback stub.
- [ ] 跨机 RTT — physical distance between devices (esp. driver ↔ cloud
      slave-C) drops success rate on flaky workloads.
- [ ] 云 sandbox 冷启动 — first workload run on a fresh droplet pays
      cold-start cost that later runs don't.

Notes:

## TimeToCompletion rel_diff > 100%: <workload>

根因候选:

- [ ] OAuth round-trip — the first call per workload does the token
      exchange; stub skips this entirely.
- [ ] 真 tunnel — packet-level tunnel encapsulation adds per-hop cost.
- [ ] 跨机 RTT — bandwidth * distance dominates when workload is
      network-bound.
- [ ] 云 sandbox 冷启动 — first launch of the slave-C daemon on the
      droplet.

Notes:

## WrongContextFailureRate abs_diff > 5pp: <workload>

根因候选:

- [ ] OAuth round-trip — timeouts during token exchange cause the
      driver to route to a wrong context.
- [ ] 真 tunnel — tunnel drop causes fall-back routing.
- [ ] 跨机 RTT — routing decisions time out on distant slaves.
- [ ] 云 sandbox 冷启动 — cold-start slave-C fails capability
      discovery, driver routes elsewhere.

Notes:

## RoutingAccuracy abs_diff > 5pp: <workload>

根因候选:

- [ ] OAuth round-trip — routing decision made before OAuth returns;
      driver rescinds to a fallback path.
- [ ] 真 tunnel — tunnel establishment delay defeats routing time budget.
- [ ] 跨机 RTT — RTT-sensitive routing heuristic misfires.
- [ ] 云 sandbox 冷启动 — capability snapshot returns partial results
      during cold start, driver picks the wrong slave.

Notes:

---

## 降级 2 设备 影响段

Populate this section **only when `topology_used.json.topology_enum ==
"2-device-min"`**. When set, the run has dropped BOTH the Windows
device AND the cloud sandbox — the following coverage axes are lost:

- **无 cross-OS coverage**: `windows-only-artifact` workload is
  unavailable in this config (it is removed from the topology's
  workloads list). Any external-validity claim covering Windows-only
  artifacts remains unproven by this run — flag in the paper's
  external-validity discussion.
- **无 cross-internet tunnel coverage**: `agent.cs.ac.cn` real tunnel
  cost is unmeasured because slave-C is absent. Any
  `TimeToCompletion` divergence between prod and stub cannot be
  attributed to cross-internet RTT here.

Impact on §6.6 divergence gate: `divergent-unexplained = 0` MUST be
computed only over metrics reachable in the reduced topology (i.e.
`cross-device-code-mod` × 4 metrics = 4 rows). Do not silently pass
a run that lost 4 rows from the 4-device config's 8-row set.

---

## Handoff

本文件是模板（`analysis_template.md`），未含真数据；真数据由
`paper/v3/p3-prod-multidevice-run` 产 `analysis.md`（无 `_template`
后缀，位于 `tests/eval/results/prod/run-<timestamp>/analysis.md`）。
