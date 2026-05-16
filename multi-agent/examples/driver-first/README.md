# Driver-First Orchestration Example

This example documents the intended driver-first contract flow.

## Direct Slave

Use this when exactly one slave satisfies the required skills and MCP tools.

Expected dry-run route:

```json
{
  "recommended_route": "direct_slave",
  "recommended_skill": "chat"
}
```

## Driver Fanout

Use this when the driver needs to coordinate multiple slaves, call tools across
slaves, build MCP services, or pause for follow-up clarification.

Expected dry-run route:

```json
{
  "recommended_route": "driver_fanout",
  "recommended_skill": "fanout"
}
```

## Master Fanout

Use this when compatibility or operator policy requires the remote master.

Expected dry-run route:

```json
{
  "recommended_route": "master_fanout",
  "recommended_skill": "fanout"
}
```

## Artifact Transport

Distributed driver, master, and slave deployments must exchange user files,
generated MCP code, and final reports through observer artifacts. Do not rely on
shared local filesystem paths for cross-machine execution.
