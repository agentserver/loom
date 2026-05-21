# deploy

Production bring-up templates for the agents in `cmd/`. Unlike `examples/`
(which are end-to-end Go demos), each subdirectory here ships an installer
script and config templates you point at a real host.

## Quick start — one-liners

Replace the placeholders (`OBSERVER_HOST`, `WS_ID`, `YOUR_API_KEY`, agent
names) with your own values. Both commands are run **inside the directory
you want to use as the agent's project / install dir**.

### Driver (Termux on Android, aarch64)

EN — drop a Claude Code driver project into the current directory (binary,
`config.yaml`, `.mcp.json`, skills bundle); next `claude` run auto-loads
the driver MCP and skills:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
pkg install -y bash curl && bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver-android.sh) \
  --name driver-myandroid
```

中文 — 在当前目录铺一个 Claude Code driver 工程（二进制 + `config.yaml` +
`.mcp.json` + skills 包）；下次 `claude` 启动时 MCP 与 skills 自动加载：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
pkg install -y bash curl && bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-driver-android.sh) \
  --name driver-myandroid
```

启动前别忘了：`./driver-agent register --config ./config.yaml` 走一次
device-code，再 `claude login`（或 `export ANTHROPIC_API_KEY=...`）。

### Slave (Termux on Android, aarch64)

EN — install slave-agent into `~/.loom/<NAME>/` (binary + `config.yaml`,
auto-detected resources). No clone needed; same env-var pattern as driver:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
pkg install -y bash curl && bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave-android.sh) \
  --name slave-myandroid
```

中文 — 在 Android Termux 里把 slave-agent 装到 `~/.loom/<NAME>/`（二进制 +
`config.yaml`，CPU/内存自动探测）。不用 clone 仓库，env 变量约定跟
driver 一样：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
pkg install -y bash curl && bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/download/v0.0.1/bootstrap-slave-android.sh) \
  --name slave-myandroid
```

启动方式（Android 没 systemd，只能前台或 `nohup`）：

```bash
# claude login 过的话直接：
~/.loom/slave-myandroid/slave-agent ~/.loom/slave-myandroid/config.yaml
# 否则先 export ANTHROPIC_API_KEY=...
```

首启动会在 stderr 打印 device-code 验证 URL —— 浏览器批准一次即可，
凭证会自动回写 `config.yaml`。

### Slave (generic Linux with systemd, amd64 / arm64)

非 Android 的 Linux 主机用这条（需要 clone 本仓库到目标机），支持 systemd
托管；`--anthropic-key` 可选，已 `claude login` 时不需要：

```bash
cd deploy/linux/slave   # on a checkout of this repo on the target host
./install.sh \
  --name slave-myhost \
  --observer-url http://OBSERVER_HOST:8090 \
  --workspace WS_ID \
  --api-key 'YOUR_API_KEY' \
  --systemd                           # omit for foreground smoke mode
```

详细参数与 observer 的部署参见下表中的子目录 README。

| Path | Target |
|---|---|
| [`linux/observer`](linux/observer/) | Generic `observer-server` install. SQLite-backed HTTP daemon (default `:8090`); foreground or `--systemd`. amd64 / arm64. |
| [`linux/driver`](linux/driver/) | Generic `driver-agent` install into a Claude Code project dir (no systemd — Claude Code launches the MCP server on demand). |
| [`linux/slave`](linux/slave/) | Generic `slave-agent` install on any Linux host. Foreground smoke mode or `--systemd` for a managed service. amd64 / arm64. |
| [`linux/compose-test`](linux/compose-test/) | docker-compose end-to-end test wiring all three installers together against a local observer; surfaces the device-code "join workspace" URLs each role prints on first start. |

Pre-built binaries for each release are published at
<https://github.com/agentserver/loom/releases>. Each `install.sh` accepts
`--bin PATH` to point at a downloaded asset; otherwise it looks in `./bin/`
relative to itself.

For the pre-wired prod-test bundle (`driver-prod`, `slave-jetson-prod`,
`slave-local-prod` against `agent.cs.ac.cn` / `ws-prod`), see
[`../tests/prod_test/`](../tests/prod_test/) — that bundle is for the
project's own staging environment and is gitignored.
