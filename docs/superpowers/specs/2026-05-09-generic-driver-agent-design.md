# Generic Driver Agent — Design

**Date:** 2026-05-09
**Status:** Draft (awaiting user review)
**Scope:** Add a first-class `driver-agent` that lets a user, working inside Claude Code (CLI or VS Code extension), use the multi-agent workspace through natural conversation. The driver runs locally as a stdio MCP server attached to Claude Code; it simultaneously holds an agentserver tunnel as a regular agent. The user can mention any local-machine file or directory path in their request; the driver registers those paths into a per-process FileRegistry and rewrites the prompt with a `<USER_FILES_MANIFEST>` block carrying agentserver-tunneled URLs. Master and slaves fetch bytes lazily through a new agentserver "peer proxy" route back to the driver's webui; if the user names output paths, slaves PUT result bytes back through the same channel and the driver atomically writes to the local disk. The orchestrator/planner/executor packages are not touched. The change set is: (1) a new `cmd/driver-agent` and `internal/driver` in `multi-agent`; (2) one new helper in `internal/webui`; (3) one paragraph appended to the planner prompt; (4) one new HTTP route (`/api/agent/peer/{short_id}/proxy/*`) and one new SDK helper (`Client.PeerProxy`) in `agentserver`.

## Motivation

Today the only way to drive the workspace is to write a Go binary like `examples/dynamic-mcp/e2e-driver/main.go`: hardcoded prompt, hardcoded skill, hardcoded asserts, and zero awareness of the user's filesystem. Anything beyond a one-shot demo means recompiling. Two consequences:

1. The "uniform interface to a heterogeneous, self-attaching agent cluster" promise of the framework can't actually be experienced — every driver is a one-off.
2. The user can't naturally reference their own data. They have to either pre-upload to some HTTP endpoint a slave can reach, or paste file contents into a prompt. Neither composes with how a developer actually works in an IDE.

This spec adds a generic driver:
- The user's "interface" is Claude Code (terminal CLI or VS Code extension). The driver registers as a stdio MCP server in Claude Code's MCP config; Claude calls its tools just like any other MCP server.
- The driver speaks two protocols: stdio MCP up to Claude Code, agentserver tunnel down to the workspace. Both run in the same Go process; lifecycles are tied (Claude Code exits → MCP child exits → agentserver tunnel closes → driver card disappears from the workspace).
- File and directory paths the user mentions are registered into a process-local FileRegistry; the driver mints handle URLs that point at its own webui, reachable via a new agentserver peer-proxy route (see § 1a) that authenticates via `proxy_token` and gates by workspace membership. Master and slaves fetch on demand; nothing is uploaded eagerly.
- Output paths are first-class. Slaves PUT result bytes; the driver atomically writes to the named local path. The reducer's task output, surfaced to Claude as a normal completion, is augmented with the list of files the driver actually wrote, so Claude can tell the user "I wrote /home/me/out/clean.parquet (12.3 MB)."

## Non-goals

- No multi-user mode for v1. One driver process serves one Claude Code session; cross-session state sharing is out of scope.
- No daemon mode. The driver does not survive Claude Code's exit. (A future spec may add a daemon variant; this design notes the seams.)
- No path allow-listing. The driver runs as the user's OS uid; OS file permissions are the security boundary. The driver writes a JSONL audit log so misuse is at least observable. (See § 5 for one hard rule that v1 does enforce.)
- No support for non-stdio MCP transport. Claude Code talks to the driver via stdio JSON-RPC, the standard local MCP shape.
- No replacement for the existing `examples/*/e2e-driver/main.go` test programs. They remain as Go-coded e2e harnesses; the new driver is for *interactive* use.
- No new orchestrator behavior. Master/planner/slaves are unaware that the agent on the other end is a driver vs. another orchestrator.
- Auto-detection of "write paths" from prose ("output to X") is out of scope for v1. The MCP `submit_task` tool takes `read_paths` and `write_paths` as explicit lists; Claude is expected to populate them.

## Architecture

### High-level data flow

```
+---------------------+     stdio MCP    +-------------------------+   agentserver tunnel    +-----------------+
|  Claude Code        |  JSON-RPC over   |  driver-agent           |  DelegateTask, Discover |  master-agent   |
|  (CLI or VS Code)   |  stdin/stdout    |  serve-mcp              | <---------------------> |   (orchestrator)|
|                     | <--------------> |                         |                         +-----------------+
|  user: "merge       |                  |  +-------------------+  |                                  |
|   /home/me/data into|                  |  |  FileRegistry     |  |                                  | DAG fanout
|   /home/me/out/x"   |                  |  |  - read tokens    |  |                                  v
|                     |                  |  |  - dir tokens     |  |                          +-----------------+
+---------------------+                  |  |  - put tokens     |  |  agentserver-tunneled    |  slave-agents   |
                                         |  +-------------------+  |  GET /files/blob/<sha>   |  (image-*,      |
                                         |  +-------------------+  | <----------------------- |   build_mcp,    |
                                         |  |  webui mux:       |  |  PUT /files/put/<token>  |   echo, ...)    |
                                         |  |  /files/blob/...  |  | -----------------------> +-----------------+
                                         |  |  /files/dir/...   |  |
                                         |  |  /files/put/...   |  |
                                         |  +-------------------+  |
                                         |  +-------------------+  |
                                         |  |  audit.log JSONL  |  |
                                         |  +-------------------+  |
                                         +-------------------------+
```

