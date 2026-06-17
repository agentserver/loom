# Codex app-server hot session worker design

## Summary

Commander already has a backend-neutral hot worker cache through
`agentbackend.SessionWorkerBackend`, but the Codex backend does not implement
it yet. Codex turns currently fall back to `Backend.RunResume`, which starts a
new `codex exec resume <session_id> --json ...` process for every turn.

This design adds an experimental Codex hot worker backed by `codex
app-server`. The existing `codex exec resume` path remains the stable fallback.
The feature is opt-in until the app-server protocol integration has enough
production evidence.

## Goals

- Reuse a live Codex app-server thread for repeated Commander turns on the same
  session.
- Surface active hot workers accurately in Commander.
- Reduce per-turn latency caused by starting a fresh `codex exec resume`
  process.
- Preserve the current `RunResume` behavior as a compatibility fallback.
- Keep Commander observer/frontend independent from Codex-specific status text.

## Non-goals

- Do not replace `Backend.Run` or `Backend.RunResume`.
- Do not make Codex app-server the default path in the first implementation.
- Do not use app-server WebSocket as the default local transport.
- Do not implement hot workers for Claude or opencode in this design.
- Do not rely on app-server experimental fields without startup probing.

## Current behavior

`internal/commander.Handler.SessionTurn` first checks whether the backend
implements `agentbackend.SessionWorkerBackend`. If it does not, or if worker
creation reports `ErrSessionWorkerUnavailable`, Handler calls
`Backend.RunResume`.

`pkg/agentbackend/codex.Backend` currently implements only `Backend`.
`RunResume` resolves the historical session working directory and invokes:

```text
codex exec resume <session_id> --json \
  --dangerously-bypass-approvals-and-sandbox \
  --skip-git-repo-check -
```

The prompt is sent on stdin as `User answered: <answer>`. This is robust but
starts a new CLI process for every turn.

## Proposed architecture

Add a Codex app-server client and worker implementation inside
`pkg/agentbackend/codex`.

```text
codex.Backend
  - exec *executor                  existing codex exec / exec resume path
  - appServer *appServerManager     lazy app-server process/client manager
  - workerMode codexWorkerMode      off | app_server

codexAppServerManager
  - starts codex app-server lazily
  - owns JSON-RPC transport
  - initializes and probes protocol support
  - creates/resumes thread handles

codexSessionWorker
  - sessionID
  - workDir
  - thread handle/subscription
  - Run(ctx, prompt, sink)
  - Close()
  - implements agentbackend.HealthySessionWorker
```

Do not make the disabled/off path perform extra session reads. Today the
Commander handler returns immediately when the backend does not implement
`agentbackend.SessionWorkerBackend`; if Codex always implements that interface,
`SessionTurn` would call `GetSession` before discovering `worker_mode: off`.
Preserve the off-mode hot path by either:

- returning a plain backend that does not implement `SessionWorkerBackend` unless
  app-server mode is enabled, or
- adding a cheap capability check that the handler can evaluate before
  `GetSession`.

The preferred implementation is a small wrapper returned by the backend builder
only when `worker_mode: app_server` is enabled:

```text
codexBackend                         // existing stable backend; no worker method
codexWorkerBackend { *codexBackend } // adds NewSessionWorker only when enabled
```

If startup probe fails after the interface is exposed, `NewSessionWorker` returns
`agentbackend.ErrSessionWorkerUnavailable` so Commander falls back to the
existing `RunResume` path.

## Runtime context parity

The app-server path must preserve the effective runtime context of the existing
`codex exec resume` path. A hot worker is only a transport/process reuse
optimization; it must not change what Codex can see, which tools are available,
or how a resumed thread is configured.

Parity requirements:

- Resume the same Codex thread/session ID that `RunResume` would resume.
- Use the historical session working directory resolved by `resumeWorkDir`, not
  the daemon default directory, when a session has cwd metadata.
- Start Codex with the same environment inherited by the existing executor,
  including the same `CODEX_HOME` and authentication/config discovery behavior.
- Preserve `agent.extra_args` or an app-server equivalent for model/profile,
  sandbox, approval, and config overrides.
