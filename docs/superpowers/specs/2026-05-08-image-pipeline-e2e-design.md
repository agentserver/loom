# Image-Pipeline End-to-End Test — Design

**Date:** 2026-05-08
**Status:** Draft (awaiting user review)
**Scope:** Add a live end-to-end test that exercises `master-agent` + two custom workspace agents (built directly on `agentserver/pkg/agentsdk`, not on `cmd/slave-agent`) against a real `agentserver` with real `claude` running as the master's planner/reducer. The pipeline captures an image on agent A, then compresses it on agent B, returning a final URL/byte-count to the master's reducer. Along the way, formalize a small `pkg/transport` library so that user-written sub-task agents have a documented, reusable way to pass large/binary artifacts between nodes without inflating prompts.

## Motivation

The current framework auto-flows sub-task outputs into downstream prompts via the `{{nX.output}}` template (see `internal/orchestrator/fanout.go:127-129` and `internal/orchestrator/dag.go:204 Render`). This works for short text but breaks down when sub-task output is binary or large: stuffing a base64 PNG into the next prompt explodes token usage and forces the intermediate slave's claude to act as a byte-shuffling middleman.

We do **not** want to push transport into the framework — `multi-agent/internal/*` should remain transport-agnostic, with `{{nX.output}}` as a string-substitution default. Instead we add a side-channel pattern: the upstream node returns a **handle JSON** (small, e.g. `{"type":"image_url","url":"http://...","bytes":51234}`), the framework substitutes that JSON into the downstream prompt as usual, and the downstream tool dereferences via the URL. This keeps prompts small, leaves bytes out of the LLM round-trip, and lets users plug in any transport (HTTP, shared filesystem, S3, custom) without framework changes.

This work delivers:

1. A `pkg/transport` package with a `Transport` interface and two reference implementations (`http`, `sharedfs`). Not imported by `internal/*`.
2. A documented "handle JSON" output convention.
3. Two reference custom agents (`agent-image-capture`, `agent-image-compress`) under `examples/image-pipeline/` that use `pkg/transport/http` and the agentserver SDK directly (no claude, no MCP) to demonstrate the pattern.
4. A bash e2e script that wires master + 2 slaves + agentserver + claude end-to-end and asserts pipeline success.

## Non-goals

- No changes to `internal/orchestrator`, `internal/planner`, `internal/dispatch`, `internal/executor`, `internal/store`, or any other framework internal.
- No new `skill` field on planner nodes; no orchestrator changes to thread skills through `DelegateTask`.
- The two image agents are **not** based on `cmd/slave-agent` and do **not** use MCP or claude internally. They are independent `agentsdk` clients. This deliberate choice demonstrates that `master-agent` orchestrates **any** workspace agent, not only sibling slave-agents — keeping `multi-agent` honestly framework-shaped. (Discussion of why we don't reuse `slave-agent` here: the slave's claude executor does not propagate `mcp_servers` to the `claude` CLI invocation, so a "claude-on-slave calls MCP tool" path would require modifying `internal/executor/claude.go`, which is out of scope.)
- No authentication, GC, or quota for the reference HTTP transport — it's testdata-grade.
- No real-camera capture; the capture agent synthesizes a deterministic noise PNG.

## Architecture

### Data flow (happy path)

```
[driver] --DelegateTask({target=master, skill:fanout, prompt:"capture then compress"})--> [master-agent]
                                                                     |
                                          master.planner (real claude) emits DAG:
                                          [{id:n1, target_id:agent-image-capture,  prompt:"capture a 256x256 image",      depends_on:[]},
                                           {id:n2, target_id:agent-image-compress, prompt:"compress {{n1.output}} at q=50", depends_on:[n1]}]
                                                                     |
                                          DelegateTask(n1) -> agent-image-capture (custom agentsdk Task handler)
                                              -> synthesize 256x256 PNG via image/png
                                              -> httpx.Put(image/png, bytes) -> Handle{type:image_url, url:http://A:7001/blobs/<id>, mime:image/png, bytes:N}
                                              -> task.Complete(Output: Handle.Marshal())  // string of handle JSON
                                                                     |
                                          orchestrator stores outputs[n1] = "<handle JSON string>"
                                          orchestrator Renders n2.prompt: substitutes {{n1.output}} with full handle JSON string
                                                                     |
                                          DelegateTask(n2) -> agent-image-compress (custom agentsdk Task handler)
                                              -> regex first https?:// URL out of task.Prompt
                                              -> http.Get url -> image.Decode -> jpeg.Encode(quality=50)
                                              -> httpx.Put(image/jpeg, bytes) -> Handle{type:image_url, mime:image/jpeg, bytes:M, meta:{original_bytes:N, ratio:M/N}}
                                              -> task.Complete(Output: Handle.Marshal())
                                                                     |
                                          master.reducer (real claude) summarizes:
                                          "Captured 51234-byte PNG, compressed to 20100 bytes (ratio 0.39). Final URL: http://B:7002/blobs/<id>"
                                                                     |
[driver] <--WaitForTask({task_id})-- final summary (TaskInfo.Output)
```

