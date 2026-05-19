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

For Bash and Claude Code permission tests, each slave config must opt in:

```yaml
discovery:
  skills:
    - chat
    - mcp
    - register_mcp
    - bash
    - claude_permissions
```

The driver exposes:

- `run_slave_bash`
- `get_slave_claude_permissions`
- `update_slave_claude_permissions`

Permission tools currently use task delegation with `skill="claude_permissions"` because agentserver does not expose the needed custom-agent peer/control proxy. The permission task is handled by native `slave-agent` Go code, not by asking slave Claude Code to edit its own permissions. Future work should move this to a dedicated agentserver control channel.

Example permission patch:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "allow_presets": ["python", "curl", "file_write"]
}
```

Example Bash execution:

```json
{
  "target_display_name": "slave-a-online-dag-160628",
  "script": "python3 - <<'PY'\nprint('ok')\nPY",
  "timeout_sec": 60
}
```

Manual online E2E prompt for Claude Code driver:

```text
Use driver tools to:
1. list agents and choose two slaves with bash and claude_permissions capability.
2. update each slave's Claude Code permissions with python, curl, and file_write presets through the task-channel permission tool.
3. run a Python matrix multiplication script on slave A using numpy through run_slave_bash.
4. run an equivalent Python matrix multiplication script on slave B using for loops through run_slave_bash.
5. compare the result hashes and report whether they match.
```

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

## Claude Code Runtime Permissions

Online E2E slaves execute user-created scripts through Claude Code, so each
slave's stable workdir must preserve Claude Code project permissions. Keep this
file in every mounted slave directory, for example
`/tmp/multi-agent-driver-first-e2e/slave-a/.claude/settings.local.json` and
`/tmp/multi-agent-driver-first-e2e/slave-b/.claude/settings.local.json`:

```json
{
  "permissions": {
    "allow": [
      "Read",
      "Write",
      "Edit",
      "Bash(python *)",
      "Bash(python3 *)",
      "Bash(curl *)",
      "Bash(chmod *)",
      "Bash(ls *)",
      "Bash(cat *)",
      "Bash(pwd)",
      "Bash(mkdir *)"
    ],
    "deny": []
  }
}
```

The runtime image also installs `python3-numpy` and includes the same user-level
Claude Code allowlist for newly created containers. The per-workdir settings are
still useful because those directories are intentionally reused across test runs
to preserve login and project authorization state.

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
