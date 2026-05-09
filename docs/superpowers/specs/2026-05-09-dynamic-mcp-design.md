# Dynamic MCP Server Creation — Design

**Date:** 2026-05-09
**Status:** Draft (awaiting user review)
**Scope:** Extend `multi-agent` so that, when a task requires a tool no agent currently exposes, the master can dispatch a `build_mcp` sub-task to a slave that has the `build_mcp` skill plus the required hardware/runtime resources. The slave uses claude to author a Python MCP server, validates and registers it at runtime, persists it under a clearly-namespaced `generated_mcp/` directory, and re-publishes its agent card. The master then automatically re-plans the rest of the work with the new tool visible. A bounded 3-iteration negotiation loop lets the planner expand `allowed_packages` or revise the spec if the first build attempt is blocked.

## Motivation

Today the planner picks among the tools agents already advertise. When a workspace lacks a needed tool, the user has to write the tool by hand, register it in a slave's `config.yaml` under `mcp_servers:`, and restart the slave. This breaks the "framework" feel: the workspace can't grow new capabilities autonomously in response to a task.

This spec adds an autonomous build-and-register loop while keeping the framework's core contracts intact: existing `mcp_servers:` static configuration still works; the orchestrator's DAG model still works; the only orchestrator change is a small "phase boundary" check that invokes the planner a second time after a `build_mcp` node completes.

## Non-goals

- No fully sandboxed Python execution. Generated code runs as a normal subprocess of the slave; trust comes from (a) the strict import allow-list and (b) the slave operator implicitly trusting their own claude session. A real sandbox (firejail, gvisor, container) is left for future work and noted in risks.
- No git-style commit/branch model for generated code. Versions are numbered (`v1.py`, `v2.py`, …); old versions stay on disk for diffing/rollback but no formal VCS.
- No automatic tool-removal / GC of unused generated MCP servers. Operators can manually delete `generated_mcp/<name>/` and the corresponding `dynamic_mcp.yaml` entry.
- No support for non-stdio MCP transports in generated servers. Generated servers are always Python stdio JSON-RPC subprocesses, matching the existing `testdata/fake-mcp-stdio/main.go` wire format.
- The 3-iteration negotiation cap is hardcoded for v1. Making it dynamic / per-task overridable is a future extension and explicitly noted below.

## Architecture

### High-level data flow

```
[user] → master fanout task
                              ┌─ Phase 1 (build) ──────────────────────────────────────┐
master.planner (claude)       │  planner reads agent cards (skills + tools + resources)│
   sees no agent has tool X   │  picks slave Y (has skill build_mcp + matching res)    │
   emits 1-node DAG:          │  emits {id:n0, kind:"build_mcp",                       │
     [n0 build_mcp@Y]         │         target_id:Y, prompt:<spec JSON>}               │
                              │                                                        │
                              │  master delegates n0 → Y (skill="build_mcp")           │
                              │  Y.dispatch routes skill=build_mcp → BuildMCPExecutor  │
                              │     - parses spec JSON                                 │
                              │     - composes claude prompt (spec + optional prior    │
                              │       code + optional bridge URL for compose_servers)  │
                              │     - invokes claude (existing claudeExec wire format) │
                              │     - validates (ast.parse + import allow-list)        │
                              │     - smoke-launches (tools/list)                      │
                              │     - registers via mcpExec.RegisterStdio              │
                              │     - persists to dynamic_mcp.yaml                     │
                              │     - re-publishes agent card with new tool listed     │
                              │     - returns task.Complete with handle JSON           │
                              │       (type:mcp_tool_set OR type:build_mcp_blocked)    │
                              └────────────────────────────────────────────────────────┘
                              ┌─ Negotiation loop (up to 3 iterations) ────────────────┐
orchestrator inspects         │  if handle.type == "build_mcp_blocked":                │
   handle.type:               │    if iter < 3:                                        │
                              │      re-call planner with blocked spec attached;       │
                              │      planner emits new build_mcp node (expanded        │
                              │      packages OR revised hints OR abandon)             │
                              │    if iter >= 3 or planner abandons:                   │
                              │      mark master task failed                           │
                              └────────────────────────────────────────────────────────┘
                              ┌─ Phase 2 (use) ────────────────────────────────────────┐
                              │  if handle.type == "mcp_tool_set":                     │
                              │    DiscoverAgents (Y now advertises tool X)            │
                              │    planner.Plan(originalPrompt + buildSummary, agents) │
                              │    append resulting nodes to running DAG               │
                              │    schedule normally; SDK.DelegateTask flows as today  │
                              └────────────────────────────────────────────────────────┘
master.reducer → final summary → task.Complete
```

