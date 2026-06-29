# refund-policy-checker — E4 task family

> Tool produced by user-promoted固化: `check_refund_eligibility`

Given an order record and a refund policy doc, return `{eligible, reason,
policy_clause}`. Stage A reads policy + order with a one-shot script;
Stage B固化 builds an MCP that loads the (configurable) policy once and
checks each order against it. Reuse-N exercises additional orders that
hit different policy clauses.

## Layout

| Path | Role |
|---|---|
| `first-task/`              | First refund check — exercise window-expiry clause |
| `reuse-1/`                 | Final-sale clause hit (not eligible) |
| `reuse-2/`                 | Damaged-on-arrival clause (eligible, special path) |
| `reuse-3/`                 | Happy path — within window, refundable category |
| `acceptance/cases.jsonl`   | Stage B固化 gate |

## Running once the MCP exists

```bash
skills/mcp-acceptance --tool check_refund_eligibility \
  --cases tests/eval/golden/refund-policy-checker/acceptance/cases.jsonl

tools/eval/runner --spec tests/eval/golden/refund-policy-checker/first-task/spec.yaml
```

Oracle deep-equals the tool's output against `expected/verdict.json`.
The policy doc (`policy.md`) is identical across all five entries — it
represents the固化 policy snapshot.
