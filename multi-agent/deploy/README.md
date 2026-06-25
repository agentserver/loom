# deploy

Production bring-up templates for the agents in `cmd/`. Unlike `examples/`
(which are end-to-end Go demos), each subdirectory here ships an installer
script and config templates you point at a real host.

Drivers and slaves can each run under **Claude Code**, **Codex CLI**, or
**opencode**, chosen independently per agent via `--agent claude|codex|opencode`.
A mixed fleet is fully supported: Claude Code drivers alongside Codex slaves,
or any other combination, all sharing the same observer / workspace.

## Quick start

All three roles deploy via release-hosted bootstrap scripts — **no repo
clone required**, on any Linux (amd64 / arm64) or Termux/Android (aarch64).
Default backend is **Codex CLI**; pass `--agent claude` or `--agent opencode`
to switch.

**Prerequisites**: `codex` CLI (`npm i -g @openai/codex`, Node ≥ 22) +
`codex login` or `export OPENAI_API_KEY=...`. Codex can target any
OpenAI-compatible endpoint via `[model_providers.<name>]` in
`~/.codex/config.toml`.

### Observer

```bash
export LOOM_WORKSPACE_ID=WS_ID LOOM_API_KEY='YOUR_API_KEY'   # both optional
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-observer.sh) \
  --name obs-prod --systemd
```

Installs observer-server into `~/.loom/<NAME>/`, seeds one workspace with a
bootstrap api-key (random if `LOOM_API_KEY` is unset; printed once on
stdout). `--systemd` optional.

### Slave

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-slave.sh) \
  --name slave-myhost --systemd          # drop --systemd on Termux/Android
```

Installs slave-agent into `~/.loom/<NAME>/` (binary + `config.yaml`,
CPU / memory / arch auto-detected). First start prints a device-code URL
on stderr — approve it in a browser; agentserver issues a proxy token
that the slave uses to authenticate with observer directly.
No `LOOM_API_KEY` needed.

Foreground mode: `~/.loom/<NAME>/slave-agent ~/.loom/<NAME>/config.yaml`.

### Driver

```bash
export LOOM_OBSERVER_URL=http://OBSERVER_HOST:8090
bash <(curl -fsSL \
  https://github.com/agentserver/loom/releases/latest/download/bootstrap-driver.sh) \
  --name driver-myhost
```

Drops a coding-agent driver project into the current directory (binary +
`config.yaml` + `.codex/config.toml` + `AGENTS.md`). After bootstrap:

```bash
./driver-agent register --config ./config.yaml   # one-time device-code OAuth
cd <project-dir> && codex                         # first run prompts to trust the dir
```

Codex reads `[mcp_servers.driver]` from `.codex/config.toml` and
auto-launches `driver-agent serve-mcp`. Driver is NOT a long-running
daemon — Codex starts it on session open, talks via stdio, and tears it
down on exit. No `--systemd`.

### Switching backends

Pass `--agent claude` or `--agent opencode` to any bootstrap script:

| `--agent` | Driver writes | Slave chat skill | Notes |
|---|---|---|---|
| `codex` (default) | `.codex/config.toml` + `AGENTS.md` | `codex exec --json` | Needs `codex login` or `OPENAI_API_KEY`; first `codex` run prompts to trust the project dir |
| `claude` | `.mcp.json` + `.claude/skills/` | `claude --print --output-format=stream-json` | Needs `claude login` or `ANTHROPIC_API_KEY` |
| `opencode` | `~/.config/opencode/opencode.json` + `AGENTS.md` | — (driver only) | opencode CLI + desktop share one MCP config |

Mixed fleets (codex drivers + claude slaves, etc.) are fully supported —
all agents share the same observer / workspace.

详细参数请见下表中各子目录的 README；如果手头已经 clone 了仓库，也可以
跳过 bootstrap，直接用各目录的 `install.sh`（功能等价、可读 `--bin PATH`）。

| Path | Target |
|---|---|
| [`linux/observer`](linux/observer/) | Generic `observer-server` install. SQLite-backed HTTP daemon (default `:8090`); foreground or `--systemd`. amd64 / arm64. Also serves the `mcp-userspace` package registry. |
| [`linux/driver`](linux/driver/) | Generic `driver-agent` install into a coding-agent project dir (no systemd — the coding agent launches the MCP server on demand). Supports `--agent codex` (default), `--agent claude`, and `--agent opencode`. |
| [`linux/slave`](linux/slave/) | Generic `slave-agent` install on any Linux host. Foreground smoke mode or `--systemd` for a managed service. amd64 / arm64. Supports `--agent codex` (default) / `--agent claude` per slave. |
| [`linux/compose-test`](linux/compose-test/) | docker-compose end-to-end test wiring all three installers together against a local observer; surfaces the device-code "join workspace" URLs each role prints on first start. |
| [`bin/`](bin/) | Local cache of release binaries used by `install.sh` when `--bin PATH` isn't supplied. |

`agent-backends.md` covers the per-backend config differences: example
`[model_providers.<name>]` block for pointing Codex CLI at any
OpenAI-compatible endpoint, the container caveat that project-level
`.codex/config.toml` needs an interactive trust prompt (mount the global
config in containers instead), and `permissions`-skill JSON examples for
all backends.

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

For the pre-wired prod-test bundle (driver + two slave instances against
`agent.cs.ac.cn`), see
[`../tests/prod_test/`](../tests/prod_test/) and its `E2E_RUNBOOK.md` — that
bundle is for the project's own staging environment and is gitignored.

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