### Repository layout (new and modified)

```
multi-agent/
├── internal/
│   ├── config/config.go                    # +Resources struct (yaml)
│   ├── orchestrator/
│   │   ├── fanout.go                       # +phase boundary: handle build_mcp output handles
│   │   └── fanout_test.go                  # +cases for blocked + tool_set + iter cap
│   ├── planner/
│   │   ├── planner.go                      # +Node.Kind field
│   │   └── prompts.go                      # +instructions about build_mcp
│   ├── executor/
│   │   ├── buildmcp.go                     # NEW: BuildMCPExecutor
│   │   ├── buildmcp_test.go                # NEW
│   │   └── mcp.go                          # +RegisterStdio + ListTools
│   └── tunnel/tunnel.go                    # +PublishCard reads ListTools, includes Resources
├── cmd/slave-agent/main.go                 # wire build_mcp into routes; load dynamic_mcp.yaml
├── examples/dynamic-mcp/
│   ├── README.md
│   ├── agent-builder/
│   │   ├── main.go                         # thin wrapper, mostly delegates to cmd/slave-agent
│   │   └── config.example.yaml
│   ├── e2e-driver/main.go                  # follows image-pipeline driver pattern
│   └── scripts/e2e.sh
└── docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md   # this file
```

Untouched: `pkg/transport/*`, `examples/image-pipeline/*`, all framework internals not listed above.

## Detailed design

### 1. Resource declaration (slave config + agent card)

Slave `config.yaml` gains an optional `resources:` block:

```yaml
resources:
  cpu:
    cores: 8
    arch: x86_64
  gpu:
    count: 1
    model: "NVIDIA RTX 4090"
    vram_gb: 24
  memory_gb: 32
  devices: [camera, microphone, gpio]
  tags: [photogrammetry, low-latency]
```

All fields are optional. The framework does not interpret these values — they pass through verbatim to the agent card and into the planner's prompt context, so that real claude can reason about them ("user wants a photo → pick the agent with `devices:[camera]`").

Go types in `internal/config/config.go`:

```go
type Resources struct {
    CPU      *CPUSpec `yaml:"cpu,omitempty"       json:"cpu,omitempty"`
    GPU      *GPUSpec `yaml:"gpu,omitempty"       json:"gpu,omitempty"`
    MemoryGB int      `yaml:"memory_gb,omitempty" json:"memory_gb,omitempty"`
    Devices  []string `yaml:"devices,omitempty"   json:"devices,omitempty"`
    Tags     []string `yaml:"tags,omitempty"      json:"tags,omitempty"`
}
type CPUSpec struct {
    Cores int    `yaml:"cores"            json:"cores"`
    Arch  string `yaml:"arch,omitempty"   json:"arch,omitempty"`
}
type GPUSpec struct {
    Count  int    `yaml:"count"               json:"count"`
    Model  string `yaml:"model,omitempty"     json:"model,omitempty"`
    VRAMGB int    `yaml:"vram_gb,omitempty"   json:"vram_gb,omitempty"`
}
```

Auto-detection of CPU / RAM / devices is out of scope for v1. Operators write the values they want to advertise.

### 2. Agent card extension

`internal/tunnel/tunnel.go:PublishCard` is extended to include two new fields under `card`:

```json
{
  "skills":     ["chat", "mcp", "build_mcp"],
  "tools":      ["echo", "raise", "exif", "dimensions"],
  "resources":  {"cpu": {"cores": 8}, "devices": ["camera"], "tags": ["photogrammetry"]},
  "accepts_tasks": true,
  "has_web_ui":    true,
  "version":       "0.1.0"
}
```

`tools` is the flattened set of tool names from all currently-active stdio MCP servers (both static `mcp_servers:` from config.yaml and dynamic entries from `dynamic_mcp.yaml`). It is collected by calling `mcpExec.ListTools(ctx, name)` for each server and unioning the results.

`PublishCard` is invoked at startup (as today) and again from `BuildMCPExecutor` after a successful registration, so the planner's next `DiscoverAgents` call sees the new tool.

