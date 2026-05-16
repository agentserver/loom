# dynamic-mcp

End-to-end example for the autonomous build-and-register loop.

## Scenario

The master receives a task that needs a tool no agent in the workspace
advertises (compute SHA-256 of an image and report parity of last hex digit).
Master's planner sees that no agent has a `sha256_parity` tool, but the
`dynmcp-builder` agent has skill `build_mcp` and resources tagged
`[crypto, python3]`. It emits a `kind: "build_mcp"` node first; the builder
authors a Python MCP server via claude, validates and registers it, and the
orchestrator re-plans phase 2 with the new tool visible. Phase 2 emits a
`skill: "mcp"` use node that calls the freshly-registered tool. The reducer
summarizes.

If the first build attempt is blocked (e.g., claude wrote `import requests`
but the spec only allowed stdlib), the orchestrator re-calls the planner
with `BUILD_MCP_BLOCKED:` context appended; the planner can expand
`allowed_packages`, revise the spec, or abandon. Iteration is bounded at 3.

## Layout

- `agent-builder/config.example.yaml` — slave-agent config with `build_mcp` skill + `resources:` advertising `tags: [crypto, python3]`
- `e2e-driver/main.go` — Go binary that DelegateTasks the master and asserts the reducer output + checks the generated file landed on disk with the AUTO-GENERATED header
- `scripts/e2e.sh` — bash wrapper that builds, launches master + builder slave, runs the driver, and reports
- `scenarios/driver-files-multi-mcp/` — fixture-level scenario showing a driver-side file task that should build multiple independent MCP services in the first phase

## First-time setup

Three pre-registered configs needed (master + builder + driver). Same flow as
`examples/image-pipeline/README.md`: copy `config.example.yaml`, set
`server.url`, run the binary once interactively to complete device-flow login,
the binary writes credentials back. Repeat for each of the three.

For master: use `cmd/master-agent/config.example.yaml` with
`discovery.display_name: master-dynmcp`.

For builder: use `examples/dynamic-mcp/agent-builder/config.example.yaml`.
**Important:** the builder's config file should live in a stable directory
because `generated_mcp/` and `dynamic_mcp.yaml` are written next to it. A
fresh tmp dir on every run defeats persistence (and forces a fresh build).

## Running

```bash
export AGENTSERVER_URL=https://your-agentserver
# No ANTHROPIC_API_KEY needed if local claude CLI is logged in.
export MASTER_CONFIG=/persistent/path/master/config.yaml
export BUILDER_CONFIG=/persistent/path/builder/config.yaml
export DRIVER_CONFIG=/persistent/path/driver/config.yaml

cd multi-agent/   # module root
./examples/dynamic-mcp/scripts/e2e.sh
```

Expected last line: `OK dynamic-mcp e2e`. On failure the script tails master
and builder logs.

## Cleaning up generated state

Re-running the e2e with the same builder config reuses the generated server
(idempotent — `spec_hash` matches). To force a rebuild from scratch:

```bash
rm -rf $(dirname "$BUILDER_CONFIG")/generated_mcp
rm -f $(dirname "$BUILDER_CONFIG")/dynamic_mcp.yaml
```

## Caveats

- Generated Python runs as a subprocess of the builder; **it is not
  sandboxed**. The framework's defenses are (a) the strict import allow-list
  and (b) the smoke-launch precondition. A determined attacker controlling
  claude's output could still cause harm. Future work: real sandboxing.
- The 3-iteration negotiation cap is hardcoded. To raise it, edit
  `internal/orchestrator/fanout.go`'s `maxBuildIterations` constant and rebuild.
- `generated_mcp/` should be git-ignored in any real deployment so generated
  code doesn't accidentally get committed.
