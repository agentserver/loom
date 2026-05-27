# loom-py

Python client for the [loom](https://github.com/agentserver/loom) multi-agent fabric.

Wraps the `driver-agent` MCP surface as a fluent workflow API. Zero runtime Python
deps; one external dep: the `driver-agent` Go binary on PATH.

## Install (dev)

```bash
pip install -e multi-agent/python
```

## 5-minute quickstart

```python
import loom

with loom.workflow(goal="say hello") as wf:
    res = wf.chat("Reply with the word HELLO and stop.",
                  target="slave-local-prod").wait()
print(res.output)  # "HELLO"
```

See `multi-agent/python/tests/e2e/` for more examples.