### 3. `BuildMCPExecutor` input/output contract

#### Input — task prompt (JSON)

The slave's dispatch routes `skill=build_mcp` to `BuildMCPExecutor`. The task prompt is parsed as:

```json
{
  "name":              "image_meta",
  "description":       "Read EXIF metadata + dimensions from an image URL.",
  "tools": [
    {
      "name":               "exif",
      "description":        "Return EXIF tags as JSON object.",
      "args_schema": {
        "type": "object",
        "properties": {"url": {"type": "string", "description": "image URL"}},
        "required": ["url"]
      },
      "result_description": "JSON object whose keys are EXIF tag names."
    }
  ],
  "hints":              "Use Pillow; allowed packages: pillow, requests.",
  "allowed_packages":   ["pillow", "requests"],
  "compose_servers":    [],
  "version":            1,
  "prior_path":         "",
  "patch_instructions": "",
  "iteration":          1,
  "max_iterations":     3
}
```

Field rules:

| Field | Required | Notes |
|---|---|---|
| `name` | yes | `[a-z][a-z0-9_]{0,31}`. Used as `mcp_servers` map key, `dynamic_mcp.yaml` key, and file-path component. |
| `description` | yes | Human-readable; flows into generated file's docstring. |
| `tools[]` | yes, ≥1 | Each `args_schema` must be a JSON-Schema `type: object`; the framework only validates structural shape, not full JSON-Schema semantics. |
| `hints` | no | Free-form guidance for claude. |
| `allowed_packages` | no, default `[]` | Whitelist of pip-installable third-party packages allowed to appear in `import` statements. |
| `compose_servers` | no, default `[]` | Names of the slave's other MCP servers whose tools the new server may call via the loopback bridge (§ 7). |
| `version` | yes, ≥1 | Determines the output filename `v<version>.py` and whether `prior_path` is used. |
| `prior_path` | required when `version >= 2` | File path of the previous version, read and included in claude's prompt. |
| `patch_instructions` | required when `version >= 2` | Free-form description of what to fix. |
| `iteration` | yes, ≥1 | 1-based current negotiation iteration. |
| `max_iterations` | yes, defaults to 3 in spec; orchestrator currently hardcodes 3 | Upper bound enforced by orchestrator. |

#### Output — task result (handle JSON)

