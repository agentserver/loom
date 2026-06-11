# deploy

Production bring-up templates for the agents in `cmd/`. Unlike `examples/`
(which are end-to-end Go demos), each subdirectory here ships an installer
script and config templates you point at a real host.

Drivers and slaves can each run under **Claude Code** or **Codex CLI**,
chosen independently per agent via `--agent claude|codex`. A mixed fleet is
fully supported: Claude Code drivers alongside Codex slaves, or any other
combination, all sharing the same observer / workspace.

## Quick start — one-liners

All three roles bring up with a single `bash <(curl -fsSL ...)` call
against a release-hosted bootstrap script — **no repo clone required**, on
any Linux host (amd64 / arm64) or Termux/Android (aarch64). Replace the
placeholders (`OBSERVER_HOST`, `WS_ID`, `YOUR_API_KEY`, agent names) with
your own values. Append `--systemd` to slave/observer for a managed unit
(needs sudo; not available on Termux).

### Observer

EN — install observer-server into `~/.loom/<NAME>/`, seed one workspace
with a bootstrap api-key (random if `LOOM_API_KEY` is unset; printed once
on stdout — copy it, slaves/drivers need it):

```bash
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'   # both optional
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-observer.sh) \
  --name obs-prod --systemd
```

中文 — 把 observer-server 装到 `~/.loom/<NAME>/`，初始化一个工作区和
bootstrap api-key（不传 `LOOM_API_KEY` 就随机生成并打印一次，slave / driver
注册时要用）。`--systemd` 可选，默认前台：

```bash
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'   # 两个都可省
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-observer.sh) \
  --name obs-prod --systemd
```

### Driver

#### Claude Code (default)

EN — drop a coding-agent driver project into the current directory (binary,
`config.yaml`, `.mcp.json`, skills bundle); next `claude` run auto-loads
the driver MCP and skills:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost
```

中文 — 在当前目录铺一个 coding-agent driver 工程（二进制 + `config.yaml` +
`.mcp.json` + skills 包）；下次 `claude` 启动时 MCP 与 skills 自动加载：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost
```

启动前别忘了：`./driver-agent register --config ./config.yaml` 走一次
device-code，再 `claude login`（或 `export ANTHROPIC_API_KEY=...`）。
Driver 不是常驻进程（Claude Code 按 `.mcp.json` 拉起来），所以没有 `--systemd`。

#### Codex

EN — Codex variant — drop the same driver project but write `.codex/config.toml`
instead of `.mcp.json`, and lay an `AGENTS.md` Codex can read on first launch:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost --agent codex
```

中文 —— Codex 版本，写入 `.codex/config.toml` 并附 `AGENTS.md`：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost --agent codex
```

启动前：`codex login`（订阅）或 `export OPENAI_API_KEY=...`；首次 `cd` 进项目
后跑一次 `codex` 让它把这个目录加入 trust list（否则项目级 `.codex/config.toml`
不会加载）。然后 `./driver-agent register --config ./config.yaml` 走 device-code。

### Slave

#### Claude Code (default)

EN — install slave-agent into `~/.loom/<NAME>/` (binary + `config.yaml`,
auto-detected CPU/memory/arch). Add `--systemd` to register a systemd
unit (Linux only, sudo); omit it for foreground / Termux:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --systemd          # drop --systemd on Termux/Android
```

中文 — 把 slave-agent 装到 `~/.loom/<NAME>/`（二进制 + `config.yaml`，
CPU / 内存 / 架构自动探测）。加 `--systemd` 走 systemd 托管（仅 Linux，需 sudo），
不加就前台 / Termux 跑：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --systemd          # Termux/Android 上去掉 --systemd
```

前台模式：`~/.loom/<NAME>/slave-agent ~/.loom/<NAME>/config.yaml`。
首启动 stderr 会打印 device-code 验证 URL —— 浏览器批准一次，凭证自动
回写 `config.yaml` 并注册到 observer。

#### Codex

