# driver-files-multi-mcp

Scenario-shaped example for a driver-side file task that needs more than one
new MCP service.

## Story

The user uploads two files to the driver:

- `driver-files/orders.csv`: recent orders and refund requests.
- `driver-files/refund-policy.md`: business policy for deciding refund risk.

No current slave advertises tools for CSV profiling or policy checking. The
driver uses `bash` to generate Python sources on the builder slave and
`register_mcp` to install two independent services in the same first phase:

- `csv_profile`: read CSV text and return typed column stats, refund totals,
  and suspicious rows.
- `refund_policy_check`: read policy markdown plus structured order facts and
  return policy violations and recommended actions.

After both services are registered and advertised, the master replans use-phase
MCP calls, then writes a concise report to the driver write target.

## What This Example Tests

- Driver files are represented as `USER_FILES_MANIFEST` URL handles, not local
  filesystem paths on the master or slaves.
- Independent MCP registrations are represented as separate first-phase DAG
  roots so the orchestrator can dispatch them concurrently.

This is a fixture-level example. It does not require a live agentserver; the
Go test in `examples/dynamic-mcp/scenario_test.go` validates that the files,
manifest, contract, and expected first-phase plan remain internally consistent.
