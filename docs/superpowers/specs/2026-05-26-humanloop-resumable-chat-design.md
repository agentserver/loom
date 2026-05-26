# Mid-chat human-in-the-loop (resumable chat via session continuation)

**Date:** 2026-05-26
**Status:** draft, awaiting user review
**Scope:** slave (chat backend + dispatch + new skill `chat_resume` + new internal MCP server `humanloop`) + driver (`submit_task` / `wait_task` / `get_task` / new `resume_task`) + `skills/multiagent` doc + e2e on `multi-agent/tests/prod_test`

## Goal

When a slave's chat backend (Claude Code or Codex CLI) is mid-conversation and needs a user judgement call or wants to elevate permissions, give the model a way to pause, ask the human (sitting at the driver), receive an answer, and resume the same conversation. Today every `submit_task(skill="chat")` is a single-shot RPC: once the slave's backend exits, the conversation is gone, and the only signal the driver has is a final `output` string.

Concretely, four categories of mid-chat callbacks must work end-to-end:

1. Permission / sensitive operation elevation (chat wants to run something outside the current `permissions.allow`; needs the user to approve).
2. Intermediate-result judgement (chat reasoned partway, isn't sure between candidates, wants the user to pick / clarify before continuing).
3. Missing input / disambiguation (chat needs a specific file, parameter, or credential that wasn't supplied).
4. Long-running cost checkpoints (chat realises it'll burn unexpected tokens / time / external-API calls and wants explicit consent to continue).

## Non-goals

- **Changing `agentserver` upstream.** The task-status enum (`pending / assigned / running / completed / failed / cancelled`) stays as-is. We do **not** introduce a server-side `awaiting_user` status. (See "Why" below — the protocol-level workaround is the whole point of this design.)
- **`request_permission` actually changes permissions.** In this MVP it is an *advisory* signal — the slave backend does not gain new abilities just because the user answered "approve". Real elevation still goes through the existing `update_slave_claude_permissions` / `register_slave_mcp` driver tools, invoked separately by the user.
- **Mid-chat callbacks from non-chat skills** (e.g. `bash`, `file`, `register_mcp`). Those executors stay single-shot. The model decides what to run *inside* the chat skill, so funnelling the human-loop through chat covers the four categories above already.
- **Pre-declared contract checkpoints** (driver telling the slave "always pause at step N"). The model decides when to ask; we are not adding a contract-side `checkpoint` primitive.
- **Cross-slave session migration.** A chat thread is bound to one slave for life — the backend `session_id` is slave-local.
- **Backwards compatibility with old slaves/drivers.** Both sides ship together; there is no `humanloop` discovery flag and no fallback path. Mixed old/new fleets are out of scope.

## Why — the protocol problem

We picked the user-facing surface "wait_task returns mid-state, driver calls `resume_task` to continue" (call it surface A). We then audited whether `agentserver@v0.48.1` and our backend CLIs can support it natively:

- `pkg/agentsdk/task.go` hard-codes the status set; the only status-write API is `Complete / Fail / Running`. There is no way to ship a custom `awaiting_user` task status.
- `TaskInfo.Result` is free-form JSON, however, and `TaskInfo.SessionID` / `DelegateTaskResponse.SessionID` already exist as read-only labels.
- `DelegateTaskRequest` does **not** accept a `session_id`, so the agentserver cannot itself resume a task to its old session. Resume has to be a *new* task.
- Our current `pkg/agentbackend/claude/executor.go` and `pkg/agentbackend/codex/executor.go` invoke `claude --print` / `codex exec --json` one-shot, with no session capture and no `--resume` plumbing.

So surface A is implementable only by **emulating "non-terminal" inside the driver MCP layer**: every agentserver task always ends `completed`, but the driver inspects `result.kind` and re-presents it to the caller as `status:"awaiting_user"`. The slave backend records a `session_id` on the first turn and uses `claude --resume <S>` / `codex resume <S>` to continue when the driver issues a continuation task. This is the whole shape of the design.

## Design

### 1. Protocol shape

A chat conversation that may pause/resume becomes a chain of agentserver tasks bound together by a backend `session_id`:

```
caller (user's Claude Code)
   │ submit_task(prompt, skill="chat", target=...)
   ▼
driver MCP ──delegate──▶ slave (chat backend, captures session_id S)
                              │ model calls ask_user(question, options?)
                              ▼
                          humanloop MCP server captures payload + IPCs to executor
                              │
                          executor closes stdin → backend exits cleanly
                              │ task.Complete(result={
                              │     "kind": "awaiting_user",
                              │     "question": {...},
                              │     "session_id": S
                              │ })
                              ▼
driver wait_task / get_task inspects result.kind:
   { "status":"awaiting_user", "is_final":false,
     "session_id": S, "current_task_id": T0,
     "question": {...} }

caller →
   resume_task(last_task_id=T0, answer="...")
       │
       ▼
driver  GetTask(T0) → fetch S, target_id; delegate NEW task:
        skill="chat_resume", prompt={session_id:S, answer, kind}
       ▼
slave   chat_resume executor → claude --resume S (stdin: "User answered: ...")
       │ (may loop: model asks again, executor exits, …)
       ▼
final  task.Complete(result={"kind":"final", "summary":"..."})
       ▼
driver resume_task returns status:"completed" + final output
```

**The driver carries zero chat-thread state.** Every "what is the current state of this conversation?" query is answered by `agentsdk.GetTask(last_task_id)`: it returns `SessionID`, `TargetID`, and `Result` (the JSON marker). The `session_id` is the natural thread identifier; the `last_task_id` is the natural cursor into that thread.

### 2. The `humanloop` MCP server (slave-side, Go, in-process binary subcommand)

A new internal stdio MCP server, implemented in Go and invoked as a subcommand of the `slave-agent` binary so deploys keep their single-binary shape.

**Invocation.** Each time the chat executor spawns a backend, it first:

1. `mktemp`s a unix socket path under `$LOOM_HOME/<agent>/humanloop/`.
2. Spawns `slave-agent humanloop-mcp <socket-path>` as a child process.
3. Injects the child into the backend's MCP config (`claude --mcp-config …` / `[mcp_servers.loom_humanloop]` in codex TOML, written to a per-task scratch file).

Lifetime: child dies when the backend exits or the task ends. No persistent process.

**Two tools, distinct semantics.**

```jsonc
ask_user({
  question:  "string",     // required; the question to surface to the user
  options?:  ["A: …", …],  // optional; turns the question into multiple choice
  context?:  "string"      // optional; extra context for the user
})

request_permission({
  intent:  "run_bash" | "write_path" | "install_mcp" | "other",  // required
  target:  "string",       // required: command / path / MCP id / …
  reason?: "string"        // optional
})
```

The split exists for caller-side dispatch: future driver work can map `request_permission` answers like `"approve"` directly to `update_slave_claude_permissions`, while `ask_user` is always a free-form answer. In this MVP the two have identical executor behaviour.

**Tool-call behaviour.** When the model invokes either tool:

1. The MCP server forwards the payload to the executor over the unix socket.
2. The MCP server immediately returns a fixed tool_result to the backend:

   ```jsonc
   { "status": "submitted",
     "note": "Your question was dispatched to the user. The backend will now pause; the user's answer will arrive as your next user turn after resume." }
   ```
3. The executor records `AwaitingUser=<payload>`, then closes the backend's stdin (graceful shutdown). The backend reads the tool_result, naturally ends the turn, and exits.

The fixed string in the tool_result is load-bearing: it tells the model "do not continue acting on guesses; treat my next input as the user's answer." Without it the model is free to take more actions before the executor can intervene.

**Graceful-shutdown timeouts.** After IPC payload received, executor closes stdin and waits `humanloop.shutdown_grace_sec` (default 10s). On expiry: SIGTERM, then 5s, then SIGKILL.

**Question quota per task.** The MCP server tracks `questions_emitted` per task. Beyond `humanloop.max_questions_per_task` (default 5) the next `ask_user` / `request_permission` call gets a *different* tool_result instead of pausing:

```jsonc
{ "status": "refused",
  "reason": "max questions reached for this task; decide yourself and explain in summary" }
```

This is the only abuse defence in MVP (see "Boundaries" §B for the rationale).

### 3. Slave changes

**3.1 `pkg/agentbackend/{claude,codex}/executor.go` — chat path.**

Today: spawn `claude --print --output-format=stream-json --verbose`, feed prompt on stdin, scan stream-json for assistant text, return `Result{Summary, CapabilityChange}`.

Changes:

- Before spawn: start `humanloop` MCP child + socket, inject into `--mcp-config`.
- While scanning stream-json: parse the first `type:"system"` frame to capture `session_id` (codex: `thread.started` event → `thread_id`). Store on `Result.SessionID`.
- A goroutine reads from the humanloop unix socket. On payload: stash into `Result.AwaitingUser`, close stdin, wait for backend to exit (with the shutdown-grace timeout above).
- After backend exits: if `AwaitingUser != nil`, return `Result{SessionID, AwaitingUser, Summary:<any text accumulated>}`. Otherwise normal path.

**3.2 New executor: `pkg/agentbackend/{claude,codex}/executor.go` — `RunResume`.**

Spawns the backend with `--resume <session_id>` (claude) / `resume <thread_id>` (codex). Stdin is `"User answered: <answer>\n"` (the `User answered: ` prefix gives the model an unambiguous signal that this is the answer to its previous `ask_user` / `request_permission` call). Same humanloop injection — the resumed chat can ask *again*.

Returns the same `Result` shape (may again carry `AwaitingUser`, or finalise).

**3.3 `internal/executor/executor.go` — type extension.**

```go
type Result struct {
    Summary          string
    CapabilityChange string
    SessionID        string          // NEW; from backend's first stream-json frame
    AwaitingUser     *AskUserPayload // NEW; non-nil ⇒ chat paused
}

type AskUserPayload struct {
    Kind     string   `json:"kind"`              // "ask_user" | "request_permission"
    Question string   `json:"question,omitempty"`
    Options  []string `json:"options,omitempty"`
    Context  string   `json:"context,omitempty"`
    Intent   string   `json:"intent,omitempty"`
    Target   string   `json:"target,omitempty"`
    Reason   string   `json:"reason,omitempty"`
}
```

**3.4 `internal/dispatch/dispatch.go` — result serialisation (chat / chat_resume only).**

The marker wrapping is scoped to the two chat skills only — bash, file, mcp, register_mcp, etc. keep their existing result shape, since the driver's `result.kind` interpretation (§4.2) is the consumer and only triggers on those two skills.

For `skill == "chat" || skill == "chat_resume"`:

- If `Result.AwaitingUser != nil`, the dispatcher writes:

  ```jsonc
  { "kind": "awaiting_user",
    "session_id": "<S>",
    "question": { "kind": "ask_user|request_permission", ... } }
  ```

- Otherwise:

  ```jsonc
  { "kind": "final", "summary": "<text>", "session_id": "<S>" }
  ```

Both flow through `task.Complete(...)` and become `TaskInfo.Result` for the driver to introspect. For every other skill the dispatcher emits the existing summary verbatim (no `kind` wrapping); driver §4.2 treats absent-or-non-recognised `kind` as the legacy `completed` path.

**3.5 New skill `chat_resume` (slave dispatch route).**

`cmd/slave-agent/main.go` always registers (no discovery flag):

```go
routes["chat_resume"] = chatResumeExecutor{backend}
```

`chat_resume` is in the `jsonPromptSkill` allowlist (its prompt is JSON, no manifest preamble):

```jsonc
{ "session_id": "<S>", "answer": "<user text>", "kind": "ask_user|request_permission" }
```

The executor calls `backend.RunResume(ctx, sessionID, answer, sink)`. Same dispatcher serialisation rules as chat.

**3.6 Session-level lock (slave-side, file flock).**

`chat_resume` executor takes an exclusive `flock` on `$LOOM_HOME/<agent>/humanloop/<session>.lock` before invoking the backend. If contention: `task.Fail("session busy")` and return immediately. This prevents two simultaneous resumes from racing on the same backend session jsonl. (Per §5.3 — this is a single-machine race, do not introduce a distributed lock.)

**3.7 Discovery — no new flag.**

All slaves register `chat_resume` and ship the `humanloop` MCP server. There is no per-slave opt-in.

### 4. Driver changes

All driver changes are stateless — no new tables, no in-memory thread map.

**4.1 `internal/driver/tools.go` — `submit_task`.**

Today the success return is `{task_id, target_id, target_display_name, manifest}`. Add one field unconditionally:

```jsonc
{ "task_id": "...", "session_id": "<from DelegateTaskResponse>", "target_id": "...", ... }
```

For non-chat skills the session_id is whatever agentserver returns (often empty); the caller can ignore it.

**4.2 `wait_task` / `get_task` — `result.kind` interpretation.**

After fetching `TaskInfo`, attempt `json.Unmarshal(info.Result, &{Kind})`. Two branches:

```jsonc
// case 1: kind == "awaiting_user"  (chat / chat_resume tasks only, see §3.4)
{ "status":          "awaiting_user",
  "is_final":        false,
  "session_id":      "<TaskInfo.SessionID>",
  "current_task_id": "<TaskInfo.TaskID>",
  "target_id":       "<TaskInfo.TargetID>",
  "question":        { "kind": "...", "question": "...", "options": [...], ... } }

// case 2: kind == "final"   (chat / chat_resume tasks; unwrap summary)
//   → existing behaviour: status:"completed", output=<wrapper.summary>, …

// case 3: result is not a JSON object with a recognised "kind"
//         (non-chat skills like bash/file/mcp/register_mcp/...)
//   → existing behaviour: status:"completed", output=<TaskInfo.Output or raw Result>, …
```

`wait_task` historically blocked until `completed | failed | cancelled`. With this change `awaiting_user` is also a *terminating poll* — the agentserver task **is** done at `completed`; we just expose a different surface status. The implementation: as soon as the poll sees `info.Status=="completed"` and `result.kind=="awaiting_user"`, return immediately with the `awaiting_user` shape.

**4.3 New tool: `resume_task`.**

```jsonc
// input schema
{ "type": "object",
  "properties": {
    "last_task_id": { "type": "string" },   // required
    "answer":       { "type": "string" },   // required
    "timeout_sec":  { "type": "integer" }
  },
  "required": ["last_task_id", "answer"] }
```

Behaviour (stateless):

1. `agentsdk.GetTask(last_task_id, includeOutput=true)`.
2. Validate: `info.Status == "completed"`, `result.kind == "awaiting_user"`. On mismatch: `MCPToolError{"not awaiting_user; current status=<...>, kind=<...>"}`.
3. Extract `session_id`, `target_id`, `question.kind` from `info.Result`.
4. `agentsdk.DelegateTask(target_id, skill="chat_resume", prompt=JSON{session_id, answer, kind})`.
5. Block-wait the new task (same polling loop `wait_task` uses) with timeout `args.timeout_sec` (default `cfg.DriverDefaults.TaskTimeoutSec`, fallback 600 s). Return the same response shape as `wait_task` (may be `completed` *or* another `awaiting_user` *or* `failed` / `cancelled`).

Output shape identical to `wait_task` — a caller can loop on `resume_task` without branching on tool name.

**4.4 `cancel_task` — unchanged.**

`awaiting_user` is not a live thing to cancel: the slave backend already exited. Not calling `resume_task` is itself "cancellation." Keep the v1 stub.

**4.5 Driver MCP tool surface.**

`internal/driver/tools.go` `Tools.All()` appends `&resumeTaskTool{t}` after `&waitTaskTool{t}`.

### 5. Observer — no changes

`session_id` is the natural thread key. The observer dashboard groups same-session tasks by `session_id` (already available on every task event). No new event types, no new columns. `observerstore` is untouched.

### 6. `skills/multiagent` updates

Three reference files plus the top-level `SKILL.md` get edits.

**`SKILL.md`** — new section "Mid-chat human-in-the-loop":

> Chat tasks (`skill="chat"`) can pause mid-conversation. `wait_task` may return `status:"awaiting_user"` instead of `completed`. When that happens:
> - The `question` field carries `kind` (`ask_user` | `request_permission`), `question` text, optional `options`, optional `context`.
> - **Default behaviour: surface the question to the human via `AskUserQuestion`. Do not invent an answer.** Only auto-answer when the question text explicitly says e.g. *"do not ask user, decide based on X"*.
> - Call `resume_task(last_task_id=<current_task_id>, answer=<user's reply>)` to continue. `resume_task` returns the same shape as `wait_task` — keep looping until you see `status:"completed"`.

Plus a doctrine line:

> `request_permission` is **advisory**, not enforcement. Answering "approve" does **not** grant the slave new abilities; if the user wants to elevate permissions, call `update_slave_claude_permissions` / `register_slave_mcp` separately.

**`references/driver-tools.md`** — add `resume_task` section; extend `wait_task` / `get_task` schemas with the `awaiting_user` branch.

**`references/slave-skills.md`** — add `chat_resume` entry; document that **chat** may emit `kind:"awaiting_user"` markers and that `humanloop` is always available.

**`references/task-contract.md`** — note that `chat_resume` is a JSON-prompt skill and lives in the `jsonPromptSkill` allowlist.

### 7. System prompt — ask_user abuse guard

`agentbackend.CapabilityEpilogue` (the appended system text both backends inject) gets a new paragraph:

> You have two tools, `ask_user` and `request_permission`, that pause the chat to ask the human. Only call them when **(a)** you are genuinely uncertain how to proceed, **(b)** guessing wrong has a non-trivial cost (loss of work, irreversible side-effects, or wasted spend), and **(c)** the human can answer in one or two sentences. Otherwise: decide yourself and explain the assumption in your final summary. You have a small budget of questions per task; spend them carefully.

This is the soft guard. The hard guard is `humanloop.max_questions_per_task` in §2.

## Boundaries

**A. Session lifecycle / failure modes.**

| Failure | Behaviour |
|---|---|
| Backend never emits a session_id (first stream-json frame missing) | Executor returns the task as `failed` even if `AwaitingUser` is set. We need session_id to resume, so a missing one is fatal for the thread. |
| Backend exits during graceful shutdown grace window — clean | Normal path. |
| Backend exits beyond grace window, SIGTERM kills it | Task is marked `failed` (the model never got to read its own ask_user tool_result, so the conversation state is unreliable for resume). |
| `resume_task` when slave has gone offline | Driver returns the agentsdk DelegateTask error. Caller may retry with the same `last_task_id`. |
| `resume_task` when slave still up but `claude --resume <S>` reports "session not found" | `chat_resume` executor `task.Fail("session not found / claude resume error: ...")`, driver surfaces failure. |
| `resume_task` called twice in parallel with the same `last_task_id` | Both successfully start `chat_resume` tasks; the second one hits the per-session flock in §3.6 and `task.Fail("session busy")`. |

**B. ask_user abuse — soft + hard guards.**

Soft: system-prompt language in §7. Hard: per-task question quota in §2 (default 5, refused tool_result on overflow). MVP intentionally has no per-user-per-day rate limit; we'll add one only if we see abuse in practice.

**C. `request_permission` is advisory.**

The slave backend does not gain abilities when the user answers "approve". The answer is forwarded as plain text. Tightening this — auto-invoking `update_slave_claude_permissions` based on the answer — is future work.

**D. Answer text is not sanitised.**

Driver passes the user's answer to the slave as-is. The slave backend's system prompt is responsible for treating it as untrusted user content. (Standard Claude / Codex hygiene; no change from today.)

**E. Cross-slave drift.**

`resume_task` always targets `GetTask(last_task_id).TargetID`. Caller cannot redirect. The session is slave-local.

**F. Concurrency at scale.**

Different chat threads (different `session_id`) on the same slave are independent — the backend CLI already isolates them. Same-thread concurrent resume is prevented by §3.6 flock.

## E2E test matrix

**Environment baseline: `multi-agent/tests/prod_test`.** No in-process httptest, no isolated unit harness for these cases — these all run end-to-end through:

- `driver-prod` (Claude Code + driver MCP, on this host)
- `slave-local-prod` (amd64 docker on this host)
- `slave-jetson-prod` (arm64 host-native systemd on the Jetson)

…all hitting the shared production observer at `39.104.86.73` and the agentserver at `agent.cs.ac.cn`. Per `e2e_required_for_features_and_fixes` memory: in-process httptest does **not** count; if prod_test cannot be used for some case, fall back to "scheme B" (local greylight on this host) and call it out explicitly.

| # | Case | Where it runs |
|---|---|---|
| 1 | Happy chat (no `ask_user` called) — verifies humanloop MCP injection has zero impact on baseline chat | `driver-prod` → `slave-local-prod` |
| 2 | Single `ask_user` → `resume_task` → final | `driver-prod` → `slave-local-prod` |
| 3 | Multi-round `ask_user` (≥2 rounds before final) | `driver-prod` → `slave-local-prod` |
| 4 | `request_permission` path — verify `kind` distinguishes from `ask_user`; driver surfaces `intent` / `target` / `reason` cleanly | `driver-prod` → `slave-local-prod` |
| 5 | Cross-node round trip — chat with `ask_user` on the Jetson, verify arm64 build + WAN path works | `driver-prod` → `slave-jetson-prod` |
| 6 | Slave offline mid-thread — kill `slave-local-prod` container after `awaiting_user`, call `resume_task`, expect clean failure; restart container, retry, expect success | `driver-prod` → `slave-local-prod` |
| 7 | Session not found — manually delete the backend session jsonl between pause and resume, expect `chat_resume` failure with clear `failure_reason` | `driver-prod` → `slave-local-prod` |
| 8 | Per-session flock — fire two concurrent `resume_task` calls against the same `last_task_id`; second one fails `session busy` | `driver-prod` → `slave-local-prod` |
| 9 | Question quota — system prompt + a chat that tries to call `ask_user` 6 times; 6th call gets `refused` tool_result and continues without pausing | `driver-prod` → `slave-local-prod` |
| 10 | Graceful-shutdown timeout — set `shutdown_grace_sec=1`, monkey-patch the backend stub to ignore stdin close, verify SIGTERM→SIGKILL path and task ends `failed` cleanly | `slave-local-prod`, may use stub backend script swapped in for the test |

Cases 1–9 exercise the real Claude/Codex backends. Case 10 is the only one allowed to use a synthetic backend stub (real backends won't reliably ignore stdin-close for the test).

## Implementation order

1. **Spike-0 (TBD-prototype)** — manually run `claude --resume <S>` after a session where the last assistant turn ended on a tool_use for `ask_user` whose tool_result was never received. Verify either (a) the resumed turn accepts a fresh user message cleanly, or (b) we need to retroactively patch a tool_result into the session jsonl before resume. Repeat for `codex resume <S>`. Outcome decides whether the humanloop MCP server needs a "post-mortem patch jsonl" step. **This is the only TBD in the spec; the rest of the design works either way.**
2. `humanloop` MCP server — Go subcommand of `slave-agent`, unix-socket IPC, two tools, quota counter.
3. `pkg/agentbackend/{claude,codex}/executor.go` — session_id capture, AwaitingUser path, humanloop MCP injection, graceful shutdown plumbing.
4. `RunResume` on both backends.
5. `internal/executor/executor.go` — `Result` extension, dispatch marker serialisation.
6. `chat_resume` slave skill + per-session flock.
7. `internal/driver/tools.go` — `submit_task` `session_id` field, `wait_task` / `get_task` `awaiting_user` branch, new `resume_task` tool.
8. `skills/multiagent` doc updates.
9. E2E cases 1–10 on prod_test.
10. Roadmap entry + CHANGELOG.

## Configuration knobs

To slave `config.yaml`:

```yaml
humanloop:
  shutdown_grace_sec: 10          # backend stdin-close → SIGTERM grace
  max_questions_per_task: 5       # hard quota
```

No driver-side knobs needed (stateless).

## Open questions

- Codex's `thread_id` vs Claude's `session_id` naming — internal protocol uses `session_id` uniformly, surface name in MCP responses is `session_id`. Acceptable to translate at the backend boundary.
- Whether `resume_task` should accept `prompt` *in addition to* `answer` (e.g. user wants to inject context, not just answer the question). Deferred — answer-only is sufficient for the MVP and "extra context" can be folded into the answer text. Revisit after a few real chats expose pain.