### Repository layout (new directories only)

```
multi-agent/
├── pkg/
│   └── transport/
│       ├── transport.go               # Handle + Transport interface + Marshal/ParseHandle
│       ├── transport_test.go
│       ├── http/
│       │   ├── http.go                # in-process HTTP server, Put/Get/Close
│       │   └── http_test.go
│       └── sharedfs/
│           ├── sharedfs.go            # local-FS Put/Get
│           └── sharedfs_test.go
├── examples/
│   └── image-pipeline/
│       ├── README.md                  # explains handle JSON convention + how to run e2e
│       ├── internal/
│       │   ├── agentboot/
│       │   │   └── agentboot.go       # shared registration + card publish + Connect helper
│       │   │                          # used by both image agents (DRY)
│       │   ├── imageops/
│       │   │   ├── imageops.go        # SynthPNG(width,height,seed) and EncodeJPEG(reader,quality)
│       │   │   └── imageops_test.go
│       │   └── handlepick/
│       │       ├── handlepick.go      # FirstURL(prompt) regex helper for compress agent
│       │       └── handlepick_test.go
│       ├── agent-image-capture/
│       │   ├── main.go                # agentsdk Task handler: SynthPNG -> httpx.Put -> Complete(handle JSON)
│       │   ├── main_test.go           # spins up agentboot + httpx, drives the handler with a fake Task
│       │   └── config.example.yaml
│       ├── agent-image-compress/
│       │   ├── main.go                # agentsdk Task handler: FirstURL -> http.Get -> EncodeJPEG -> httpx.Put -> Complete
│       │   ├── main_test.go
│       │   └── config.example.yaml
│       ├── e2e-driver/
│       │   └── main.go                # discovers master via agentsdk, DelegateTask(skill=fanout),
│       │                              #   WaitForTask, asserts reducer output + downloads final URL,
│       │                              #   optionally inspects master/data.db sub_tasks via sqlite
│       └── scripts/
│           └── e2e.sh                 # builds, launches master + 2 image agents, runs driver, asserts
└── docs/superpowers/specs/2026-05-08-image-pipeline-e2e-design.md   # this file
```

Untouched: every existing file under `multi-agent/internal/`, `multi-agent/cmd/`, and `multi-agent/tests/`.

## `pkg/transport` design

### Handle and Transport types

```go
// pkg/transport/transport.go
package transport

import (
    "context"
    "encoding/json"
    "io"
)

// Handle is the small JSON-serializable descriptor that travels through the
// {{nX.output}} template path. The bytes themselves move via the side channel
// referenced by URL.
type Handle struct {
    Type  string            `json:"type"`            // caller-defined: image_url, blob_url, ...
    URL   string            `json:"url"`             // dereferencing locator
    Bytes int64             `json:"bytes,omitempty"` // size hint
    MIME  string            `json:"mime,omitempty"`  // e.g. image/png
    Meta  map[string]string `json:"meta,omitempty"`  // free-form, e.g. {"original_bytes":"51234","ratio":"0.39"}
}

// Marshal returns the canonical one-line JSON form. Always succeeds.
func (h Handle) Marshal() string {
    b, _ := json.Marshal(h)
    return string(b)
}

// ParseHandle attempts to interpret s as a Handle JSON document. Returns
// (zero, false) if s is not JSON or lacks the required Type/URL fields, so
// callers can transparently fall back to treating s as plain text.
func ParseHandle(s string) (Handle, bool) {
    var h Handle
    if err := json.Unmarshal([]byte(s), &h); err != nil {
        return Handle{}, false
    }
    if h.Type == "" || h.URL == "" {
        return Handle{}, false
    }
    return h, true
}

// Transport stores and retrieves opaque byte payloads. Producers Put bytes and
// receive a Handle; consumers Get bytes from a Handle.
type Transport interface {
    Put(ctx context.Context, mime string, data io.Reader) (Handle, error)
    Get(ctx context.Context, h Handle) (io.ReadCloser, error)
    io.Closer
}
```

