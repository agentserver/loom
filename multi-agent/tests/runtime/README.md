# Runtime test layout

This directory keeps one independent working directory per process:

- `driver/` for `driver-agent`
- `master/` for `master-agent`
- `slave/` for `slave-agent`

All three `config.yaml` files point at `https://agent.cs.ac.cn`. Register each
agent before first use.

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

## Online Docker E2E Reuse

For online tests against `https://agent.cs.ac.cn`, keep one stable host
directory per process and always remount the same directory into its container.
Each process writes its agentserver registration credentials back into its own
`config.yaml`; reusing that directory avoids repeating device-flow login.

Use separate host directories for each role. `driver`, `master`, and every
`slave` must start from its own directory; future test runs must keep using the
same role directory:

```text
/tmp/multi-agent-driver-first-e2e/driver/
/tmp/multi-agent-driver-first-e2e/master/
/tmp/multi-agent-driver-first-e2e/slave-a/
/tmp/multi-agent-driver-first-e2e/slave-b/
```

Use stable container names for long-lived online E2E containers:

```text
ma-e2e-driver
ma-e2e-master
ma-e2e-slave-a
ma-e2e-slave-b
```

Start later runs from the same role directory and same mounted root:

```bash
docker run --name ma-e2e-slave-a --network host \
  -v /tmp/multi-agent-driver-first-e2e:/e2e \
  -v /root/.zshrc:/root/.zshrc:ro \
  multi-agent-e2e-runtime:latest \
  zsh -lc 'cd /e2e/slave-a && /e2e/bin/slave-agent config.yaml'
```

Do the same for `slave-b`, `master`, and `driver`, changing only the container
name and `cd /e2e/<role>` directory. If a container is stopped, restart it with
the same mounted directory. Do not copy credentials between role directories.

When registration is required, the test runner must print the login URL from the
agent process logs to stdout/stderr without filtering it. Copy the printed login
URL immediately; device codes expire quickly. After login succeeds, keep the
role's generated `config.yaml` and container directory in place so later runs can
reuse the same authorization.

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
