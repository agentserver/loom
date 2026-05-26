# Spike-0: backend resume with dangling tool_use

**Date:** 2026-05-26
**Goal:** Verify `claude --resume <S>` and `codex resume <T>` cleanly handle sessions whose last assistant turn called a tool (our `ask_user` / `request_permission`) but never received a `tool_result` — the exact shape humanloop will leave behind when it kills the backend to pause for human input.

## Method

A tiny MCP server (`/tmp/spike0-mcp.py`) registers one tool `hang` whose handler runs `time.sleep(3600)` and never returns. We then exercise two endings for the first turn:

- **Run A (timeout / SIGTERM):** `timeout 30 claude ...`. The CLI catches the signal and synthesises a `tool_result` with `"MCP error -32000: Connection closed"` before exiting cleanly. Session is *not* left dangling.
- **Run B (SIGKILL the whole process group while the tool call is in flight):** simulates humanloop killing the backend the instant our `ask_user` MCP returns control. This DOES leave the session ending on a bare `tool_use` (claude) / `function_call` (codex) with no corresponding `tool_result` / `function_call_output`.

Run B is the realistic case for humanloop's pause→resume cycle and is what the resume tests below operate on.

Logs:
- `/tmp/spike0-claude-run1.log` — Run A (timeout, produced synthetic tool_result; session NOT dangling)
- `/tmp/spike0-claude-run2.log` — Run B (SIGKILL, session left dangling on tool_use)
- `/tmp/spike0-claude-resume.log` — resume of the Run B session
- `/tmp/spike0-codex-run1.log` — codex Run B equivalent
- `/tmp/spike0-codex-resume.log` — resume of the codex session

## Claude Code

- Version: `2.1.150 (Claude Code)`
- Backend session jsonl: `~/.claude/projects/<dir>/<session_id>.jsonl`
- Result: **PASS** — `claude --resume <S>` accepts and continues a session that ends on a bare `tool_use`.

### Evidence — dangling session prior to resume

`/root/.claude/projects/-tmp-spike0-claude/2d4bf70f-2729-4566-970e-57dbe36b2fef.jsonl` (8 lines) ends with:

```
  7 assistant      role=assistant content=['tool_use(id=toolu_vrtx_01CSzd1Bp name=mcp__spike__hang)']
```

No `tool_result` for `toolu_vrtx_01CSzd1Bp` follows. Exactly the humanloop-pause shape.

### Evidence — resume turn

```bash
echo 'I am the user; my answer to your previous question is: 42. Continue.' \
  | timeout 60 claude --resume 2d4bf70f-... --print --output-format=stream-json --verbose --mcp-config /tmp/spike0-mcp.json
```

Exit code: **0**. No `tool_use without tool_result` error. The session jsonl after resume:

```
  7 assistant      tool_use(id=toolu_vrtx_01CSzd1Bp name=mcp__spike__hang)   <-- still dangling
 10 user           text "Continue from where you left off."                  <-- claude SYNTHESISED this
 11 assistant      text "No response requested."                             <-- replied to its own prompt
 12 user           text "I am the user; my answer to your previous ..."      <-- our actual stdin prompt
 13 assistant      thinking
 14 assistant      tool_use(id=toolu_vrtx_01D1rZY2d7tDzR name=mcp__spike__hang)
```

Two key observations:

1. **Claude injects a synthetic user message `"Continue from where you left off."`** on resume to satisfy the Anthropic API's "tool_use must be followed by tool_result OR by another user message" expectation. The bare `tool_use` at line 7 is NEVER given a `tool_result` in the jsonl — yet the API accepts the conversation. (Either the CLI re-shapes the request on the wire, or the API tolerates a bare tool_use when the next user turn is a text message; either way the public CLI handles it.)
2. **Our prompt was delivered AFTER the synthetic continuation** (lines 11→12). The model saw it and acted (it chose to retry the same tool, because the prompt asked for the answer to a *previous question* the spike never asked — semantically reasonable behaviour for our test prompt; for humanloop the resume prompt will be the actual answer text and the model will use it). The session id is **preserved across resume** (same UUID).

### Fallback (if FAIL)

Not needed for claude.

## Codex CLI