The `Type` field is set by the caller after `Put`, since it's a semantic tag (transport doesn't know whether the bytes are an image, a PDF, or anything else):

```go
h, _ := tr.Put(ctx, "image/png", bytes.NewReader(data))
h.Type = "image_url"
return h.Marshal()
```

### `pkg/transport/http`

```go
// pkg/transport/http/http.go
package httpx

type Options struct {
    Addr      string // bind addr, default "127.0.0.1:0" (random free port)
    PublicURL string // override URL prefix (default "http://" + listener.Addr().String())
    Dir       string // optional: persist blobs to disk; empty = in-memory map
}

type Server struct{ /* listener, addr, store */ }

func New(opts Options) (*Server, error) // listens immediately
func (s *Server) Addr() string          // host:port actually bound
func (s *Server) Put(ctx context.Context, mime string, data io.Reader) (transport.Handle, error)
func (s *Server) Get(ctx context.Context, h transport.Handle) (io.ReadCloser, error)
func (s *Server) Close() error
```

- HTTP routes: `GET /blobs/{id}` returns the bytes with the recorded `Content-Type`; `HEAD /blobs/{id}` returns size only.
- Blob ID: first 16 hex chars of `sha256(data)` (content-addressed, automatic dedupe).
- URL pattern: `<PublicURL>/blobs/<id>`, e.g. `http://127.0.0.1:54321/blobs/9f86d081884c7d65`.
- No auth, no TTL — the process owns its state and dies when the agent process exits.

### `pkg/transport/sharedfs`

```go
// pkg/transport/sharedfs/sharedfs.go
package sharedfs

type FS struct{ dir string }

func New(dir string) (*FS, error) // mkdir -p dir
func (f *FS) Put(ctx context.Context, mime string, data io.Reader) (transport.Handle, error)
func (f *FS) Get(ctx context.Context, h transport.Handle) (io.ReadCloser, error)
func (f *FS) Close() error // no-op
```

- URL form: `file:///abs/path/to/dir/<id>`.
- Same content-addressed ID scheme as the HTTP impl.
- `Get` strips the `file://` prefix and `os.Open`s the path. Rejects URLs whose absolute path falls outside `dir`.
- Included to demonstrate that `Transport` is genuinely substitutable, not HTTP-specific.

### Tests for `pkg/transport`

`pkg/transport/transport_test.go`:
- `Handle.Marshal` then `ParseHandle` round-trips for: minimal handle, handle with Meta, handle with all fields.
- `ParseHandle` returns `false` for: empty string, plain text, JSON missing `type`, JSON missing `url`, malformed JSON.

`pkg/transport/http/http_test.go`:
- Put then Get round-trips arbitrary bytes; Content-Type matches the MIME passed to Put.
- Putting the same bytes twice returns the same Handle.URL (dedupe).
- Get on a never-Put handle returns a non-nil error (404).
- Close stops the server: subsequent Get returns a connection error and the port is released.
- Concurrency: 100 parallel Puts of distinct payloads succeed under `-race`, all Handles resolve.
- `Options.PublicURL` overrides the URL prefix (Addr unchanged).

`pkg/transport/sharedfs/sharedfs_test.go`:
- Put then Get round-trips bytes; file appears under `dir/<id>`.
- Putting same bytes twice returns same handle; file content unchanged.
- Get on a `file://` URL outside `dir` returns an error (path traversal guard).
- Get on missing handle returns `os.ErrNotExist`-flavored error.

## Reference custom agents

Both binaries are independent agentsdk clients. Each:
1. Loads YAML config (server URL, name, credentials, optional listen-addr).
2. If credentials are missing, runs `RequestDeviceCode` + `PollForToken` + `Register`, prints the verification URL, writes credentials back to the config. Otherwise calls `SetRegistration`.
3. POSTs its discovery card to `/api/agent/discovery/cards` (copying the helper from `multi-agent/internal/tunnel/tunnel.go:81`) so the master's planner can see it.
4. Starts an in-process `httpx.Server` on `127.0.0.1:0` (random port) for transport.
5. Calls `cli.Connect(ctx, agentsdk.Handlers{Task: handler})` to enter the task poll loop.

