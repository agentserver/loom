# driver-agent serve-daemon - test evidence

- Date: 2026-06-15
- Branch: worktree-driver-daemon
- Implementation branch: worktree-driver-daemon
- Scope: PR-2 of commander-web-entry vision - daemon mode, WS out, HTTP API
- Out of scope: observer-side hub, web UI, master path

## Why unit + binary smoke is sufficient

PR-2 is a greenfield daemon package plus one new driver-agent subcommand. The
observer-side WebSocket hub is PR-3, so cross-service runtime is covered here
with in-process httptest/gorilla fixtures rather than a real observer.

Coverage:

- WS envelope round-trips for register, command, event, and error constants
- Handler forwards Backend.ListSessions/GetSession/RunResume and preserves
  ErrSessionNotFound
- HTTP routes for /healthz, /sessions, /sessions/{id}, SSE turn streaming,
  and bearer-token enforcement
- WS client dial/register, Bearer auth, command dispatch, stream events,
  connection-scoped turn cancellation, per-session turn serialization,
  heartbeat, WebSocket ping/pong half-open detection, inbound read limit,
  reconnect on drop, 401/403 terminal shutdown, backoff reset,
  observer ack-linked state, turn lock cleanup, and schema mismatch shutdown
- Daemon serves the same data through HTTP and WS, exposes a Ready signal,
  sets HTTP ReadHeaderTimeout, and shuts down cleanly
- Driver config daemon defaults and explicit override
- Driver-agent serve-daemon flag parsing, observer URL to WS URL mapping,
  shared backend construction, and release ldflags version injection smoke
- Full repository race run
- CGO=0 binary smoke

The optional real-token daemon smoke was skipped intentionally: it requires an
existing driver.yaml with credentials.proxy_token. The automated WS tests cover
the same wire behavior without reading or exposing local credentials.

## Verification

```text
go vet ./...
=> exit 0
```

```text
go test ./... -race -count=1
=> exit 0
```

```text
git diff --stat -- cmd/master-agent/** internal/orchestrator/** internal/orchestration/**
=> empty

git diff --stat master..HEAD -- cmd/master-agent/** internal/orchestrator/** internal/orchestration/**
=> empty
```

```text
CGO_ENABLED=0 go build -o /tmp/bin-smoke-pr2/driver-agent ./cmd/driver-agent
=> exit 0

/tmp/bin-smoke-pr2/driver-agent
=> exit 2, usage includes:
   driver-agent serve-daemon  --config /path/to/driver.yaml [--listen host:port]
```

```text
CGO_ENABLED=0 go build -ldflags '-X main.driverVersion=vtest' -o /tmp/bin-smoke-pr2/driver-agent-ldflags ./cmd/driver-agent
=> exit 0
```

## Commit sequence

```text
044c9bb feat(driver-agent): serve-daemon subcommand
14bc790 feat(driver): daemon config defaults
1224e96 feat(commander): daemon orchestrator
7cf60a9 feat(commander): WebSocket client transport
f468610 feat(commander): HTTP API and SSE transport
d6384e1 feat(commander): shared backend handler
5963265 feat(commander): WS envelope schema
0515cf7 docs: implementation plan for driver-agent serve-daemon (commander PR-2)
8f97003 docs: spec for driver-agent serve-daemon (commander PR-2)
```

## Open items for PR-3

- Observer must implement WS endpoint at /api/daemon-link.
- Observer must send `{ "type": "ack" }` after accepting a register frame and
  validating schema_version so daemon health reports linked: true.
- Observer must route each user's daemon links by agentserver subject.
- Observer must validate Bearer ProxyToken via the existing identity.Resolver
  chain.
- Observer must reject schema mismatches with error code
  schema_version_mismatch; daemon now treats that as terminal.