Negotiable failures (claude wrote disallowed imports, syntax error, smoke-launch failed) are returned via `task.Complete` with `type: "build_mcp_blocked"` so the orchestrator can re-call the planner. Hard errors (malformed spec JSON, missing `prior_path`, OS / claude-binary errors that are not the LLM's "fault") use `task.Fail` and surface as a normal failed sub-task. The two output `type` values are:

**Success:**
```json
{
  "type":  "mcp_tool_set",
  "url":   "file:///root/.../generated_mcp/image_meta/v1.py",
  "meta": {
    "name":      "image_meta",
    "version":   "1",
    "tools":     "exif,dimensions",
    "slave_id":  "0549268a-…",
    "lines":     "123",
    "deps":      "pillow,requests",
    "iteration": "1"
  }
}
```

**Soft failure (negotiation needed):**
```json
{
  "type": "build_mcp_blocked",
  "url":  "",
  "meta": {
    "spec_name":      "image_meta",
    "iteration":      "1",
    "needed_packages":"requests",
    "reason":         "claude used requests for HTTP fetch; no stdlib equivalent fits",
    "stage":          "validate_imports"
  }
}
```

`stage` is informational: which step of the build pipeline rejected the artifact (`validate_imports`, `validate_syntax`, `smoke_launch`, `claude_invocation`).

### 4. `BuildMCPExecutor.Run` — internal pipeline

1. **Parse spec** from `Task.Prompt`. Reject malformed spec with `task.Fail` (this is a planner bug, not negotiation-recoverable).
2. **Load prior code** if `version >= 2`. Refuse if `prior_path` doesn't exist.
3. **Compose claude prompt:**
   - System prompt: 60-line MCP stdio protocol skeleton (matches `testdata/fake-mcp-stdio/main.go` wire format), instructions to emit *only* a complete Python file (no markdown fences, no commentary), required header lines.
   - User prompt: spec JSON + optional `prior_path` contents + optional `compose_servers` bridge documentation (URL + how to call) + a final "respond with python source only".
4. **Invoke claude** via the existing `executor.NewClaudeExecutor`-style subprocess, but without the capability epilogue (this is a build task, not a chat task). Plain `claude --print --output-format=stream-json` invocation; collect text output.
5. **Strip wrappers** from claude's output: if the response contains a `python` fenced code block, extract its contents; else use as-is. Discard anything before the first `import` or `#` line.
6. **Syntax validate:** `python3 -c 'import ast,sys; ast.parse(sys.stdin.read())'` with the source piped in. On failure → return `build_mcp_blocked` with `stage:"validate_syntax"`, `reason: <ast error>`.
7. **Import allow-list validate:** statically parse all top-level `import X` and `from X import …` statements; collect module names; require each to be either in `allowed_packages` or in Python's standard library (use a hardcoded stdlib name list embedded in `internal/executor/buildmcp.go`, derived from the published Python 3.11 stdlib index — about 200 names). On any name outside both → return `build_mcp_blocked` with `stage:"validate_imports"`, `needed_packages:"<comma-list of disallowed names>"`, `reason: <one-liner>`.
8. **Write to disk:** `<workdir>/generated_mcp/<name>/v<version>.py` with the strict header shown in § 5 below.
9. **Smoke-launch:** spawn `python3 path`, send one `tools/list` JSON-RPC request, await response within 2s. On timeout / non-2xx / malformed response → kill subprocess, delete file, return `build_mcp_blocked` with `stage:"smoke_launch"`.
10. **Register:** `mcpExec.RegisterStdio(name, MCPServerCfg{Transport:"stdio", Command:"python3", Args:[path]})`. If a server with the same name was already registered (i.e., this is a `version >= 2` evolution), the prior subprocess is killed and the new one becomes active.
11. **Persist:** compute `spec_hash = sha256(canonical-json(spec))` (where canonical-json sorts keys lexicographically) and append/replace the entry in `dynamic_mcp.yaml` with `spec_hash`, `version`, `created_at`, `tools`. Atomic write (write `.tmp` + rename). Before steps 4-9, if `dynamic_mcp.yaml` already has an entry with this `spec_name` AND its `spec_hash` matches the new spec AND its `version` matches AND the file at its `args[0]` exists, skip the build entirely and short-circuit to step 12 (idempotency: re-running the same build with the same spec is a no-op).
12. **Re-publish card:** call `tunnel.PublishCard(ctx)` so that subsequent `DiscoverAgents` returns the agent's updated `tools[]`.
13. **Return** `task.Complete` with the success handle JSON above.

### 5. Generated-code namespace

Layout under the slave's working directory:

```
$workdir/
├── config.yaml
├── dynamic_mcp.yaml          # framework-managed; edited only by BuildMCPExecutor
├── generated_mcp/
│   ├── README.md             # "These files are auto-generated; do not hand-edit."
│   ├── image_meta/
│   │   ├── v1.py
│   │   └── v2.py
│   └── another_tool/
│       └── v1.py
└── data.db
```

`dynamic_mcp.yaml`:

```yaml
servers:
  image_meta:
    transport: stdio
    command: python3
    args: [generated_mcp/image_meta/v2.py]
    version: 2
    created_at: 2026-05-09T13:30:00Z
    spec_hash: "sha256:abcd…"
    tools: [exif, dimensions]
```

Slave startup loads `config.yaml` first, then merges `dynamic_mcp.servers` into the `mcpExec.cfg` map. If a name appears in both, the `dynamic_mcp.yaml` entry overrides the static one (with a warning logged).

Header on every generated file:

```python
# -*- coding: utf-8 -*-
# AUTO-GENERATED by multi-agent build_mcp at 2026-05-09T13:30:00Z
# spec.name=image_meta  version=1  iteration=2
# spec_hash=sha256:abcd1234…  prior_path=
# DO NOT HAND-EDIT. To evolve this file, send another build_mcp task
# with version=N+1, prior_path=this file path, and patch_instructions=…
#
# This file is reserved for framework-generated MCP servers.
# Hand-written MCP servers belong elsewhere (referenced via config.yaml).
```

The `generated_mcp/` directory is added to the slave's `.gitignore` template (and the README warns: don't check it into a repo).

### 6. Orchestrator changes — phase boundary + negotiation

`internal/planner/planner.go` adds two fields to `Node`:

