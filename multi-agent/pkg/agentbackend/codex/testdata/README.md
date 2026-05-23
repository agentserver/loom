# codex testdata

Captured from:

```bash
codex exec --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check \
  'reply with the single word: pong'
```

on 2026-05-23, codex version `codex-cli 0.130.0`.

## Event schema observed

The stream is NDJSON (one JSON object per line). Each line has a top-level
`"type"` field that discriminates the event kind.

**Event of interest:** lines with `type == "item.completed"` **and**
`item.type == "agent_message"` carry the assistant text at `.item.text`.

Example line from this fixture (line 5 of 6):

```json
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"pong"}}
```

So the executor's NDJSON parser should:

1. Parse each line as JSON.
2. Filter for `type == "item.completed"`.
3. Within that, check `item.type == "agent_message"`.
4. Extract `.item.text` as the assistant reply.

Other event types present in this fixture:

| `type`            | Notes                                                     |
|-------------------|-----------------------------------------------------------|
| `thread.started`  | Session metadata, carries `thread_id`                    |
| `turn.started`    | Marks start of a model turn                              |
| `item.started`    | Intermediate item (e.g. `command_execution`) in progress |
| `item.completed`  | Item finished; `item.type` is `command_execution` or `agent_message` |
| `turn.completed`  | End of turn; carries `usage` (token counts)              |

## Re-capturing

If a future codex version changes the schema, re-run the capture command above
(check `codex --version` first), replace this file and `codex_exec.ndjson`,
and update the executor parser accordingly.
