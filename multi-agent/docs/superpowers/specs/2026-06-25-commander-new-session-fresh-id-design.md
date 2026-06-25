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

The backend's real session ID rides in the existing terminal `done`
SSE payload at **exactly one path**: `data.result.session_id`. The
existing `marshalTurnResult` already shapes the payload as
`{"result": {...}}`, so `session_id` is a new sibling of whatever
`result` already carries:

```
event: done
data: {"result": {"session_id":"<codex-minted-id>", ...existing fields...}}
```

The frontend reads `data.result.session_id`. The hub rekey reads
`data.result.session_id`. Slave handler writes
`data.result.session_id`. No alternative path is acceptable — a shape
mismatch silently fails rebind + rekey, reintroducing the original
bug.

Rationale: the existing SSE sink layer (`internal/commander/sink.go` +
`sseSink.Write`) only carries text payloads — the daemon-link envelope's
`EventPayload.Text` field is what propagates through hub. A new
`session_id` SSE event would either need a whole new structured event
type or get stringified through `{"text": ...}`. Reusing `done`
sidesteps both — `done` is already terminal AND already carries a JSON
result that can be extended without touching the sink layer.

It also fixes the ordering problem: the hub's stream loop exits on
terminal frames (`command_result` → `done`), so any post-`done` event
is dropped. Putting the ID INTO `done` means the hub sees it before
loop exit and can perform the turn-state rekey atomically.

### Slave handler change

`Handler.SessionTurn(ctx, id, prompt, sink)` becomes
`Handler.SessionTurn(ctx, id, prompt, fresh, sink)`. When `fresh=true`:

1. Skip `trySessionWorker` entirely (it depends on existing rollout).
2. Call `res, err := h.Backend.Run(ctx, agentbackend.Task{Prompt: prompt,
   Origin: agentbackend.OriginUser}, sink)`. The `Origin: OriginUser` (a
   new field on `Task`, or an equivalent existing flag — verify the
   field name when implementing) tells the codex executor that this is a
   user-initiated session, NOT an agent_task subagent. Codex executor's
   loom-meta sidecar writer currently hard-codes `origin: agent_task`
   (see `pkg/agentbackend/codex/executor.go` sidecar branch); the
   `fresh+user` path must either set `origin: user` in the sidecar or
   suppress sidecar writing entirely so the new session shows up as a
   user row in `list_sessions`, not as a misclassified `agent task`.
3. Read the codex-minted thread ID from `res.SessionID` (the existing
   `executor.Result.SessionID` field already carries it — do NOT scan
   the `<codex_home>/sessions/current` marker, which is a CODEX_HOME-level
   last-active pointer that concurrent `Run`/`RunResume` calls would
   race-overwrite).
4. Compose the terminal `done` envelope so its `result` JSON object
   includes `session_id: res.SessionID`. The slave's existing done-emit
   path (whatever assembles the `command_result` envelope from `res`) is
   the single point of change — it learns to copy `res.SessionID` into
   the JSON when present. If `err == nil && res.SessionID == ""` on a
   fresh turn, treat as a bug — surface as `error` envelope with code
   `backend_unavailable` and message `"fresh turn returned without a
   session ID"`.
5. `Run`'s normal intermediate events (`status`, `chunk`) flow through
   unchanged. The `session_id` lands as a field inside the terminal
   `done` payload — single frame, no ordering issue, no new event type.

When `fresh=false`: existing flow unchanged.

### Frontend change

`postTurn(daemonID, sessionID, prompt, onEvent, opts?)` gains:
- `opts.fresh?: boolean` — sent as `{fresh: true}` in the JSON request
  body (NOT a query param; the HTTP handler reads ONLY from the body to
  avoid the two-channel mismatch that would silently drop fresh and
  re-trigger the original bug).
- The frontend reads the real backend ID off the existing terminal
  `done` event's payload — `data.result.session_id` (per §Protocol).
  No new event type, no new onEvent branch.

`sendPrompt` in `CommanderApp.tsx`:
- Before posting, check `pendingSessionRef.current?.phase === 'draft' &&
  pendingSessionRef.current.sessionID === submitted.sessionID`. If so, set
  `fresh: true` in the postTurn call.