```go
type Node struct {
    ID        string   `json:"id"`
    TargetID  string   `json:"target_id"`
    Prompt    string   `json:"prompt"`
    DependsOn []string `json:"depends_on,omitempty"`
    Kind      string   `json:"kind,omitempty"`  // "" (default) or "build_mcp"
    Skill     string   `json:"skill,omitempty"` // "" → slave's default exec (claude); "mcp" → mcpExec; "build_mcp" → BuildMCPExecutor; etc.
}
```

`Skill` is needed so a Phase 2 "use" node can be dispatched to the slave's `mcp` executor (where the freshly-registered tool actually lives), bypassing claude. Without this, phase-2 nodes land on the slave's default claude executor, which today does not propagate `mcp_servers` to the `claude` CLI invocation. (Note: this same gap was identified during the image-pipeline brainstorming and worked around by skipping the slave-agent entirely. The dynamic-mcp feature requires the gap to be closed properly, so this spec includes the small framework change.)

The `runFanout` `DelegateTask` call now passes `Skill: n.Skill`:

```go
resp, err := o.sdk.DelegateTask(fanoutCtx, agentsdk.DelegateTaskRequest{
    TargetID:       n.TargetID,
    Skill:          n.Skill,                 // NEW
    Prompt:         prompt,
    TimeoutSeconds: o.cfg.SubTaskDefaults.TimeoutSec,
})
```

The slave's existing `dispatch.routes` map (`{"mcp": mcpExec, "build_mcp": buildMCPExec, "": claudeExec}`) handles routing.

The planner's prompt (§ 8 below) tells claude how to set `kind` AND `skill` consistently:
- For `kind: "build_mcp"`, set `skill: "build_mcp"`.
- For phase-2 use-nodes that target a freshly-built MCP tool, set `skill: "mcp"` and write `prompt` as the JSON `{"server":"<name>","tool":"<tool>","args":{...}}` matching the existing `mcp` executor contract (`internal/executor/mcp.go:Run`).
- For ordinary chat nodes, omit `skill` (or set `""`) — slave dispatches to claude as today.

`internal/orchestrator/fanout.go:runFanout` adds, in the `d.Status == "completed"` branch, after the existing output-saving code:

```go
if h, ok := transport.ParseHandle(d.Output); ok {
    switch h.Type {
    case "build_mcp_blocked":
        specName := h.Meta["spec_name"]
        iterCount[specName]++ // map kept on the orchestrator stack
        if iterCount[specName] > maxBuildIterations { // const = 3
            cancelAll()
            return executor.Result{}, fmt.Errorf(
                "build_mcp '%s' exhausted %d iterations; last need=%q reason=%q",
                specName, maxBuildIterations,
                h.Meta["needed_packages"], h.Meta["reason"])
        }
        // re-call planner with the blocked output appended as context
        ctx2 := t.Prompt + "\n\nBUILD_MCP_BLOCKED: " + d.Output
        newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
        if perr != nil { … fail … }
        if err := Validate(newPlan); err != nil { … fail … }
        appendNodesToScheduler(sched, newPlan, plan, rows) // see below

    case "mcp_tool_set":
        // re-discover agents (Y's card now lists the new tool)
        agents, _ = o.discoverFiltered(fanoutCtx)
        // re-call planner for phase 2
        ctx2 := t.Prompt + "\n\nBUILT: " + d.Output
        newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
        if perr != nil { … fail … }
        if err := Validate(newPlan); err != nil { … fail … }
        appendNodesToScheduler(sched, newPlan, plan, rows)
    }
}
```

`appendNodesToScheduler` is a new helper that:
- Assigns each new node a unique ID by prefixing its planner-emitted id with the parent build node's id (e.g., `n0_a`, `n0_b`) to avoid collision with nodes already in the DAG.
- Inserts the new nodes into the existing `Scheduler` via a new `Scheduler.Append([]planner.Node)` method.
- Inserts corresponding rows into `store.SubTaskRow` for `t.ID`.
- Does NOT re-validate the entire combined DAG; only validates the new nodes against themselves + their declared dependencies on the existing build node id.

Cycle/duplicate-id checking: `Validate(newPlan)` already catches cycles and duplicates within the new plan. The id-prefixing guards against duplicates with the existing DAG.