Step 1-3 + 5 are nearly identical between the two agents — that boilerplate lives in `examples/image-pipeline/internal/agentboot/agentboot.go`. Each agent's `main.go` is essentially: parse flags, call `agentboot.Run(ctx, cfg, taskHandler)`.

### `agent-image-capture`

| Field | Value |
|---|---|
| Discovery `display_name` | `image-capture` |
| Discovery `description` | "Image capture agent. Always returns a JSON handle string `{type:image_url, url:..., mime:image/png, bytes:N}` pointing at a freshly generated 256x256 PNG. Ignores task prompt content." |
| Discovery `skills` | `["capture"]` (purely informational — agent ignores skill on the incoming task) |
| Task handler | Calls `imageops.SynthPNG(256,256,42)` → `httpx.Put(ctx,"image/png",reader)` → `task.Complete(ctx, TaskResult{Output: handle.WithType("image_url").Marshal()})`. |
| Stderr boot line | `LISTEN <host:port>` (so the e2e script can log it). |
| Failure modes | `httpx.Put` failure → `task.Fail(ctx, err.Error())`. |

### `agent-image-compress`

| Field | Value |
|---|---|
| Discovery `display_name` | `image-compress` |
| Discovery `description` | "Image compress agent. Reads the first https?:// URL it finds in the task prompt, downloads the image, re-encodes as JPEG (quality=50 default; override via `quality=N` substring in prompt). Returns a JSON handle string `{type:image_url, url:..., mime:image/jpeg, bytes:M, meta:{original_bytes:N, ratio:M/N}}`." |
| Discovery `skills` | `["compress"]` |
| Task handler | `handlepick.FirstURL(task.Prompt)` → `http.Get` → `image.Decode` → `imageops.EncodeJPEG(image, quality)` → `httpx.Put(ctx,"image/jpeg",reader)` → `task.Complete`. |
| Stderr boot line | `LISTEN <host:port>`. |
| Failure modes | no URL in prompt → `task.Fail("no URL in prompt")`; GET fails or non-2xx → `task.Fail`; decode fails → `task.Fail`. |

### Tests for the custom agents

`internal/imageops/imageops_test.go`:
- `SynthPNG(64,64,42)` produces bytes that `image.Decode` decodes back to a 64x64 image; same seed → identical bytes.
- `EncodeJPEG(decoded image, 50)` produces bytes that `image.Decode` decodes; output strictly smaller than the input PNG bytes for the noise image.

`internal/handlepick/handlepick_test.go`:
- `FirstURL("Compress {\"type\":\"image_url\",\"url\":\"http://x/y\",...}")` → `http://x/y`
- `FirstURL("no urls here")` → `("", false)`
- Doesn't get tripped up by trailing punctuation (`http://x/y."` returns `http://x/y`).

`agent-image-capture/main_test.go`:
- Build a fake `*agentsdk.Task` (we cannot construct it directly because `proxyToken` and `serverURL` are unexported — instead, factor the actual work into a helper `runCapture(ctx, hx *httpx.Server) (string, error)` and test that helper). Asserts: returned string parses as Handle JSON; MIME is `image/png`; downloading the URL via `hx.Get` round-trips the bytes.

`agent-image-compress/main_test.go`:
- Spin up an `httptest.Server` that serves a known PNG. Construct a `runCompress(ctx, hx, prompt) (string, error)` helper. Pass a prompt containing the test server URL. Assert: returned string parses as Handle JSON; MIME is `image/jpeg`; bytes < input bytes; `hx.Get` round-trips a valid JPEG (decode succeeds).
- Call helper with prompt missing URL → returns error.

## Agent configurations

The e2e script materializes these into the work dir; `config.example.yaml` lives under each agent dir for documentation.

The two image agents share a tiny config shape (much smaller than the full slave-agent config because there are no executors, no journal, no webui):