The driver process holds three things in memory: the agentserver `agentsdk.Client`, the FileRegistry (read/dir/put token tables), and the MCP server loop on stdin/stdout. The webui is the existing `internal/webui` mux with one new attached sub-mux at `/files/`.

### Lifecycle

```
Claude Code spawns:           driver-agent serve-mcp --config ~/.config/multi-agent/driver.yaml
   ↓ stdio
driver-agent process boots:
   1. config.Load(driver.yaml)
   2. tunnel.New + tunnel.EnsureRegistered (one-time device-flow already ran during `driver-agent register`)
   3. tunnel.PublishCard (display_name=driver-<user>, skills=[]; this driver does not accept tasks)
   4. webui.New + driver.MountFilesHandler (registers /files/* handlers)
   5. tunnel.Run goroutine (handles inbound proxied HTTP from master/slaves)
   6. mcpserver.New(stdio).Serve — blocks on stdin
   ↓ Claude Code closes stdin (user ends session)
driver-agent process:
   7. mcpserver returns (clean shutdown signal)
   8. tunnel.Close — agentserver removes the card
   9. process exits 0
```

### Why the driver is itself an agentserver agent

Three reasons, in importance order:

1. **Reachability.** Once we add the peer-proxy route described in § 1a, master and slaves can reach any agent in the workspace through `/api/agent/peer/{short_id}/proxy/...`, authenticated with the requester's `proxy_token` and gated by workspace membership. Making the driver an agent therefore gives master/slaves an authenticated path to the driver's `/files/*` endpoints. (The image-pipeline pattern of an agent-local `127.0.0.1:port` blob server cannot work here: slaves run on remote machines and cannot route to the user's laptop's loopback. The browser-facing `code-{shortID}.{baseDomain}` subdomain proxy uses cookie auth and only matches the built-in opencode/claudecode/openclaw prefixes, so it is not usable for inter-agent calls.)
2. **Discovery.** The planner sees the driver in `DiscoverAgents` and knows it exists. v1 doesn't use this — driver advertises empty skills, planner ignores it — but it leaves room for future "driver as a tool target" patterns (e.g., a slave asking the user a clarifying question through the driver).
3. **Symmetry.** master, slave, and now driver all use the same agentboot pattern (config → register → publish card → connect). One mental model for operators, one config schema family.

### Why the driver is the same process as the MCP server

Lifecycles match: each Claude Code session is one driver session. Combining them makes "the driver is online" and "Claude Code is open" the same thing — no orphan tunnels, no need to coordinate process supervision. A future "daemon" variant can split them; we explicitly note the seams in § 9.

## Detailed design

### 1a. agentserver: new peer-proxy route + SDK helper

The only protocol-level addition required of the agentserver fork (`agentserver/`) is a generic peer-proxy route. It is small, obvious, and does not interact with any existing route's auth.

**HTTP route** (registered next to the other `/api/agent/*` proxy routes in `agentserver/internal/server/server.go`):

```
ANY /api/agent/peer/{short_id}/proxy/{rest:.*}
```

Handler `agentserver/internal/server/agent_peer_proxy.go::handleAgentPeerProxy`:

1. Authenticate the requester via the existing `extractProxyTokenSandbox(w, r)` helper. Returns the requester's `Sandbox`.
2. Look up the target sandbox via `s.Sandboxes.ResolveByShortID(short_id)`. If absent → 404.
3. Compare workspaces: `requester.WorkspaceID == target.WorkspaceID`. If not → 403 (cross-workspace access denied).
4. Locate the target's active yamux session via the existing `tunnel.Registry`. If the target is offline → 502.
5. Open a new yamux stream, send `HTTPStreamMeta{ Path: "/" + rest, Method: r.Method, Header: r.Header (with Authorization stripped) }`, then stream the request body. Read the response and stream it back to the original client. (This mirrors `handleSubdomainProxy` exactly; the only difference is the auth check happened before forwarding.)
6. The 120-second total timeout that subdomain proxy uses is reused; long-running PUT/GET file transfers stay under it (a 1 GiB file at 100 Mbit/s is ~85 s — within budget; bigger transfers should chunk via range requests, future work).

**SDK helper** in `agentserver/pkg/agentsdk/peer.go`:

```go
// PeerProxy makes an HTTP request to another agent in the same workspace via
// the agentserver peer-proxy route. The path is the path on the target's
// webui (e.g., "/files/blob/abc..."), without the /api/agent/peer/{short_id}/proxy
// prefix. Authorization is added automatically.
func (c *Client) PeerProxy(ctx context.Context, method, targetShortID, path string, body io.Reader) (*http.Response, error)
```