`maxBuildIterations` is a package-level constant `const maxBuildIterations = 3`. v1 hardcodes; future work makes it configurable per-task. (This is documented in the orchestrator's package doc and again in the README.)

If a node's `kind=="build_mcp"` and the build fails with `task.Fail` (not the negotiable handle), the existing fail path applies: in `all_or_nothing`, abort; in `best_effort`, downstream gets skipped.

### 7. Composing existing MCP servers from generated code (optional)

When the spec includes `compose_servers: ["image"]`, `BuildMCPExecutor` adds to the claude prompt:

> Your code may call the slave's other MCP servers via a loopback HTTP bridge:
> `POST http://127.0.0.1:<bridge_port>/bridge/call`
> with body `{"server":"<name>","tool":"<tool>","args":{...}}`.
> The response body is the MCP tool's result. Use the `requests` library
> (must be in `allowed_packages` if you import it). The bridge is local
> only and goes away when the slave process exits.

The bridge itself is a small `http.Handler` registered on the slave's existing webui mux (`internal/webui/server.go`) under the `/bridge/` path, behind a slave-internal token (the slave's `Credentials.ProxyToken` is reused as the bearer; only callers from `127.0.0.1` are accepted as a defense-in-depth check). The handler routes the request through `mcpExec.callStdio` / `callHTTP` and returns the raw MCP result. The bridge endpoint is added in `internal/webui/server.go` and exists whether or not any generated server uses it (cost is negligible).

If `compose_servers` is empty, the bridge is still mounted but the prompt doesn't mention it.

### 8. Planner prompt extension

`internal/planner/prompts.go:planPrompt` is extended with these instructions, inserted after the existing DAG schema description:

```
Each agent card lists `skills`, `tools`, and `resources`. When deciding the DAG:

1. If the work needs a tool that some agent already lists in `tools`, emit
   a node targeting that agent. Set `skill: "mcp"` and write the prompt
   as `{"server":"<server-name>","tool":"<tool-name>","args":{...}}` so
   the slave's mcp executor handles it directly. Omit `kind`.

2. If no agent lists the needed tool but at least one agent has skill
   `build_mcp` and resources matching the requirement, you MAY emit a
   sub-task node with `kind: "build_mcp"`, `skill: "build_mcp"`, and prompt
   set to a JSON spec:
       {"name":"...", "description":"...",
        "tools":[{"name":"...","description":"...","args_schema":{...},
                  "result_description":"..."}],
        "hints":"...", "allowed_packages":["..."], "compose_servers":[],
        "version":1, "iteration":1, "max_iterations":3}
   Emit ONLY the build node — DO NOT also emit the use nodes in this
   plan. The orchestrator schedules the build, then automatically calls
   you again with the agent's updated `tools` list, and you plan the
   use phase then.

3. If a build_mcp sub-task returns an output handle of
   type "build_mcp_blocked", I will call you again with the blocked
   output appended to the original task prompt under the marker
   "BUILD_MCP_BLOCKED: …". You may:
   (a) emit a new build_mcp node with expanded allowed_packages,
   (b) emit a new build_mcp node with revised hints / spec,
   (c) abandon — emit a single chat-skill node that explains the
       failure to the user.
   The build iteration counter is bounded at 3 globally; after that I
   fail the master task.

4. Match resources sensibly. A camera-required tool goes to a slave with
   `devices:[camera]`. Heavy compute goes to one with `gpu`. Use the
   resource fields literally — they are not a fixed schema.
```

`agentsJSON` is updated to include `Tools` and `Resources` fields when serializing each card so they reach the prompt verbatim.

### 9. Slave wiring (`cmd/slave-agent/main.go`)

`main.go` is extended to:
- Parse the optional `dynamic_mcp.yaml` file (alongside `config.yaml`).
- Register `build_mcp` as a route in `dispatch.routes`, pointing to a `BuildMCPExecutor` constructed with: the slave's claude bin, the slave's `mcpExec` (so it can `RegisterStdio`), the slave's `tunnel` (so it can `PublishCard`), and the slave's working directory.
- Pass the slave's `Resources` config through to `tunnel.PublishCard` so the card published at boot already includes resource info.

A slave that wants to participate in `build_mcp` adds `build_mcp` to its `discovery.skills`. A slave that doesn't simply omits the skill — `BuildMCPExecutor` is still wired (cost ≈ a few struct fields) but unused.

## Demo / e2e (`examples/dynamic-mcp/`)

### Scenario