```yaml
server:
  url: ${AGENTSERVER_URL}
  name: <agent-name>      # used for registration; e.g. agent-image-capture-e2e
credentials:
  sandbox_id: ""          # filled by device-flow on first run
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""
discovery:
  display_name: <image-capture|image-compress>
  description: |
    <see Reference custom agents section above>
  skills: [<capture|compress>]
listen_addr: 127.0.0.1:0  # for the in-process httpx.Server
```

### master

```yaml
server: { url: ${AGENTSERVER_URL}, name: master-e2e-image }
claude: { bin: claude }
planner: { bin: claude, timeout_sec: 60 }
fanout:
  max_concurrency: 2
  default_policy: all_or_nothing
  subtask_defaults: { timeout_sec: 300 }
discovery:
  display_name: master-e2e-image
  description: Orchestrator for image-pipeline e2e
  skills: [route, fanout]
```

Note: `default_policy: all_or_nothing` so a capture failure aborts compress immediately rather than dispatching n2 against missing data.

## Master task prompt and planner behavior

The `e2e-driver` (described in the harness section below) submits the task by calling `agentsdk.Client.DelegateTask` with `TargetID` set to the master's discovered `AgentID`, `Skill: "fanout"`, and `Prompt` set to:

```
Capture a 256x256 image using the image-capture agent, then pass its output URL to
the image-compress agent at quality=50. Sub-task n2 MUST reference n1's output via
{{n1.output}} so my orchestrator can substitute it. Final answer should include the
final compressed image URL and the byte size.
```

Two things this prompt must do for real claude as planner:
1. Make the topology unambiguous (capture first, compress depends on capture).
2. Force `{{n1.output}}` to appear literally in n2's prompt, since that's what the orchestrator's `Render` looks for at substitution time.

The existing `internal/planner/prompts.go:planPrompt` already documents the `{{X.output}}` template syntax and `depends_on` semantics, so the planner has the context it needs; the master prompt above adds the application-specific instruction.

### Fallback for flaky planner output

If real claude's plan output is malformed or omits the template (rare but possible), the script supports a `USE_FAKE_PLANNER=1` env switch that rewrites the master config to point `planner.bin` at `multi-agent/testdata/fake-planner.sh` with `FAKE_PLANNER_MODE=plan_chain`. The chain mode emits a hard-coded 2-node DAG with `{{a.output}}` template — but its `target_id` values are `agent-a/agent-b`, which won't match real registered agent IDs; using the fallback also requires patching the script to substitute the discovered agent IDs into a small inline planner instead. Documented but not the default path.

## End-to-end harness

The harness has two pieces:

1. `multi-agent/examples/image-pipeline/e2e-driver/main.go` — a Go binary that uses `agentserver/pkg/agentsdk` to find the master, submit a task, wait for the result, and run assertions.
2. `multi-agent/examples/image-pipeline/scripts/e2e.sh` — a bash wrapper that compiles binaries, launches master + 2 slave agent processes against agentserver, waits for them to register, then runs the driver, then tears down.

### Why a Go driver instead of pure curl

The agent-side HTTP API requires a `Bearer <proxy_token>` header from a registered agent. There is no plaintext local POST endpoint on the master (the master's webui is exposed only through agentserver's tunnel, and `/tasks` is GET-only). The clean way to submit a task is to act as a registered agent and call `agentsdk.Client.DelegateTask`. A Go binary that imports the existing SDK is half the code and far less brittle than reproducing the auth/poll/wait flow in bash.

### `e2e-driver` behavior