- Preserve Codex's own runtime skill/plugin discovery. Commander
  `discovery.skills` are routing/advertisement metadata and are not injected as
  Codex runtime skills; Codex skills come from Codex configuration and skill
  locations discovered under the effective `CODEX_HOME`/cwd.
- Preserve the humanloop MCP server used for `ask_user` and
  `request_permission`. If app-server cannot provide equivalent MCP wiring, the
  app-server worker must report `ErrSessionWorkerUnavailable` before executing a
  turn and let `RunResume` handle it.
- Preserve the existing resume prompt semantics unless an app-server API offers
  a direct user-turn equivalent that produces the same conversation state.

The implementation should expose this parity as tests or startup probes where
possible. If any required context cannot be mapped safely into app-server, the
Codex backend remains on the stable `RunResume` fallback.

The existing `RunResume` path is also part of this contract, not just the
fallback implementation. Before adding app-server workers, keep or add tests
that prove `RunResume`:

- resolves the historical session cwd before setting `cmd.Dir`
- preserves `agent.extra_args`
- inherits the same environment and `CODEX_HOME` discovery behavior as ordinary
  Codex CLI runs
- injects the humanloop MCP server for every resume turn
- does not treat Commander `discovery.skills` as Codex runtime skills

Because `RunResume` starts a fresh Codex process for every turn, it naturally
re-reads Codex config, skills, plugins, and MCP configuration each time. A
long-lived app-server worker must not silently keep stale runtime context after a
configuration change. The worker cache key or health check should include a
runtime context fingerprint covering at least effective cwd, `CODEX_HOME`,
`agent.extra_args`, and the app-server/MCP configuration inputs owned by Loom. If
that fingerprint changes, the existing worker is closed and the next turn creates
a new app-server worker or falls back to `RunResume`.

## Configuration

Add an explicit opt-in field to the driver config:

```yaml
agent:
  kind: codex
  worker_mode: app_server   # off | app_server; only codex consumes app_server
```

`worker_mode` is the backend opt-in and is intentionally separate from daemon
cache limits. Add it as a generic field on the existing flat `AgentConfig` /
`agentbackend.Config` carrier. Other backends should accept only the default
`off` behavior; `app_server` is valid only for `agent.kind: codex`.

Hot worker cache limits are daemon-level settings because they control the
Commander handler cache, not Codex itself:

```yaml
daemon:
  worker_max: 10
  worker_idle_timeout: 10m
```

If these settings are not implemented in the first app-server PR, leave the
existing handler defaults in place.

Default:

```yaml
agent:
  worker_mode: off
```

This keeps production behavior unchanged until the operator explicitly enables
the experimental path.

## Protocol and transport

Use Codex app-server as follows:

- Transport: `stdio://` by default.
- Framing: JSON-RPC 2.0 messages, matching app-server documentation.
- App-server request API: v2 thread/turn flow.
- Initialization: include `experimentalApi: true` only for methods/fields that
  require it.
- Version handling: probe capabilities at startup; do not assume every Codex
  version has the same method set.

Do not use `ws://` for the daemon-internal default path. WebSocket transport is
useful for debugging and remote clients, but it expands the local attack surface
and is documented as experimental/unsupported for direct exposure.

### Schema source

Implementation should generate or vendor a minimal test fixture from:

```text
codex app-server generate-json-schema --out <dir>
```

Do not generate schemas at runtime. Runtime should probe the live app-server
with a small set of required methods and fall back if the required flow is not
available.

Required methods/capabilities for the first implementation, based on the schema
generated by Codex CLI 0.140.0:

- initialize
- `thread/resume`
- `turn/start`
- `turn/started`
- `item/agentMessage/delta`
- `turn/completed`
- error notification

Implementation should still confirm these names in a local smoke test, because
app-server is experimental and the method set can move across Codex versions.

## Worker lifecycle

### Creation

```text
NewSessionWorker(ctx, session)
  1. Check worker_mode == app_server.
  2. Ensure Codex app-server process/client is running.
  3. Probe required protocol support once per manager lifecycle.
  4. Resume the historical thread using session.ID.
  5. Subscribe to thread notifications needed by Run.
  6. Return codexSessionWorker.
```

If steps 2-5 fail before a turn starts, return
`ErrSessionWorkerUnavailable`.

