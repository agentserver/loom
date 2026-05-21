# deploy

Production bring-up templates for the agents in `cmd/`. Unlike `examples/`
(which are end-to-end Go demos), each subdirectory here ships an installer
script and config templates you point at a real host.

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