Internally a thin wrapper: build URL = `c.cfg.ServerURL + "/api/agent/peer/" + targetShortID + "/proxy" + path`, set `Authorization: Bearer <proxy_token>`, do an `http.NewRequestWithContext` + `http.DefaultClient.Do`.

**Why this is the right scope for this spec:** the peer-proxy route is the smallest possible unblocker for "any agent can fetch any other agent's webui resource within the same workspace, authenticated, no new conventions." It generalizes beyond driver/files (a future feature could use it for, say, a master inspecting a slave's web UI for debugging without needing a browser cookie). It mirrors an existing pattern (the subdomain proxy) so review burden is minimal. Without it, no design that requires inter-agent HTTP works in a real cross-network deployment — only the colocated-loopback demos.

**Tests added on the agentserver side:**

| Layer | Location | Runner |
|---|---|---|
| Unit — `handleAgentPeerProxy` happy path forwards GET; auth fail returns 401; cross-workspace returns 403; offline target returns 502 | `agentserver/internal/server/agent_peer_proxy_test.go` (new) | `go test` |
| Unit — `Client.PeerProxy` builds correct URL + headers | `agentserver/pkg/agentsdk/peer_test.go` (new) | `go test` |

### 1. MCP tool surface

The driver exposes six tools to Claude Code via stdio MCP. JSON-RPC method `tools/list` returns these schemas; `tools/call` dispatches by name.

| Tool | Required input | Output |
|---|---|---|
| `list_agents` | `{}` | `{agents: [{display_name, agent_id, short_id, skills[], tools[], resources, description}]}` — the driver itself is filtered out |
| `submit_task` | `{prompt: string, read_paths?: string[], write_paths?: [{path: string, overwrite?: bool}], target_display_name?: string, skill?: string, timeout_sec?: int}` | `{task_id, target_id, target_display_name, manifest: {...same as embedded in prompt...}}` |
| `get_task` | `{task_id: string, include_subtasks?: bool}` | `{status, output?, failure_reason?, subtasks?[]}` |
| `wait_task` | `{task_id: string, poll_interval_sec?: int (default 3), timeout_sec?: int (default = remaining task timeout)}` | `{status, output, failure_reason?, written_files: [{path, bytes, sha256, written_at}]}` |
| `tail_subtasks` | `{task_id: string, since_seq?: int (default 0), max_wait_sec?: int (default 30)}` | `{cursor: int, events: [{seq, ts, node_id, target_display_name, status, output_summary?}]}` (long-poll: returns when ≥1 event arrives or timeout hits) |
| `cancel_task` | `{task_id: string}` | `{ok: bool, status: string}` |

Defaults applied inside `submit_task` if the caller omits the field:

| Field | Default | Source |
|---|---|---|
| `target_display_name` | `driver_defaults.target_display_name` from `driver.yaml`, or — if there is exactly one agent in `DiscoverAgents` whose `skills` includes `"fanout"` — that one | runtime |
| `skill` | `"fanout"` | hardcoded; explicit `chat`/`route` overrides |
| `timeout_sec` | `driver_defaults.task_timeout_sec` from `driver.yaml`, default 600 | yaml |

If `target_display_name` resolution returns 0 or >1 candidates, `submit_task` returns an MCP error with the candidate list, so Claude can re-call with an explicit target.

#### Handling input paths inside `submit_task`

Pseudocode of the tool handler (full Go in `internal/driver/tools.go`):

```
func SubmitTask(ctx, args) (Result, error) {
    manifest := Manifest{}
    for _, p := range args.read_paths {
        absP, err := filepath.Abs(p); if err != nil { return mcpErr("invalid path: %s", p) }
        info, err := os.Lstat(absP); if err != nil { return mcpErr("stat %s: %v", absP, err) }
        if isSymlink(info) { return mcpErr("symlinks not allowed: %s", absP) }
        if info.IsDir() {
            tok := registry.RegisterDir(absP)
            audit.Log("register_read_dir", absP, "")
            manifest.Files = append(manifest.Files, FileEntry{
                Path: absP, Kind: "dir",
                ListURL: serverURL("/files/dir/%s?recursive=true", tok),
                BlobURL: serverURL("/files/dir/%s/blob", tok),
            })
        } else {
            sha, size, mime, err := registry.RegisterFile(absP)
            if err != nil { return mcpErr(...) }
            audit.Log("register_read", absP, sha)
            manifest.Files = append(manifest.Files, FileEntry{
                Path: absP, Kind: "file", Bytes: size, MIME: mime, SHA256: sha,
                URL: serverURL("/files/blob/%s", sha),
            })
        }
    }
    for _, w := range args.write_paths {
        absP, err := filepath.Abs(w.path); if err != nil { return mcpErr(...) }
        if err := assertWritableTarget(absP); err != nil { return mcpErr(err.Error()) }
        tok := registry.RegisterWrite(absP, w.overwrite)
        audit.Log("register_write", absP, "")
        manifest.Writes = append(manifest.Writes, WriteEntry{
            Path: absP, Kind: "file", Overwrite: w.overwrite,
            PutURL: serverURL("/files/put/%s", tok),
        })
    }
    target, err := resolveTarget(args.target_display_name)
    if err != nil { return mcpErr(...) }
    finalPrompt := manifest.Encode() + "\n\n" + args.prompt
    resp, err := sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
        TargetID: target.AgentID, Skill: argsOr(args.skill, "fanout"),
        Prompt: finalPrompt, TimeoutSeconds: argsOr(args.timeout_sec, defaultTimeout),
    })
    if err != nil { return mcpErr(...) }
    pendingTasks.Track(resp.TaskID, manifest)   // for written_files reporting in wait_task
    return Result{TaskID: resp.TaskID, TargetID: target.AgentID,
                  TargetDisplayName: target.DisplayName, Manifest: manifest}, nil
}
```

`mcpErr` returns a JSON-RPC error with code `-32000` and message; Claude Code surfaces this to the user.

`tail_subtasks` is implemented by `cli.PeerProxy(ctx, "GET", masterShortID, fmt.Sprintf("/api/sub_tasks?task_id=%s&since_seq=%d", taskID, since), nil)` — master's webui already exposes a snapshot subtask endpoint (see `internal/webui/server.go`). The driver layers a 1 s poll loop client-side and returns the first non-empty result, falling back to an empty `events` after `max_wait_sec`. Master's existing endpoint may not have a `since_seq` parameter today; if it doesn't, the driver computes the diff client-side from successive snapshots and is responsible for `seq` numbering. The spec's deliverables include adding a minimal `?since_seq=` param to master's existing handler (one Go file, ~20 lines) so the driver doesn't have to dedupe redundant data over the wire. Real long-poll is future work.

### 2. The `<USER_FILES_MANIFEST>` block

`manifest.go::Encode()` returns:

```
<USER_FILES_MANIFEST version=1>
{"files":[
  {"path":"/home/me/data/raw.csv","kind":"file","mime":"text/csv","bytes":12345,
   "sha256":"abc123…","url":"https://AGENTSERVER/api/agent/peer/<driver_short_id>/proxy/files/blob/abc123…"},
  {"path":"/home/me/data","kind":"dir",
   "list_url":"https://AGENTSERVER/api/agent/peer/<driver_short_id>/proxy/files/dir/<dir_token>?recursive=true",
   "blob_url":"https://AGENTSERVER/api/agent/peer/<driver_short_id>/proxy/files/dir/<dir_token>/blob"}
],"writes":[
  {"path":"/home/me/out/clean.parquet","kind":"file","overwrite":true,
   "put_url":"https://AGENTSERVER/api/agent/peer/<driver_short_id>/proxy/files/put/<put_token>"}
]}
</USER_FILES_MANIFEST>
```

Then a blank line, then the user's original prompt verbatim.

Rules:

- Always emitted, even when both arrays are empty (`{"files":[],"writes":[]}`). This keeps the planner prompt's instructions about the manifest unconditional.
- The block is treated as opaque text by master and slaves: `agentsdk.DelegateTask` does not parse it, the orchestrator does not parse it, the planner sees it as part of the prompt. The planner is told (§ 4) to read it.
- `path` is always the absolute, `filepath.Abs`-normalized path the user provided (after resolving `~/` against `$HOME`). The driver does not lowercase, slashify, or otherwise transform; the value is what shows up in the audit log and what the user will recognize in Claude's responses.
- Server URL is `<config.Server.URL>/api/agent/peer/<driver.short_id>/proxy/files/...`. Both pieces are known after `EnsureRegistered`.

### 3. `/files/*` endpoints (driver webui)

All three handlers are mounted on the driver's webui mux via a new `internal/webui/server.go::SetDriverFiles(handler, fh http.Handler)` helper (mirrors the existing `SetMCPBridge` shape). The webui is invoked over the agentserver tunnel; agentserver's peer-proxy handler (§ 1a) has already validated the requester's `proxy_token` and workspace membership before forwarding, so these handlers do not re-authenticate. They do still require the request to arrive over the tunnel: a stray request hitting the local listener directly is rejected by the existing webui middleware (the tunnel attaches an `HTTPStreamMeta` header that the middleware checks for; absent → 401). This is the same defense the build_mcp `/bridge/call` handler uses.

| Endpoint | Method | Behavior |
|---|---|---|
| `/files/blob/{sha256}` | GET | Look up `sha256 → localpath` in FileRegistry. If absent → 404. Else `os.Open` the file under `O_NOFOLLOW`-equivalent guard, set `Content-Type` from the cached MIME, stream with `io.Copy`. Audit log: `fetch_blob`. |
| `/files/dir/{token}` | GET | Query: `recursive=true|false` (default false), `prefix=...` (optional, must be a clean relative path under the dir root). Walk the dir, return `{root: "/abs/dir", entries: [{relpath, size, mtime, sha256, is_dir}]}`. SHA256 is computed lazily and cached per (token, relpath) in the registry; first walk is the slow one. Audit log: `fetch_dir`. |
| `/files/dir/{token}/blob` | GET | Query: `path=relpath`. Resolve `realpath = filepath.Clean(filepath.Join(root, relpath))`; require `strings.HasPrefix(realpath+sep, rootRealpath+sep)` (i.e., no escape via `..`). Reject if any path component is a symlink (`os.Lstat` per component). Stream the file. Audit log: `fetch_blob`. |
| `/files/put/{put_token}` | PUT | Look up token → `{localpath, overwrite}`. Body is the raw bytes. Stream to `localpath + ".tmp.<random>"`, fsync, then `os.Rename` over `localpath`. If `os.Stat(localpath) == nil && !overwrite` → 409 Conflict before any byte is written. If `os.Stat(filepath.Dir(localpath)) returns ENOENT` → 409 (driver does not mkdir). Audit log: `put_blob` with bytes-written, sha-of-bytes (computed during stream). On rename failure: keep `.tmp` for inspection, return 500. |

Concurrency: FileRegistry is `sync.RWMutex`-protected; reads (handler lookups) take RLock, registrations take WLock. SHA256 cache for dir entries is a separate `sync.Map` to avoid contention during walks.

### 4. Planner prompt extension (one paragraph)

`internal/planner/prompts.go::planPrompt` gets a paragraph appended after the existing DAG schema and (if present) the build_mcp instructions:

```
The user's request may begin with a <USER_FILES_MANIFEST version=1> block
followed by a JSON object. The "files" array names files the user has
referenced; each entry has either a "url" (for a file) or "list_url" +
"blob_url" (for a directory). When you assign work to a slave that needs
to read a referenced file, include the relevant url in that node's prompt
so the slave can GET it. The "writes" array lists local paths the user
wants results written to; when a slave produces a result that should land
at one of those paths, include the matching "put_url" in the slave's
prompt and instruct the slave to PUT the resulting bytes to that URL
(the URL accepts a single PUT and returns 200 on success). The block
itself is metadata; do not echo it back, and do not invent additional
fields.
```

This is the only change to existing master code. Node, Skill, Kind — none touched. `agentsJSON` does not change shape (the driver advertises empty `skills`/`tools` and the existing serializer handles that).

### 5. Audit log and write safety

`audit.go` opens `~/.cache/multi-agent/<driver-short-id>/audit.log` (creating dirs as needed) for append-only writes. Each line is a JSON object on its own line, fsync'd after every write (acceptable cost — file ops are coarse-grained):

```json
{"ts":"2026-05-09T13:30:00Z","event":"register_read","path":"/home/me/data/raw.csv","sha256":"abc...","bytes":12345,"task_id":""}
{"ts":"2026-05-09T13:30:01Z","event":"fetch_blob","path":"/home/me/data/raw.csv","sha256":"abc...","bytes":12345,"task_id":"t-9af","peer_short_id":"slave-image-001"}
{"ts":"2026-05-09T13:30:30Z","event":"put_blob","path":"/home/me/out/clean.parquet","sha256":"def...","bytes":54321,"task_id":"t-9af","peer_short_id":"slave-build-002","overwrite":true}
```

`task_id` is set on `fetch_*` / `put_*` events (the FileRegistry tracks which task each token belongs to via `pendingTasks`); `register_*` events emit the empty string. `peer_short_id` is read from the request meta the agentserver tunnel attaches to each proxied stream (see `agentsdk.handlers.go::handleHTTPStreamWithMeta`); if absent, the empty string.

**One hard write rule that v1 enforces** (in `assertWritableTarget`):

The target path must lie under a directory whose owner uid matches the driver process uid. This blocks the obvious "Claude said `~/.ssh/id_rsa`" footgun on shared hosts (where `~/.ssh` may be the same uid but `/etc/...` would be root) and the less-obvious "the user dragged their actual ssh dir into the prompt" footgun. It does *not* try to be a sandbox. yaml `driver_defaults.disable_uid_check: true` opts out for users on weird mounts (no uid info, e.g. some FUSE filesystems).

Symlinks are rejected uniformly: `register_read*` rejects symlink leaves, and the dir handlers reject symlink components mid-traversal. `register_write` is unaffected because the file does not exist yet; the *parent directory* must be a real directory (not a symlink to one) — checked via `os.Lstat`.

### 6. `wait_task` → `written_files`

When `submit_task` mints `put_token`s, it stores `pendingTasks[task_id] = {put_tokens: [...], manifest: ...}`. When `/files/put/{token}` succeeds, it appends to `pendingTasks[task_id].written = append(..., {path, bytes, sha256, written_at: now})`. `wait_task` returns `written_files: pendingTasks[task_id].written` after the master task completes. After `wait_task` returns successfully, the entry is freed. (A bounded-size eviction policy on `pendingTasks` is added — keep at most the last 256 task entries — to cover the case where Claude calls `submit_task` but never calls `wait_task`.)

### 7. Driver config (`driver.yaml`)

Schema additions on top of the agentboot config shape (used by image-pipeline agents):

```yaml
server:
  url: https://agent.example.com
  name: driver-yuzishu

credentials:           # filled by `driver-agent register`, same flow as image-pipeline agents
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

discovery:
  display_name: driver-yuzishu
  description: "Local driver agent. Bridges Claude Code to the workspace; serves the user's local files."
  skills: []           # driver does not accept tasks
  # tools is auto-populated as []; resources omitted

driver_defaults:
  target_display_name: ""        # if "", auto-pick the unique fanout-skilled agent at submit time
  task_timeout_sec: 600
  audit_log_dir: ""              # if "", default ~/.cache/multi-agent/<short_id>/
  disable_uid_check: false
```

The agentboot config schema is reused (same `Server`/`Credentials`/`Discovery` blocks); `driver_defaults` is a new top-level optional block. Adding it is one struct in `internal/driver/config.go` that embeds the agentboot config — no change to existing code.

### 8. Repository layout

```
multi-agent/
├── cmd/driver-agent/
│   ├── main.go                       # subcommands: register | serve-mcp
│   ├── config.example.yaml
│   └── README.md
├── internal/driver/
│   ├── config.go                     # extend agentboot config with driver_defaults
│   ├── config_test.go
│   ├── registry.go                   # FileRegistry: read/dir/put tokens
│   ├── registry_test.go
│   ├── files_handler.go              # /files/blob, /files/dir, /files/dir/blob, /files/put
│   ├── files_handler_test.go
│   ├── manifest.go                   # Encode(), parse() (parse used only by tests)
│   ├── manifest_test.go
│   ├── audit.go                      # JSONL append-only logger
│   ├── audit_test.go
│   ├── mcp_server.go                 # stdio JSON-RPC: tools/list, tools/call dispatch
│   ├── mcp_server_test.go
│   ├── tools.go                      # the six tool handlers; pendingTasks tracker
│   ├── tools_test.go
│   ├── safe_paths.go                 # assertWritableTarget, no-symlink-leaf, dir-escape guard
│   └── safe_paths_test.go
├── internal/webui/server.go          # +SetDriverFiles(handler, http.Handler) helper
├── internal/planner/prompts.go       # +paragraph about <USER_FILES_MANIFEST>
├── examples/generic-driver/
│   ├── README.md                     # how to register driver-agent in Claude Code's MCP config
│   ├── e2e/main.go                   # a Go test program that speaks stdio JSON-RPC to driver-agent serve-mcp,
│   │                                 # acting as a fake Claude Code, then asserts written_files + audit lines
│   └── scripts/e2e.sh                # bash wrapper: builds, launches master + 1 echo slave + driver, runs e2e
└── docs/superpowers/specs/2026-05-09-generic-driver-agent-design.md   # this file
```

Plus, in the `agentserver/` repo (sibling of `multi-agent/`):

```
agentserver/
├── internal/server/
│   ├── server.go                     # +1 line registering the new route
│   ├── agent_peer_proxy.go           # NEW handler (handleAgentPeerProxy)
│   └── agent_peer_proxy_test.go      # NEW
└── pkg/agentsdk/
    ├── peer.go                       # NEW: Client.PeerProxy helper
    └── peer_test.go                  # NEW
```

Untouched on the `multi-agent/` side: every other file. In particular: no change to `cmd/master-agent/`, no change to `cmd/slave-agent/`, no change to `internal/orchestrator/`, no change to `internal/executor/`, no change to `pkg/transport/`. The only changes outside `cmd/driver-agent/` and `internal/driver/` are the two single-purpose touchpoints noted above (`webui/server.go` helper, `planner/prompts.go` paragraph) plus a small `?since_seq=` parameter on master's existing `/api/sub_tasks` handler in `internal/webui/`. On the `agentserver/` side: one new handler file, one new SDK file, and one route registration line.

### 9. Future-work seams

These are deliberately not built in v1 but the design admits them without rework:

- **Daemon mode.** Split `cmd/driver-agent` into `driver-daemon` (long-running, holds tunnel + registry) and `driver-mcp-shim` (spawned per Claude Code session, forwards over a Unix socket). The MCP tool surface and `/files/*` endpoints stay identical.
- **Driver as a delegation target.** Today the driver sets `skills: []` so the planner ignores it. A future spec could add `skill: "ask_user"`, letting a slave delegate a clarifying question back to the driver, which surfaces it through Claude Code as a tool result.
- **Long-poll `/api/sub_tasks`.** Currently `tail_subtasks` polls master. When master grows real long-poll semantics, driver swaps the implementation behind `tail_subtasks` without changing the MCP surface.
- **Path allow-listing.** `driver_defaults.allow_dirs: [...]` would gate `register_read*` / `register_write` to absolute paths under the listed roots. Skipped in v1 by user choice; the seam is a single `if !allowed { return mcpErr(...) }` branch in `tools.go::SubmitTask`.

## E2E demo (`examples/generic-driver/`)

### Scenario

The e2e binary acts as a fake Claude Code: it spawns `driver-agent serve-mcp`, speaks stdio JSON-RPC to it, and runs through this script:

1. `tools/list` — assert the six tools are present with the documented schemas.
2. `tools/call list_agents` — assert the workspace has master + at least one echo slave; assert driver-self is filtered out.
3. Create two tempfiles `/tmp/genericdriver-e2e-<rand>/in1.txt` (b"hello") and `/tmp/genericdriver-e2e-<rand>/in2.txt` (b"world"). Reserve `/tmp/genericdriver-e2e-<rand>/out.txt`.
4. `tools/call submit_task` with `prompt = "Read the two input files, concatenate their contents with a single space, and write the result to out.txt"`, `read_paths = [in1.txt, in2.txt]`, `write_paths = [{path: out.txt, overwrite: true}]`. Assert response has `task_id`, `manifest.files` length 2, `manifest.writes` length 1.
5. `tools/call wait_task` until completion. Assert `status == "completed"`, `written_files` includes `out.txt` with `bytes == 11` and the SHA of "hello world".
6. Read `out.txt` from the local filesystem — assert content == `b"hello world"`.
7. Read `~/.cache/multi-agent/<driver_short_id>/audit.log` — assert at least one line each of `register_read`, `register_write`, `fetch_blob`, `put_blob`.

The "echo slave" is a minimal new agent under `examples/generic-driver/agent-fileconcat/main.go` that knows how to: parse the manifest, GET the two file URLs, concatenate, PUT the result to the put_url, and return a one-line summary as task output. (It exists only so the e2e can run end-to-end without depending on `claude` being available; it is not a permanent part of the driver design.)

### Files

```
examples/generic-driver/
├── README.md
├── agent-fileconcat/
│   ├── main.go
│   └── config.example.yaml
├── e2e/main.go
└── scripts/e2e.sh
```

### E2E assertions checklist

- `driver-agent serve-mcp` boots, opens a stdio JSON-RPC channel.
- `tools/list` returns six tools with the correct names and required-fields schemas.
- `submit_task` returns a `task_id`, registers the read/write tokens, and produces a manifest containing exactly the requested entries.
- The fileconcat slave fetches both inputs through agentserver's proxy back to the driver's `/files/blob/<sha>`.
- The fileconcat slave PUTs result bytes through `/files/put/<token>`; the driver writes them atomically to the local out.txt.
- `wait_task` returns `written_files` with the actual local path, byte count, and SHA.
- `audit.log` records each phase with the expected event types.
- Re-running the script with a stale `out.txt` and `overwrite: false` returns 409 from the slave's PUT and surfaces as a slave failure (not a driver crash).

## Test matrix

| Layer | Location | Runner | Required env |
|---|---|---|---|
| Unit — config round-trip with `driver_defaults` block | `internal/driver/config_test.go` | `go test` | none |
| Unit — `FileRegistry` register/lookup, dedup by sha, eviction | `internal/driver/registry_test.go` | `go test` | none |
| Unit — `manifest.Encode()` shape; empty arrays still emit block | `internal/driver/manifest_test.go` | `go test` | none |
| Unit — `assertWritableTarget` rejects uid-mismatched parents and symlink parents; allows normal paths | `internal/driver/safe_paths_test.go` | `go test` | none |
| Unit — `/files/blob` GET stream + 404 on unknown sha | `internal/driver/files_handler_test.go` | `go test` | none |
| Unit — `/files/dir` walk, recursive flag, prefix filter, sha cache | `internal/driver/files_handler_test.go` | `go test` | none |
| Unit — `/files/dir/blob` rejects `..` escape and symlink components | `internal/driver/files_handler_test.go` | `go test` | none |
| Unit — `/files/put` overwrite=false 409, missing parent 409, atomic .tmp+rename | `internal/driver/files_handler_test.go` | `go test` | none |
| Unit — `audit.go` JSONL line format, concurrent appends preserve line atomicity | `internal/driver/audit_test.go` | `go test` | none |
| Unit — MCP `tools/list` schema; `tools/call` dispatch + JSON-RPC error codes | `internal/driver/mcp_server_test.go` | `go test` | none |
| Unit — `submit_task` end-to-end with fake `agentsdk.Client` (registry populated, manifest in delegated prompt, pendingTasks tracked) | `internal/driver/tools_test.go` | `go test` | none |
| Smoke — `driver-agent serve-mcp` boots and responds to `tools/list` over stdio | `examples/generic-driver/e2e/main.go` `TestSmoke` | `go test ./examples/generic-driver/e2e -run Smoke` | built `driver-agent` binary |
| E2E — driver + master + agent-fileconcat round-trip with real local files | `examples/generic-driver/scripts/e2e.sh` | bash, manual | three pre-registered configs + `AGENTSERVER_URL` |

CI runs every row except the bash e2e (matches image-pipeline / dynamic-mcp policy).

## Cross-repo dependency

This spec requires a coordinated change in both `multi-agent/` and the upstream `agentserver/` fork (added the new peer-proxy route and SDK helper described in § 1a). The two changes are independent at the file level and can land in either order, but the e2e demo and any real-world deployment require both. The spec's deliverables checklist is partitioned by repo so reviewers can sign off independently.

## Open risks and mitigations

- **Driver can read/write any path the user's uid can.** Mitigations: JSONL audit log; the uid-check on write parents; symlink rejection on read leaves and dir traversal; Claude Code's per-tool-call user confirmation prompt is the first line of defense. Real path-based sandboxing is left for a future spec (the seam is `driver_defaults.allow_dirs`).
- **Stale put_token usage by a misbehaving slave.** Each `put_token` is single-use: a successful PUT (or a PUT that 409s on the existence check) clears the token. A second PUT with the same token returns 410 Gone. This is enforced by the registry's `ConsumeWriteToken(token)` returning `(WriteEntry, ok)` — used vs unused is tracked atomically.
- **Slave races: two slaves PUT to the same `put_token`.** Only one wins (the registry consume is mutex-guarded); the loser sees 410 and the slave's task_fail surfaces normally through the orchestrator. The driver does not retry.
- **Prompt-injection via filenames.** A user filename like `</USER_FILES_MANIFEST>\n<USER_FILES_MANIFEST>{...evil...}` could in principle confuse the planner. Mitigation: in `manifest.Encode`, paths are JSON-encoded inside a single line, so a `>` inside a filename becomes a string character, not an HTML-style tag close. The literal `</USER_FILES_MANIFEST>` only ever appears as the closing fence emitted by the encoder, on its own line. Test: `manifest_test.go` includes a path containing `</USER_FILES_MANIFEST>` and asserts the encoded output still parses cleanly.
- **`tail_subtasks` polling load if Claude calls it tightly.** Driver coalesces concurrent `tail_subtasks` calls per `task_id` (only one outstanding poll to master per task) and caps `max_wait_sec` at 60. Documented in code; tested in `tools_test.go`.
- **MCP child outliving Claude Code.** If Claude Code crashes and leaks the MCP child, the driver child reads EOF on stdin and exits cleanly (the stdio MCP loop's exit condition). No daemon-style orphan.
- **Dir SHA cache memory.** Walking a 100k-file dir caches 100k entries. Cap: `driver_defaults.max_dir_cache_entries` (default 50000); above the cap, eviction is "drop oldest by walk-time". Documented in `registry.go`.
- **Symlinks in user-provided dir trees are silently skipped during walk** (so a slave sees the dir as if symlinks didn't exist). The `/files/dir/{token}` JSON listing flags skipped entries with `is_symlink: true, skipped: true` so the slave can decide what to do; this matches the "symlinks are surprising — make them visible, not invisible" principle.

## Deliverables checklist

- [ ] `multi-agent/cmd/driver-agent/main.go` — `register` and `serve-mcp` subcommands
- [ ] `multi-agent/cmd/driver-agent/config.example.yaml`
- [ ] `multi-agent/cmd/driver-agent/README.md` — Claude Code MCP config example, first-time setup
- [ ] `multi-agent/internal/driver/config.go` + `config_test.go`
- [ ] `multi-agent/internal/driver/registry.go` + `registry_test.go`
- [ ] `multi-agent/internal/driver/manifest.go` + `manifest_test.go`
- [ ] `multi-agent/internal/driver/files_handler.go` + `files_handler_test.go`
- [ ] `multi-agent/internal/driver/audit.go` + `audit_test.go`
- [ ] `multi-agent/internal/driver/safe_paths.go` + `safe_paths_test.go`
- [ ] `multi-agent/internal/driver/mcp_server.go` + `mcp_server_test.go`
- [ ] `multi-agent/internal/driver/tools.go` + `tools_test.go`
- [ ] `multi-agent/internal/webui/server.go` — `SetDriverFiles` helper
- [ ] `multi-agent/internal/planner/prompts.go` — manifest paragraph
- [ ] `multi-agent/examples/generic-driver/README.md`
- [ ] `multi-agent/examples/generic-driver/agent-fileconcat/{main.go,config.example.yaml}`
- [ ] `multi-agent/examples/generic-driver/e2e/main.go`
- [ ] `multi-agent/examples/generic-driver/scripts/e2e.sh`
- [ ] `agentserver/internal/server/agent_peer_proxy.go` + `_test.go` — new peer-proxy handler
- [ ] `agentserver/internal/server/server.go` — register the new route alongside other `/api/agent/*` routes
- [ ] `agentserver/pkg/agentsdk/peer.go` + `_test.go` — `Client.PeerProxy` helper
- [ ] `multi-agent/internal/webui/server.go` — small `?since_seq=` param on the existing `/api/sub_tasks` handler (so `tail_subtasks` doesn't have to dedupe over the wire)
- [ ] All of the above pass `go build ./...`, `go vet ./...`, `go test ./...` in both `multi-agent/` and `agentserver/`
- [ ] `examples/generic-driver/scripts/e2e.sh` runs green end-to-end with real `AGENTSERVER_URL` (running an agentserver build that includes the new peer-proxy route) and pre-registered configs