### Run

```text
worker.Run(ctx, prompt, sink)
  1. Emit status_code=starting through the status sink helper.
  2. Send the prompt as a new turn on the resumed app-server thread.
  3. Emit status_code=answering once the turn is accepted/running.
  4. Map agent message deltas to sink.Write("chunk", text).
  5. Map approval/user-input requests to Result.AwaitingUser.
  6. Accumulate final assistant text and run agentbackend.SplitCapability.
  7. Emit sink.Write("capability", change) when SplitCapability finds a change.
  8. Map terminal turn result to executor.Result, including Summary,
     CapabilityChange, SessionID, and AwaitingUser.
```

After the app-server has accepted a turn, ordinary errors must not fall back to
`RunResume`; retrying would duplicate execution and can duplicate billing or
session writes. The worker should return the error; the Commander handler is
responsible for removing the failed worker from the cache and refusing fallback
after accepted execution.

### Close

`Close` releases the local thread subscription/handle. It does not delete,
archive, or mutate the Codex session. The app-server process can remain alive
while other workers are cached. If the manager owns a single app-server process,
daemon shutdown closes it after all worker refs finish.

### Health

`Healthy()` is not part of `agentbackend.SessionWorker`; it is the optional
`agentbackend.HealthySessionWorker` interface. `codexSessionWorker` should
implement it so the Commander handler can discard stale workers before sending a
turn. The check should be cheap and conservative:

- false if the app-server process exited
- false if the JSON-RPC connection is closed
- false if the worker has observed a protocol-fatal error
- true otherwise

## Commander status model

Do not teach observer or frontend to parse Codex-specific strings such as
`"starting codex"` or `"codex running"`.

Extend `commander.EventPayload` for status events:

```go
type EventPayload struct {
    EventKind  string          `json:"event_kind"`
    Text       string          `json:"text,omitempty"`
    Extra      json.RawMessage `json:"extra,omitempty"`
    StatusCode string          `json:"status_code,omitempty"`
}
```

This is an additive change. Preserve the existing `Extra` field; app-server and
future backend events may already rely on it.

`Sink.Write(eventType, data string)` cannot carry structured status metadata by
itself. Add an optional status writer interface plus helper near the existing
sink abstraction, and expose it through `pkg/agentbackend` so backend packages
can call it without importing `internal/commander`:

```go
type StatusSink interface {
    WriteStatus(statusCode, text string)
}

func WriteStatus(s Sink, statusCode, text string) {
    if ss, ok := s.(StatusSink); ok {
        ss.WriteStatus(statusCode, text)
        return
    }
    s.Write("status", text)
}
```

The Commander `wsSink` and `sseSink` should implement `WriteStatus` and encode
both `text` and `status_code`. Backend executors and workers should call the
helper instead of raw `sink.Write("status", ...)` for status transitions.

Status codes:

```text
queued
starting
answering
awaiting_approval
done
error
```

Observer and frontend should prefer `status_code` when present. Existing text
values remain accepted for backward compatibility:

- `queued on daemon`
- `queued-on-daemon`
- `accepted by daemon`
- `starting codex`
- `codex running`

This status-code change can land before the app-server worker and improves the
current "queued -> done" jump for Codex exec as well. The first rollout step
must update the existing Codex exec path from raw `sink.Write("status", ...)` to
the status helper so there is a producer before observer/frontend consumers rely
on `status_code`.

## Humanloop and approval handling

The existing `codex exec` path injects the local humanloop MCP server per run
through `-c mcp_servers.loom_humanloop...`. App-server integration must not
silently drop this capability.

Preferred implementation:

- Start the app-server with equivalent MCP server configuration.
- Route user-input/approval notifications from app-server into
  `Result.AwaitingUser`.
- Preserve existing Commander behavior for multi-round human approval.

If app-server cannot support the equivalent humanloop MCP configuration, the
first implementation should keep app-server worker unavailable for Codex
entirely and return `ErrSessionWorkerUnavailable` so the existing exec-resume
path handles all turns. Do not ship an app-server path that silently removes
approval/user-input behavior.

## Fallback rules

Fallback to `RunResume` is allowed only before app-server starts executing the
turn:

| Failure point | Fallback? | Reason |
| --- | --- | --- |
| worker mode off | yes | stable default |
| Codex binary missing app-server | yes | unsupported local version |
| initialize/probe fails | yes | unsupported protocol |
| `thread/resume` fails before turn starts | yes | no execution happened |
| turn request rejected before accepted/running | yes | no execution happened |
| app-server error after turn accepted | no | avoid duplicate execution |
| stream disconnect after partial output | no | avoid duplicate execution |
| worker health false before Run | yes | no execution happened |

When fallback happens, emit a neutral status event:

```text
status_code=starting, text="hot worker unavailable; falling back to resume"
```

## Observability

Daemon logs should include:

- worker mode selected
- Codex CLI version
- app-server protocol probe result
- fallback reason
- worker cache hits/misses
- worker eviction/close reason

The Commander session row already has `active_worker`. Its semantics remain:
"the daemon currently has a hot worker cached for this session", not "a turn is
currently running".

## Security

- Prefer app-server `stdio://` transport so no extra socket is exposed.
- If `unix://` is introduced later, create the socket under a daemon-owned temp
  directory with `0700` parent permissions and remove it on shutdown.
- Do not bind app-server `ws://` by default.
- Preserve the existing Commander bearer/cookie protections; app-server is an
  internal implementation detail behind the daemon.

## Testing

### Unit tests

- fake app-server initialize/probe success
- fake app-server missing required method -> `ErrSessionWorkerUnavailable`
- `NewSessionWorker` resumes the requested session ID
- `Run` maps agent-message deltas to chunk events
- `Run` maps terminal result to `executor.Result`
- `Run` preserves `SplitCapability` behavior and emits capability events
- app-server error before turn accepted falls back
- app-server error after turn accepted does not fall back
- `Healthy` reflects process/connection death
- `Close` releases subscription without deleting the session
- `RunResume` preserves historical cwd, `agent.extra_args`, environment,
  `CODEX_HOME` discovery, and humanloop MCP injection

### Commander tests

- Handler uses app-server worker when backend implements
  `SessionWorkerBackend`
- `worker_mode: off` does not force an extra `GetSession` before `RunResume`
- Handler falls back only for pre-run unavailable errors
- active worker marker appears after a successful worker turn
- EventPayload status-code serialization preserves the existing `Extra` field
- status-code events move observer/frontend through queued -> starting ->
  answering -> done

### Integration smoke tests

These should be opt-in or CI-gated on Codex availability:

- start `codex app-server` with stdio
- initialize/probe protocol
- resume a known test thread or start a temporary thread
- send one small prompt
- observe at least one terminal event

## Rollout plan

1. Add backend-neutral `status_code` support to daemon, observer, and frontend.
2. Add app-server JSON-RPC client with fake-server tests.
3. Add Codex app-server manager and protocol probe.
4. Add `codexSessionWorker` behind `worker_mode: app_server`.
5. Enable in `tests/prod_test` only.
6. Measure latency and correctness against the existing exec-resume fallback.
7. Consider making app-server mode the default only after repeated manual and CI
   evidence across supported Codex CLI versions.

## Risks

- App-server protocol is experimental and can change. Mitigation: opt-in mode,
  version/protocol probe, fallback to `RunResume`.
- Humanloop MCP injection may not map cleanly to long-lived app-server.
  Mitigation: keep fallback until approval flow is proven.
- A single app-server process might serialize or couple sessions unexpectedly.
  Mitigation: fake-server tests plus production observation; allow future
  per-worker process mode if needed.
- Streaming notification semantics may differ from `codex exec --json`.
  Mitigation: treat app-server event mapping as a separate adapter with fixtures.

## Acceptance criteria

- With `worker_mode: off`, behavior is unchanged, including no extra session
  file read before `RunResume`.
- With `worker_mode: app_server` and supported Codex CLI, two Commander turns on
  the same session reuse a cached `SessionWorker`.
- If app-server startup/probe fails, the turn still succeeds through
  `codex exec resume`.
- Once app-server accepts a turn, errors do not trigger `RunResume` retry.
- Commander UI can show `starting` and `answering` using backend-neutral
  `status_code`.
- Tests cover app-server success, fallback, and no-duplicate-execution paths.
