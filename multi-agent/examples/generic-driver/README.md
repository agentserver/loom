# generic-driver example

End-to-end demo of the `driver-agent` from `cmd/driver-agent`. Spawns master, a `fileconcat` slave, and the driver under a fake Claude Code; round-trips two files through the workspace via the agentserver peer-proxy.

## Prerequisites

- An `agentserver/` build that includes the peer-proxy route (Tasks 1–3 of the implementation plan).
- Three registered yaml configs: one for `driver-agent`, one for `master-agent`, one for `agent-fileconcat`. Each must be `register`ed once against the same agentserver (see each binary's README).

## Run

```
AGENTSERVER_URL=https://agent.example.com \
DRIVER_CONFIG=$HOME/.config/multi-agent/driver.yaml \
MASTER_CONFIG=$HOME/.config/multi-agent/master.yaml \
FILECONCAT_CONFIG=$HOME/.config/multi-agent/fileconcat.yaml \
./scripts/e2e.sh
```

## Expected output

```
==> building binaries
==> starting master
==> starting fileconcat slave
==> running e2e
ok: tools/list returned all six tools
ok: submit_task returned t-...
ok: out.txt = hello world
ok: audit log records all four event types
E2E PASS
```

## Smoke test (no agentserver required)

```
go test ./examples/generic-driver/e2e -run TestSmoke -v
```

This boots `driver-agent serve-mcp` against a local fixture config (no real workspace needed) and verifies the MCP handshake + `tools/list` round-trip. Useful for CI.
