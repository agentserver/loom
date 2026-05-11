# Runtime test layout

This directory keeps one independent working directory per process:

- `driver/` for `driver-agent`
- `master/` for `master-agent`
- `slave/` for `slave-agent`

Before first use, replace `https://agent.example.com` in all three
`config.yaml` files with the same agentserver URL, then register each agent.

Run these commands inside WSL:

```bash
cd /mnt/c/Users/DELL/multi-agent/multi-agent

./bin/driver-agent register --config tests/runtime/driver/config.yaml

mkdir -p tests/runtime/slave/work

cd tests/runtime/master
../../../bin/master-agent config.yaml

cd ../slave
../../../bin/slave-agent config.yaml
```

The first `master-agent` and `slave-agent` runs print registration URLs and
write credentials back into their local `config.yaml` files. After registration,
keep master and slave running. The driver is normally started by Claude Code or
the VS Code Claude extension as a stdio MCP server.

If Claude/VS Code is running inside WSL, use the Linux paths:

```json
{
  "mcpServers": {
    "driver": {
      "command": "/mnt/c/Users/DELL/multi-agent/multi-agent/bin/driver-agent",
      "args": [
        "serve-mcp",
        "--config",
        "/mnt/c/Users/DELL/multi-agent/multi-agent/tests/runtime/driver/config.yaml"
      ]
    }
  }
}
```

If Claude/VS Code is running on the Windows side rather than inside WSL, start
the WSL binary through `wsl.exe`:

```json
{
  "mcpServers": {
    "driver": {
      "command": "wsl.exe",
      "args": [
        "bash",
        "-lc",
        "cd /mnt/c/Users/DELL/multi-agent/multi-agent && exec ./bin/driver-agent serve-mcp --config tests/runtime/driver/config.yaml"
      ]
    }
  }
}
```