- Inside the existing `event === 'done'` branch, read the new field
  `data.result.session_id` (when present). If set AND it differs from
  `submitted.sessionID` AND `selectedRef.current?.sessionID === submitted.sessionID`,
  rebind:
  - `const realID = data.result.session_id`
  - `selectedRef.current = { daemonID: submitted.daemonID, sessionID: realID }`
  - `setSelected({ daemonID: submitted.daemonID, sessionID: realID })`
  - `setPendingSession(prev => prev ? { ...prev, sessionID: realID, phase: 'submitting' } : null)`
  - `pendingSessionRef.current = { ...pendingSessionRef.current, sessionID: realID, phase: 'submitting' }` (if non-null)
  - Then `void loadTree()` — when the tree returns and the real row appears,
    pending clears as before.
- The selectedRef-match guard avoids race: if the user clicked another
  session between `+` and the `done` event, the rebind no-ops (the
  existing pending stays as-is; the just-committed session still
  surfaces in the next `loadTree`).

The existing pending → submitting flip stays; the ID rebind happens in the
same step. No separate event listener — read the new field off the
existing `done` event.

### HTTP layer

`commanderhub` HTTP handler at `/api/commander/daemons/<d>/sessions/<sid>/turn`:
- Add `fresh bool` to the JSON body schema.
- Pass through to `commander.SendCommandStream` payload (the
  `SessionTurnArgs` JSON gains the field automatically since the protocol
  type is updated).

### Hub turn-state rekey (atomic on terminal `done`)

The hub tracks per-turn state (`turnKey{owner, daemonID, sessionID}` →
turn-state, in-flight gate) keyed on the URL's `<sid>`. For a fresh
turn, the URL `<sid>` is the client-minted placeholder UUID; the real
codex-minted ID is in the terminal `done` payload's `session_id` field.