Master receives: "Compute the SHA-256 hash of the image at https://example.com/foo.png and tell me whether the last hex digit is even or odd."

Initial agent cards in the workspace:
- `agent-builder`: skills `[chat, mcp, build_mcp]`, resources `{tags:[crypto]}`, tools `[]`.

Phase 1 — planner emits:
```json
[{"id":"n0", "target_id":"<builder>", "kind":"build_mcp",
  "prompt":"{\"name\":\"image_hash\", \"description\":\"…\",
            \"tools\":[{\"name\":\"sha256_parity\",
                        \"args_schema\":{...},
                        \"result_description\":\"…\"}],
            \"allowed_packages\":[\"requests\"],
            \"version\":1, \"iteration\":1, \"max_iterations\":3}"}]
```

Builder runs the pipeline; success on first iteration; returns `mcp_tool_set` handle pointing at `generated_mcp/image_hash/v1.py`. Subsequent `DiscoverAgents` shows builder's `tools` includes `sha256_parity`.

Phase 2 — orchestrator calls planner again with the build summary appended; planner emits a single `n1` node targeting the same builder (skill defaults to "" → claude on builder, but with the new MCP tool now visible to claude via the existing `mcp_servers` route — claude calls the tool itself). Or, alternatively, planner emits a `skill: mcp` node with `prompt = '{"server":"image_hash","tool":"sha256_parity","args":{"url":"https://example.com/foo.png"}}'` and bypasses claude entirely. Both work; the demo uses the explicit `skill: mcp` form for determinism.

Reducer summarizes: "The hash is …; last digit is e (even)."

### Files

```
examples/dynamic-mcp/
├── README.md
├── agent-builder/
│   ├── main.go                  # invokes cmd/slave-agent's run() with build_mcp wiring
│   └── config.example.yaml
├── e2e-driver/main.go           # follows examples/image-pipeline/e2e-driver pattern
└── scripts/e2e.sh
```

### E2E assertions

- Master task `status == completed`.
- Reducer output contains both "even" or "odd" (case-insensitive) and a 64-hex-character string.
- File `<builder workdir>/generated_mcp/image_hash/v1.py` exists.
- File starts with `# AUTO-GENERATED by multi-agent build_mcp` header.
- `<builder workdir>/dynamic_mcp.yaml` contains an `image_hash` entry with `version: 1`.
- (Optional, with `--master-data-db`) `sub_tasks` rows: n0 completed with `output` parsing as `mcp_tool_set` handle; the appended use-node completed.

### Cleanup

The e2e script's EXIT trap deletes the builder's `generated_mcp/` directory and `dynamic_mcp.yaml` so the next run starts clean. Operators running the demo manually are warned in the README to do the same.

## Test matrix

| Layer | Location | Runner | Required env |
|---|---|---|---|
| Unit — `Resources` yaml round-trip | `internal/config/config_test.go` (extend) | `go test` | none |
| Unit — `MCPExecutor.RegisterStdio` replace semantics | `internal/executor/mcp_test.go` (extend; uses `testdata/fake-mcp-stdio` v1 then v2) | `go test` | none |
| Unit — `MCPExecutor.ListTools` | `internal/executor/mcp_test.go` (extend) | `go test` | none |
| Unit — `BuildMCPExecutor.runBuild` (testable helper using `testdata/fake-claude.sh` and a fake-emit-python script) | `internal/executor/buildmcp_test.go` (NEW) | `go test` | python3 on PATH |
| Unit — orchestrator negotiation loop (fake planner returns `build_mcp_blocked` then `mcp_tool_set`; assert iteration cap; assert phase-2 replan) | `internal/orchestrator/fanout_test.go` (extend) | `go test` | none |
| Smoke — `examples/dynamic-mcp/agent-builder` task handler | `agent-builder/main_test.go` | `go test` | python3 on PATH |
| E2E — full pipeline | `examples/dynamic-mcp/scripts/e2e.sh` | bash, manual | pre-registered configs (master + builder + driver) + `AGENTSERVER_URL` + `claude` + `python3` + `sqlite3` |

CI gates the first six (`go test ./...` once the new packages exist). The bash e2e is documented as manual, mirroring the image-pipeline pattern.

## Open risks and mitigations

