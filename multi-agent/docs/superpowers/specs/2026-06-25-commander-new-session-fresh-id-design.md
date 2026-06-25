# Commander New-Session: backend mints fresh session ID

- Status: Draft
- Date: 2026-06-25
- Owner: yzs15
- Parents:
  - `2026-06-25-commander-new-session-design.md`
  - `2026-06-25-commander-new-session-followups-design.md`

## Problem

The `+` button mints a UUID client-side and POSTs the first turn with that
UUID. The codex backend's `workerBackend.NewSessionWorker` then calls
`thread/resume` (per `pkg/agentbackend/codex/appserver_manager.go:639`).
codex's `thread/resume` requires an existing rollout file on disk — for a
fresh UUID with no rollout, it errors:

```
codex exit: exit status 1: Error: thread/resume: thread/resume failed:
no rollout found for thread id 20023e1f-30cd-42df-aedb-2b83846b493f (code -32600)
```

The handler's fallback path is `Backend.RunResume`, which is the
executor-mode equivalent of `codex exec resume <sid>` — also a resume, also
requires existing state.

Codex's `thread/start` RPC creates a fresh thread but does NOT accept a
client-supplied thread ID (`ThreadStartParams` has no `threadId` field —
the codex app-server mints the ID itself).

So the assumption baked into both prior specs ("client mints UUID → server
implicit-creates on first reference") does not hold for codex. Other
backends (claude, opencode) may behave similarly; today only codex is the
shipped backend that hit this.

## Goals

- The `+` flow ends with a working session that has a real backend rollout
  AND appears in `list_sessions`.
- Existing resume flow (clicking a session that already exists in the tree)
  continues to work unchanged.
- Frontend keeps its placeholder UX so the user immediately sees a chat
  composer after clicking `+` — no extra round-trip before they can type.

## Non-goals

- No change to how list_sessions / GetSession work for existing sessions.
- No change to how subsequent turns on the new session are dispatched (once
  the rollout exists, `thread/resume` works normally).
- No change to claude / opencode backends in this spec (they may need an
  analogous fix later, but the immediate user-reported failure is codex).

## Architecture

### Protocol

`SessionTurnArgs` gains one field:

```go
type SessionTurnArgs struct {
    ID     string `json:"id"`
    Prompt string `json:"prompt"`
    Fresh  bool   `json:"fresh,omitempty"`  // new: client minted ID, no rollout yet
}
```

Frontend sets `fresh: true` when posting the FIRST turn on a `pendingSession`
(i.e. when `pendingSession.phase === 'draft'` for the session being submitted).
Subsequent turns leave it unset.

A new SSE event type, `session_id`, carries the backend's real session ID
when it differs from the client's:

```
event: session_id
data: {"session_id":"<codex-minted-id>"}
```

Emitted at most once per turn. The frontend listens for it inside `postTurn`
and surfaces it to `CommanderApp` via a new callback so pending state can
rebind to the real ID.

### Slave handler change

`Handler.SessionTurn(ctx, id, prompt, sink)` becomes
`Handler.SessionTurn(ctx, id, prompt, fresh, sink)`. When `fresh=true`:

1. Skip `trySessionWorker` entirely (it depends on existing rollout).
2. Call `h.Backend.Run(ctx, agentbackend.Task{Prompt: prompt}, sink)` — the
   executor-mode "new session" path that uses `codex exec` and lets codex
   auto-mint the thread ID.
3. The codex executor already writes a sidecar with the minted thread ID
   (`pkg/agentbackend/codex/executor.go:205` — "current-session marker:
   written on BOTH Run and RunResume"). After `Run` returns, parse the
   sidecar to obtain the minted ID.
4. Emit `event: session_id\ndata: {"session_id":"<minted-id>"}` to the sink
   so the frontend can rebind.
5. `Run`'s normal result events (`status`, `chunk`, `done`) flow through
   unchanged.

When `fresh=false`: existing flow unchanged.

### Frontend change

`postTurn(daemonID, sessionID, prompt, onEvent, opts?)` gains:
- `opts.fresh?: boolean` — sent as `?fresh=1` query param OR as `{fresh:true}`
  in the JSON body (HTTP handler reads from body; query param is simpler).
- `onEvent` already receives all SSE events; the new `session_id` event
  threads through it.

`sendPrompt` in `CommanderApp.tsx`:
- Before posting, check `pendingSessionRef.current?.phase === 'draft' &&
  pendingSessionRef.current.sessionID === submitted.sessionID`. If so, set
  `fresh: true` in the postTurn call.
- Inside the onEvent callback, when `event === 'session_id'`, capture the
  real backend ID. After the turn `done` event, rebind:
  - `selectedRef.current = { daemonID, sessionID: realBackendID }`
  - `setSelected(...)`
  - `setPendingSession(prev => prev ? { ...prev, sessionID: realBackendID, phase: 'submitting' } : null)`
  - `pendingSessionRef.current = { ...same }`
  - Then `void loadTree()` — when the tree returns and the real row appears,
    pending clears as before.

The existing pending → submitting flip stays; the ID rebind happens in the
same step.

### HTTP layer

`commanderhub` HTTP handler at `/api/commander/daemons/<d>/sessions/<sid>/turn`:
- Add `fresh bool` to the JSON body schema.
- Pass through to `commander.SendCommandStream` payload (the
  `SessionTurnArgs` JSON gains the field automatically since the protocol
  type is updated).

The `session_id` SSE event is opaque to the hub — it streams whatever the
slave writes to the sink.

## Files

| File | Change |
|---|---|
| `internal/commander/protocol.go` | `SessionTurnArgs` adds `Fresh bool` field. |
| `internal/commander/handler.go` | `SessionTurn` accepts new `fresh` arg; routes to `Backend.Run` + sidecar-read when true; emits `session_id` SSE event. |
| `internal/commander/wsclient.go` | unmarshal `Fresh` from incoming session_turn; pass through to Handler.SessionTurn. |
| `internal/commanderhub/http.go` (`turn` handler) | parse `fresh` from POST body; pass to `commander.SessionTurnArgs.Fresh`. |
| `internal/commanderhub/webapp/src/api/client.ts` | `postTurn` accepts `opts.fresh`; body JSON includes `fresh`. |
| `internal/commanderhub/webapp/src/CommanderApp.tsx` | `sendPrompt` sets `fresh: true` for draft pending; handles `session_id` event; rebinds selected + pending to real ID. |
| `internal/commander/handler_test.go` | New case: fresh=true routes to Backend.Run, not RunResume; sidecar ID is emitted on the sink. |
| Frontend unit tests | `postTurn` body shape for fresh=true; CommanderApp handles `session_id` event and rebinds. |

## Test Strategy

### Go unit
- `handler_test.go`: fresh=true with a fake backend whose `Run` returns a
  session ID via sidecar — assert the handler emits a `session_id` event on
  the sink and does NOT call `trySessionWorker`.
- `handler_test.go`: fresh=false continues to use the existing trySessionWorker
  path (existing tests stay green).

### Frontend vitest
- `client.test.ts` (or extend existing): `postTurn(..., { fresh: true })`
  sends `fresh: true` in the JSON body.
- `CommanderApp.mobile.test.tsx`: `+` flow → first prompt → assert the
  POST body has `fresh: true`. Then mock the SSE stream to emit `session_id`
  + `done`; assert `pendingSession.sessionID` was rebound to the backend ID.

### Manual prod_test
- Click `+` on driver in the live commander UI; type any prompt; send.
  Expect: codex actually responds; session row appears in tree with real
  codex thread ID; virtual placeholder clears.

## Risks

- **`Backend.Run` is the "user-task" path, not the "session_turn" path.**
  The signatures differ: `Run(ctx, Task{Prompt}, sink)` vs the worker
  path's per-session worker. For codex this means we lose the per-session
  hot-worker cache for the FIRST turn (subsequent turns use trySessionWorker
  → `thread/resume` and DO get the cache). Acceptable: the first turn is
  inherently a cold start anyway.
- **Sidecar timing**: the codex executor writes the sidecar inside `Run`,
  but the slave handler needs to read it BEFORE returning. Verify the
  sidecar is flushed by the time `Run` returns successfully (per
  `executor.go:205` comment "written on BOTH Run and RunResume so" — the
  write is mid-Run, not post-Run).
- **Frontend rebind race**: if the user clicks a different session between
  `+` and `session_id` arrival, the rebind targets the OLD `selected`.
  Mitigate by reading `selectedRef.current` at rebind time and only
  rebinding if it still matches the submitted (placeholder) ID.
- **Other backends (claude, opencode)**: if their `Run` paths are not
  symmetric to codex's, `fresh=true` on those backends might mis-route.
  Out of scope here — codex is the only shipped backend hitting this.
  Add a `t.Skip` placeholder for the other backends with a TODO comment.