- Version: `codex-cli 0.133.0`
- Backend rollout jsonl: `~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-<TS>-<thread_id>.jsonl`
- Subcommand used: `codex exec resume <THREAD>` (non-interactive). Spec's `codex resume <THREAD> --json` also exists but is the TUI variant; for spike automation `codex exec resume --json <id>` is the right call.
- Flag deviation from spec: `--cd` is **not** accepted by `codex exec resume` (only by `codex exec`). I dropped it for the resume invocation; codex picked up the right session purely by id.
- Result: **PASS** — `codex exec resume <T>` accepts and continues a session that ends on a bare `function_call`.

### Evidence — dangling rollout prior to resume

`rollout-2026-05-26T11-10-29-019e6243-2a01-7661-af39-964535611170.jsonl` (9 lines) ends with:

```
  7 response_item  reasoning
  8 response_item  function_call    name=hang call_id=call_MFJL5mNYPhyYRxKqVkhaPaZd
```

No `function_call_output` for `call_MFJL5mNYPhyYRxKqVkhaPaZd` follows.

### Evidence — resume turn

```bash
echo 'I am the user; my answer to your previous question is: 42. Continue without calling any tools.' \
  | timeout 90 codex exec resume --json --dangerously-bypass-approvals-and-sandbox 019e6243-...
```

Output:

```
{"type":"thread.started","thread_id":"019e6243-2a01-7661-af39-964535611170"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"42 noted. Continuing without tools."}}
{"type":"turn.completed","usage":{"input_tokens":11182,"cached_input_tokens":3456,"output_tokens":11,"reasoning_output_tokens":0}}
```

Exit code: **0**. No error about the missing `function_call_output`. Rollout after resume (appended to the SAME file):

```
  8 response_item  function_call            name=hang call_id=call_MFJL5mNYPhyYRxKqVkhaPaZd  <-- still dangling
  9 event_msg      task_started
 10 turn_context
 11 response_item  message  role=user        "I am the user; my answer to your previous ..."
 12 event_msg      user_message
 13 event_msg      agent_message
 14 response_item  message  role=assistant   "42 noted. Continuing without tools."
 16 event_msg      task_complete
```

Codex:
- preserves the thread_id across resume,
- appends to the same rollout file,
- accepts the dangling `function_call` without inserting any synthetic compensator,
- delivers our user message directly to the model, which acknowledges and continues.

Codex's resume behaviour is in fact **cleaner** than claude's — no synthetic "Continue from where you left off." turn, our prompt is the next user message after the dangling call.

### Fallback (if FAIL)

Not needed for codex.

## Decision for humanloop server design

**No jsonl patching is required for either backend.** Both `claude --resume <S>` and `codex exec resume <T>` cleanly tolerate sessions whose final assistant turn is a bare `tool_use` / `function_call` with no matching result, and both deliver the resume-prompt user message to the model in the next turn. Humanloop's pause flow can therefore be the simple shape the spec assumes:

1. On `ask_user` / `request_permission` MCP call: write request to humanloop store, kill the backend (SIGKILL of the process group is safe — both CLIs leave a resumable session/rollout on disk).
2. On `chat_resume` skill: invoke `claude --resume <session_id>` or `codex exec resume <thread_id>` with the user's answer text on stdin; the backend will resume and the model will see the answer as the next user message.

Two implementation notes Tasks 8/10 should bake in:

- **Claude resume order-of-operations:** claude injects a synthetic `"Continue from where you left off."` user turn BEFORE our resume prompt. Backends/parsers must not assume the first new user turn after resume is ours — index by turn position or by matching content. (Codex does not do this, so backend wrappers will differ.)
- **Codex exec flag pruning:** `codex exec resume` rejects `--cd`. Pass cwd-affecting config via `-c` overrides or by `chdir`-ing the wrapper process; do not forward the full `codex exec` flag set to the resume subcommand.

If a future Anthropic / OpenAI API tightening starts rejecting these resumed conversations on the wire, the fallback would be: before `--resume`, append a synthetic `tool_result` line into the jsonl for each dangling `tool_use` id observed in the tail. For claude that's a `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"<id>","is_error":true,"content":"humanloop: pause for user input"}]},...}` line at the bottom of `~/.claude/projects/<dir>/<session>.jsonl`. For codex, a `{"type":"response_item","payload":{"type":"function_call_output","call_id":"<id>","output":"humanloop: paused"}}` line at the bottom of the rollout. We do not need to implement this today.