If the hub leaves state keyed under the placeholder ID, the next turn
on the real ID:
- Finds NO state at the real key → defaults to `idle` (BAD if the slave
  was actually `awaiting_approval` — composer re-enables and the user
  submits a turn codex can't accept).
- Or, finds stale `InFlight=true/queued` at the placeholder key that
  never clears → next real-ID turn gets HTTP 409.

The rekey must happen at the SAME MOMENT the terminal frame writes
state, so the final `done`/`awaiting_approval`/`error` lands at the
REAL key, not the placeholder. Concretely:

1. The hub's stream loop receives the `command_result` envelope from
   the daemon-link WS. Today this calls `updateTurnStateFromEnvelope`
   with the placeholder `key`.
2. Spec change: BEFORE calling `updateTurnStateFromEnvelope`, inspect
   the envelope payload. If it contains `session_id` (terminal-frame
   field added in §Protocol) AND it differs from `key.sessionID`, build
   a new `realKey = turnKey{owner, daemonID, session_id}` and use
   `realKey` for ALL subsequent state writes in this iteration
   (terminal write + invalidateDaemonSessions).
3. Also call a small `rekey(placeholderKey, realKey)` helper on the
   turn-state store to migrate any prior in-flight entry (queued,
   starting, answering state already written under the placeholder).
   Idempotent — if `realKey` already has state from another concurrent
   path, prefer the newer terminal write.
4. The HTTP response stream continues writing SSE frames to the client
   under the placeholder URL until the loop exits — the client doesn't
   need a separate event, it reads the new ID from the `done` payload
   and rebinds its own `selected` + `pendingSession`.

After this iteration: subsequent turns under the real ID find their
state cleanly at `realKey`, run normally through `Backend.RunResume`
(fresh=false, ID=real).

## Files

| File | Change |
|---|---|
| `internal/commander/protocol.go` | `SessionTurnArgs` adds `Fresh bool` field. |
| `internal/commander/handler.go` | `SessionTurn` accepts new `fresh` arg; routes to `Backend.Run` (with `OriginUser` task field — see "agent_task misclassification" risk) when true; reads `res.SessionID` (the existing `executor.Result.SessionID` field, NOT the `<codex_home>/sessions/current` marker); writes `result.session_id` into the terminal `done` envelope's payload before it is forwarded by the hub. |
| `pkg/agentbackend/backend.go` (or equivalent) | `Task` struct gets an `Origin` field (or equivalent flag) so backends can branch sidecar/origin behavior between user-fresh and agent-task. If a clean field doesn't exist today, add one. |
| `pkg/agentbackend/codex/executor.go` | sidecar writer respects `Task.Origin`: writes `origin: user` when the task is a user-fresh new session; preserves existing `origin: agent_task` for the agent-task path. |
| `internal/commander/wsclient.go` | unmarshal `Fresh` from incoming session_turn; pass through to Handler.SessionTurn. |
| `internal/commander/http.go` | the local daemon HTTP `/turn` handler also calls `h.SessionTurn(...)` directly — must be updated for the new signature, AND parse `fresh` from its own POST body so direct-daemon `/turn` calls also work end-to-end (otherwise the daemon's compile fails when only commanderhub is updated, and direct-daemon users hit the same original bug). |
| `internal/commanderhub/http.go` (`turn` handler) | parse `fresh` from POST body ONLY (not query param — single source of truth). |
| `internal/commanderhub/hub.go` (or wherever turn-state lives) | new `rekey(oldKey, newKey)` helper; invoked from the stream-loop terminal-frame branch when the `command_result` payload contains `result.session_id` and that value differs from `key.sessionID`. The terminal write (state + invalidateDaemonSessions) uses the rekeyed `realKey` for THIS iteration. |
| `internal/commanderhub/webapp/src/api/client.ts` | `postTurn` accepts `opts.fresh`; body JSON includes `fresh`. |
| `internal/commanderhub/webapp/src/CommanderApp.tsx` | `sendPrompt` sets `fresh: true` for draft pending; reads `data.result.session_id` off the existing `done` event; rebinds selected + pending to real ID via the same guard pattern as the existing pendingNow check. |
| `internal/commander/handler_test.go` | New case: fresh=true routes to Backend.Run with OriginUser, NOT RunResume; the terminal `done` envelope on the sink carries `result.session_id == res.SessionID`. |
| `pkg/agentbackend/codex/executor_test.go` (or backend_test.go) | New case: `OriginUser` task writes sidecar with `origin: user`, not `agent_task`. |
| `internal/commanderhub/hub_test.go` (or equivalent) | New case: turn-state rekey on terminal `done` frame whose payload carries `result.session_id` preserves terminal state under the new key. |
| Frontend unit tests | `postTurn` body shape for fresh=true; CommanderApp reads `result.session_id` from the `done` event and rebinds selected + pending. |

## Test Strategy

### Go unit
- `handler_test.go`: fresh=true with a fake backend whose `Run` returns a
  real `executor.Result.SessionID` — assert the terminal `done` envelope
  written to the sink has `result.session_id == <returned>` AND the
  handler does NOT call `trySessionWorker`.
- `handler_test.go`: fresh=false continues to use the existing trySessionWorker
  path (existing tests stay green).

### Frontend vitest
- `client.test.ts` (or extend existing): `postTurn(..., { fresh: true })`
  sends `fresh: true` in the JSON body.
- `CommanderApp.mobile.test.tsx`: `+` flow → first prompt → assert the
  POST body has `fresh: true`. Then mock the SSE stream to emit a single
  `done` event whose payload includes `result.session_id`; assert
  `pendingSession.sessionID` and `selected.sessionID` were both rebound
  to that backend ID.

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
  `+` and the `done` event, the rebind targets the OLD `selected`.
  Mitigate by reading `selectedRef.current` inside the `done` branch and
  only rebinding if it still matches the submitted (placeholder) ID.
- **Other backends (claude, opencode)**: if their `Run` paths are not
  symmetric to codex's, `fresh=true` on those backends might mis-route.
  Out of scope here — codex is the only shipped backend hitting this.
  Add a `t.Skip` placeholder for the other backends with a TODO comment.