- **LLM-written Python runs unsandboxed in the slave process tree.** Mitigations: strict import allow-list (§ 3 step 7); a stdlib name list embedded in the Go source so it's auditable; smoke-launch must succeed (§ 4 step 9); `generated_mcp/` is gitignored so accidental commits don't poison shared repos. Real sandboxing (firejail / container / restricted user) is left for a future spec.
- **`max_iterations = 3` is hardcoded.** Documented in this spec, in `internal/orchestrator/fanout.go` package doc, and in the README. Future work: make it overridable via the `Fanout` config block, falling back to 3 when unset.
- **Generated code can drift from the spec.** Mitigations: `spec_hash` field in `dynamic_mcp.yaml` is the SHA-256 of the canonical-JSON-encoded spec; on registration, if a generated file's spec_hash matches an existing entry's, we skip rebuild and reuse the file (idempotency). Operators wanting to force rebuild bump `version`.
- **Real claude as planner may not emit `kind: "build_mcp"` correctly.** Mitigations: planner prompt is explicit (§ 8); the spec JSON shape is documented in the prompt; if planner emits a malformed spec, `BuildMCPExecutor` calls `task.Fail` (not negotiable), which surfaces the error in the master's reducer.
- **Race between `RegisterStdio` and a concurrent task using the same name.** `mcpExec` already serializes on `e.mu` for `connStdio` / `Close`. `RegisterStdio` takes the same lock and kills the prior subprocess before adding the new entry. A task in flight against the prior subprocess will get a connection error on its next call; the orchestrator's normal failure path applies. Operators are warned in the README: don't bump `version` while a use-phase is mid-flight.
- **Bridge security:** the loopback bridge accepts only `127.0.0.1` connections and requires the slave's proxy_token as bearer. Generated code knows the token from its environment when spawned. A malicious generated server could leak the token, but generated code is already trusted (it runs as the slave's user and can read the slave's config).
- **`tools/list` smoke-launch may be insufficient** to catch runtime bugs — the tool may be listable but crash on actual `tools/call`. v1 accepts this risk; future work might run a synthetic `tools/call` per declared tool with example arguments derived from the args_schema.

## Deliverables checklist

- [ ] `multi-agent/internal/config/config.go` — `Resources`, `CPUSpec`, `GPUSpec` types + yaml tags
- [ ] `multi-agent/internal/config/config_test.go` — round-trip tests
- [ ] `multi-agent/internal/executor/mcp.go` — `RegisterStdio`, `ListTools`
- [ ] `multi-agent/internal/executor/mcp_test.go` — replace-semantics + ListTools tests
- [ ] `multi-agent/internal/executor/buildmcp.go` — `BuildMCPExecutor` + stdlib name list constant
- [ ] `multi-agent/internal/executor/buildmcp_test.go`
- [ ] `multi-agent/internal/planner/planner.go` — `Node.Kind`, `Node.Skill`
- [ ] `multi-agent/internal/planner/prompts.go` — extended planPrompt + agentsJSON includes Tools/Resources
- [ ] `multi-agent/internal/orchestrator/fanout.go` — phase boundary, negotiation loop, `appendNodesToScheduler`, `DelegateTask` now passes `Skill: n.Skill`
- [ ] `multi-agent/internal/orchestrator/fanout_test.go` — negotiation cases
- [ ] `multi-agent/internal/orchestrator/dag.go` — `Scheduler.Append([]planner.Node)`
- [ ] `multi-agent/internal/tunnel/tunnel.go` — PublishCard uses ListTools, includes Resources
- [ ] `multi-agent/internal/webui/server.go` — `/bridge/call` handler
- [ ] `multi-agent/cmd/slave-agent/main.go` — wire build_mcp; load dynamic_mcp.yaml; pass Resources to PublishCard
- [ ] `multi-agent/cmd/slave-agent/config.example.yaml` — document `resources:` block
- [ ] `multi-agent/examples/dynamic-mcp/README.md`
- [ ] `multi-agent/examples/dynamic-mcp/agent-builder/{main.go,main_test.go,config.example.yaml}`
- [ ] `multi-agent/examples/dynamic-mcp/e2e-driver/main.go`
- [ ] `multi-agent/examples/dynamic-mcp/scripts/e2e.sh`
- [ ] All of the above pass `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] `examples/dynamic-mcp/scripts/e2e.sh` runs green end-to-end with pre-registered configs + real `AGENTSERVER_URL` + real `claude` + `python3` available
