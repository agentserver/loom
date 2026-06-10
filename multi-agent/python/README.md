# loom-py

Python client for the [loom](https://github.com/agentserver/loom) multi-agent
fabric. Wraps the `driver-agent` MCP surface as a fluent workflow API. Zero
runtime Python deps; one external dep: the `driver-agent` Go binary.

## Install (dev)

```bash
pip install -e multi-agent/python
```

Then make sure `driver-agent` is reachable via one of:

- `$LOOM_DRIVER_BIN=/abs/path/to/driver-agent`
- `driver-agent` on `$PATH`
- repo-local `multi-agent/tests/prod_test/bin/driver-agent.linux-amd64`

## Quickstart

### 1. Happy chat

```python
import loom

with loom.workflow(goal="say HELLO") as wf:
    res = wf.chat("Reply with HELLO and stop.",
                  target="slave-local-prod").wait()
print(res.output)
```

### 2. Human in the loop (ask_user)

```python
import loom

with loom.workflow(goal="pick a color") as wf:
    res = (
        wf.chat('Call ask_user(question="pick a color", options=["red","blue"]) '
                "then reply with that color.",
                target="slave-local-prod")
          .expect_or_ask()        # default handler reads stdin from terminal
          .wait()
    )
print(res.output)
```

For non-interactive contexts pass a custom handler:

```python
def handler(q: loom.Question) -> str:
    if q.kind == "request_permission":
        return "approve" if policy_check(q) else "deny"
    return ask_my_chat_ui(q.question, q.options)

res = wf.chat(...).expect_or_ask(handler).wait()
```

### 3. Find a capable slave

```python
import loom

with loom.workflow(goal="weather lookup") as wf:
    try:
        slave = wf.find_slave(mcp_tool="weather_forecast")
    except loom.SlaveNotFound:
        slave = wf.find_slave(skill="register_mcp")  # bootstrap
        # ... scaffold + register the MCP, then retry
    res = wf.chat("Will it rain in Beijing tomorrow?", target=slave).wait()
```

`list_slaves()` and `find_slave()` only return agents whose discovery role is
`slave` when the driver provides role metadata; peer drivers and masters are
excluded from these Python helpers.

### 4. File I/O via placeholders

```python
import loom

with loom.workflow(goal="CSV → summary") as wf:
    res = wf.chat(
        "Read {input:data} and write a summary to {output:report}.",
        target="slave-local-prod",
        inputs={"data": "./data.csv"},
        outputs={"report": "./report.md"},
    ).wait()
print(res.outputs["report"])  # './report.md' (already populated locally)
```

## Status

v0 covers:

- core task semantics (submit / wait / get / cancel)
- humanloop pause/resume (`expect_or_ask`)
- capability discovery + dynamic MCP (`list_slaves` / `find_slave` / `MCPSpec`)
- file I/O via `inputs` / `outputs` placeholders
- workflow context manager with fluent verbs

Not yet (planned for v0.2):

- DAG / fanout
- Codex backend differences
- TASK_CONTRACT envelope compilation (see spec § 6 for trigger criteria)
- retry / metrics / PyPI publish

See `docs/superpowers/specs/2026-05-27-loom-python-library-design.md`.