Reads from CLI flags or env:
- `--config` (path to a yaml with the driver's own agent registration credentials — same minimal shape as the image agents, no listen_addr or discovery needed)
- `--target-display-name` (default `master-e2e-image`) — what to look for in DiscoverAgents
- `--prompt` (the master task prompt above)
- `--skill` (default `fanout`)
- `--timeout` (default `180s`)

Steps inside the driver:

1. Load credentials, construct `agentsdk.Client`, `cli.SetRegistration(...)`.
2. Loop `cli.DiscoverAgents(ctx)` until an agent with `display_name == target_display_name` and `status == "available"` is present, or 60s elapses. Same loop also asserts the two slaves (`image-capture`, `image-compress`) are visible.
3. `resp, err := cli.DelegateTask(ctx, DelegateTaskRequest{TargetID: master.AgentID, Skill: "fanout", Prompt: prompt, TimeoutSeconds: 300})`.
4. `info, err := cli.WaitForTask(ctx, resp.TaskID, 2*time.Second)` — polls until terminal.
5. Assertions on `info`:
   - `info.Status == "completed"` (else: print FailureReason and exit 1).
   - `info.Output` (the reducer's text summary) is non-empty and contains a substring matching `http://[^ ]+\.(jpg|jpeg)` OR `http://[^ ]+/blobs/[0-9a-f]+` plus the substring `image/jpeg`.
6. Extract the final URL via regex from `info.Output`, then `http.Get` it. Assert: 2xx status, `Content-Type: image/jpeg`, body decodes via `jpeg.Decode` without error.
7. Print a one-line summary: `OK e2e: original=<n1.bytes> compressed=<final-bytes> ratio=<r> url=<final-url>`.

Where do `n1.bytes` / `final-bytes` come from? `info.Output` is the reducer's free-form text; the master prompt asks the reducer to include the byte sizes, but real claude may format them inconsistently. So the driver:
- Tries to parse "original=N" / "compressed=N" / "ratio=R" out of the reducer text via regex; if any are missing, it falls back to: download the final JPEG, use `Content-Length` as compressed bytes, and report `original=unknown`.
- This is a soft assertion; the hard pass criterion is steps 5 + 6 (status completed, URL present, valid JPEG).

For deeper inspection (raw n1 output, sub_task statuses), the driver optionally accepts `--master-data-db <path>`; if set, after waiting it opens the SQLite file and reads `sub_tasks WHERE parent_id = task_id`, asserts both rows are `completed`, parses each `output` as Handle JSON, asserts `n1.mime == image/png`, `n2.mime == image/jpeg`, `n2.bytes < n1.bytes`. The bash wrapper passes this flag pointing at `$work/master/data.db`.

### `e2e.sh` orchestration

Preconditions (asserted in script header):
- `AGENTSERVER_URL` set and reachable.
- `ANTHROPIC_API_KEY` set.
- `claude`, `go`, `sqlite3` on PATH.
- Pre-existing config files (with credentials filled in via prior interactive registration) at paths supplied by env vars: `MASTER_CONFIG`, `CAPTURE_CONFIG`, `COMPRESS_CONFIG`, `DRIVER_CONFIG`.

Steps:

1. `work=$(mktemp -d)`. Trap on EXIT: kill all spawned PIDs, `rm -rf "$work"`.
2. From module root, `go build` four binaries into `$work/bin/`:
   - `./cmd/master-agent`
   - `./examples/image-pipeline/agent-image-capture`
   - `./examples/image-pipeline/agent-image-compress`
   - `./examples/image-pipeline/e2e-driver`
3. Copy each of the four config files into per-agent subdirs of `$work` (so each agent has its own cwd for `data.db`, etc.).
4. Launch the three long-running agents in order, each backgrounded with stdout+stderr to `$work/<name>.log`:
   - `agent-image-capture` (cwd=`$work/capture`)
   - `agent-image-compress` (cwd=`$work/compress`)
   - `master-agent` (cwd=`$work/master`)
5. Readiness: there is no "card published" success log. Instead, the e2e driver (run in step 6) polls `agentsdk.DiscoverAgents` until it sees all three target display names (`master-e2e-image`, `image-capture`, `image-compress`) in the response, with a 60s ceiling. This is the real readiness check.
6. Run the driver: `$work/bin/e2e-driver --config $DRIVER_CONFIG --target-display-name master-e2e-image --prompt "<prompt>" --expect-agents image-capture,image-compress --master-data-db $work/master/data.db --timeout 300s`.
7. Driver exit code is the script's exit code. Print `OK image-pipeline e2e` on success; on failure, print log paths (`master.log`, `capture.log`, `compress.log`).

The prerequisite "credentials filled in via prior interactive registration" means **the e2e is a "second-run" scenario**: a one-time interactive setup is required for each of the four configs (master, capture, compress, driver). The setup is a manual step run by whoever first sets up the test box; the README documents it. This matches the existing `cmd/master-agent/scripts/e2e.sh` (which also assumes pre-registered slaves).

## Test matrix summary

| Layer | Location | Runner | Required env |
|---|---|---|---|
| Unit — handle JSON | `pkg/transport/transport_test.go` | `go test` | none |
| Unit — http transport | `pkg/transport/http/http_test.go` | `go test -race` | none |
| Unit — sharedfs transport | `pkg/transport/sharedfs/sharedfs_test.go` | `go test` | none |
| Unit — imageops | `examples/image-pipeline/internal/imageops/imageops_test.go` | `go test` | none |
| Unit — handlepick | `examples/image-pipeline/internal/handlepick/handlepick_test.go` | `go test` | none |
| Smoke — capture handler | `examples/image-pipeline/agent-image-capture/main_test.go` | `go test` (uses in-process httpx) | none |
| Smoke — compress handler | `examples/image-pipeline/agent-image-compress/main_test.go` | `go test` (uses `httptest.Server` for the upstream image) | none |
| E2E — full pipeline | `examples/image-pipeline/scripts/e2e.sh` (which runs `e2e-driver`) | bash, manual | pre-registered configs + `AGENTSERVER_URL`, `ANTHROPIC_API_KEY`, `claude`, `go`, `sqlite3` |

CI gates the first five (already covered by the existing `go test ./...` invocation once the new packages exist). The bash e2e is documented as manual-run, mirroring the existing `cmd/master-agent/scripts/e2e.sh` pattern.

## Open risks and mitigations

- **Real claude as planner may not always emit `{{n1.output}}`.** Mitigation: the master prompt is explicit about it; if it still misbehaves, swap `master.planner.bin` to point at a small inline fake planner script that the e2e installs to `$work/bin/fake-planner.sh`. The script discovers the real agent IDs (via DiscoverAgents) and writes a fixed chain DAG against them. Documented in README; not the default path.
- **The capture agent's HTTP URL must be reachable from the compress agent.** Both run on the same host in the e2e (default `127.0.0.1:0`); fine. A cross-host run would need each agent to bind a routable address and override `Options.PublicURL`. Out of scope for the script; in scope for `pkg/transport/http`'s API.
- **Custom-agent process lifetime owns the HTTP server.** The transport server lives inside each agent process; if the agent process restarts mid-task, prior URLs become invalid. The agents are long-lived (`Connect` blocks for the whole session), so this only matters across runs of the e2e. The README warns: rerunning the e2e invalidates URLs from a previous run.
- **Race between agent registration and master task submission.** There is no log line emitted on successful card publish (only on failure). The driver mitigates by polling `agentsdk.DiscoverAgents` until all three expected display names are visible (60s ceiling), then proceeds.
- **Test cost.** Real claude is invoked twice per e2e run (planner emits DAG once; reducer summarizes once). Sub-task chats (n1, n2) are handled by the custom Go agents, **not** by claude — so token cost is bounded to ~2 claude calls regardless of pipeline complexity. Documented in script header.
- **`Task` struct's `proxyToken`/`serverURL` are unexported in agentsdk.** Tests cannot directly construct a `*agentsdk.Task` to drive the handler. Mitigation: each agent's `main.go` exposes the actual work as a tiny exported `runX(ctx, hx, prompt) (string, error)` helper that takes the inputs the handler would extract from `Task`; tests exercise the helper. The handler is a thin wrapper that calls the helper then `task.Complete`/`task.Fail`. This keeps the handler 5 lines and the work testable.

## Deliverables checklist

- [ ] `multi-agent/pkg/transport/transport.go` + test
- [ ] `multi-agent/pkg/transport/http/http.go` + test
- [ ] `multi-agent/pkg/transport/sharedfs/sharedfs.go` + test
- [ ] `multi-agent/examples/image-pipeline/README.md`
- [ ] `multi-agent/examples/image-pipeline/internal/imageops/imageops.go` + test
- [ ] `multi-agent/examples/image-pipeline/internal/handlepick/handlepick.go` + test
- [ ] `multi-agent/examples/image-pipeline/internal/agentboot/agentboot.go`
- [ ] `multi-agent/examples/image-pipeline/agent-image-capture/main.go` + test + config.example.yaml
- [ ] `multi-agent/examples/image-pipeline/agent-image-compress/main.go` + test + config.example.yaml
- [ ] `multi-agent/examples/image-pipeline/e2e-driver/main.go`
- [ ] `multi-agent/examples/image-pipeline/scripts/e2e.sh`
- [ ] All of the above pass `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] `examples/image-pipeline/scripts/e2e.sh` runs green end-to-end on a host with pre-registered configs + real `AGENTSERVER_URL`, `ANTHROPIC_API_KEY`, and `claude`