EN — Codex variant — same install layout, but the `chat` skill spawns
`codex exec --json` instead of `claude --print --output-format=stream-json`.
One slave process = one backend; the choice is per-slave:

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --agent codex --systemd   # drop --systemd on Termux
```

中文 —— Codex 版本，`chat` skill 改用 `codex exec --json` 驱动，每个 slave
进程绑定一个后端，混合机队时按需选择：

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090 LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --agent codex --systemd   # Termux 上去掉 --systemd
```

启动前：`codex login`（订阅）或 `export OPENAI_API_KEY=...`；首次启动 stderr
会打印 device-code URL，浏览器批准后凭证自动回写 `config.yaml`。

详细参数请见下表中各子目录的 README；如果手头已经 clone 了仓库，也可以
跳过 bootstrap，直接用各目录的 `install.sh`（功能等价、可读 `--bin PATH`）。

| Path | Target |
|---|---|
| [`linux/observer`](linux/observer/) | Generic `observer-server` install. SQLite-backed HTTP daemon (default `:8090`); foreground or `--systemd`. amd64 / arm64. Also serves the `mcp-userspace` package registry. |
| [`linux/driver`](linux/driver/) | Generic `driver-agent` install into a coding-agent project dir (no systemd — the coding agent launches the MCP server on demand). Supports `--agent claude` (default) and `--agent codex`. |
| [`linux/slave`](linux/slave/) | Generic `slave-agent` install on any Linux host. Foreground smoke mode or `--systemd` for a managed service. amd64 / arm64. Supports `--agent claude` / `--agent codex` per slave. |
| [`linux/compose-test`](linux/compose-test/) | docker-compose end-to-end test wiring all three installers together against a local observer; surfaces the device-code "join workspace" URLs each role prints on first start. |
| [`bin/`](bin/) | Local cache of release binaries used by `install.sh` when `--bin PATH` isn't supplied. |

`agent-backends.md` covers the per-backend config differences (Claude
Code vs Codex): example `[model_providers.<name>]` block for pointing
Codex CLI at any OpenAI-compatible endpoint, the container caveat that
project-level `.codex/config.toml` needs an interactive trust prompt
(mount the global config in containers instead), and `permissions`-skill
JSON examples for both backends.

Pre-built binaries for each release are published at
<https://github.com/agentserver/loom/releases>. Each `install.sh` accepts
`--bin PATH` to point at a downloaded asset; otherwise it looks in `./bin/`
relative to itself.

## Publishing Releases

Push a `v*` tag to publish release assets through
`.github/workflows/release.yml`. The workflow builds the Linux / Windows
binaries, copies the bootstrap scripts, packages Codex prompts, packages
Claude driver skills, writes `sha256sums.txt`, and uploads everything to the
matching GitHub release.

The Claude driver skills bundle is generated by:

```bash
multi-agent/scripts/package-driver-skills.sh --tag vX.Y.Z --out dist/driver-skills.tar.gz
```

That script packages the whole committed `skills/` git tree for the selected
ref and then verifies the archive's top-level skill directories exactly match
`git ls-tree -d <ref>:skills`. Do not hand-roll `driver-skills.tar.gz`; newly
added skill directories are included automatically when they are committed
before the release tag is created.

For the pre-wired prod-test bundle (`driver-prod`, `slave-jetson-prod`,
`slave-local-prod` against `agent.cs.ac.cn` / `ws-prod`), see
[`../tests/prod_test/`](../tests/prod_test/) — that bundle is for the
project's own staging environment and is gitignored.

## Related: clients that talk to a deployed driver

These don't ship via `deploy/` because they live next to the driver on
the user's machine, not on a server, but the same release publishes them:

- **`mcp-userspace` CLI** — `cmd/mcp-userspace/`. Push validated MCP
  servers / skills to your observer-hosted personal space and `install`
  them on another device or workspace. See the
  [`userspace-publish`](../../skills/userspace-publish/SKILL.md) skill
  for the driver-side flow.
- **`loom-py` Python client** — [`multi-agent/python/`](../python/),
  PyPI name `loom`. Wraps the driver MCP surface as a fluent workflow
  API for scripts / notebooks; needs `driver-agent` on PATH but no
  Claude Code / Codex window open.
