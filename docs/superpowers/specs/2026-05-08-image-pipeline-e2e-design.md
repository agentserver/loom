# Image-Pipeline End-to-End Test — Design

**Date:** 2026-05-08
**Status:** Draft (awaiting user review)
**Scope:** Add a live end-to-end test that exercises `master-agent` + two `slave-agent` processes with a real `agentserver` and real `claude`. The pipeline captures an image on slave A, then compresses it on slave B, returning a final URL/byte-count to the master's reducer. Along the way, formalize a small `pkg/transport` library so that user-written sub-task agents have a documented, reusable way to pass large/binary artifacts between nodes without inflating prompts.

## Motivation

The current framework auto-flows sub-task outputs into downstream prompts via the `{{nX.output}}` template (see `internal/orchestrator/fanout.go:127-129` and `internal/orchestrator/dag.go:204 Render`). This works for short text but breaks down when sub-task output is binary or large: stuffing a base64 PNG into the next prompt explodes token usage and forces the intermediate slave's claude to act as a byte-shuffling middleman.

We do **not** want to push transport into the framework — `multi-agent/internal/*` should remain transport-agnostic, with `{{nX.output}}` as a string-substitution default. Instead we add a side-channel pattern: the upstream node returns a **handle JSON** (small, e.g. `{"type":"image_url","url":"http://...","bytes":51234}`), the framework substitutes that JSON into the downstream prompt as usual, and the downstream tool dereferences via the URL. This keeps prompts small, leaves bytes out of the LLM round-trip, and lets users plug in any transport (HTTP, shared filesystem, S3, custom) without framework changes.

This work delivers:

1. A `pkg/transport` package with a `Transport` interface and two reference implementations (`http`, `sharedfs`). Not imported by `internal/*`.
2. A documented "handle JSON" output convention.
3. Two reference MCP tools (`mcp-image-capture`, `mcp-image-compress`) under `examples/image-pipeline/` that use `pkg/transport/http` to demonstrate the pattern.
4. A bash e2e script that wires master + 2 slaves + agentserver + claude end-to-end and asserts pipeline success.

## Non-goals

- No changes to `internal/orchestrator`, `internal/planner`, `internal/dispatch`, `internal/executor`, `internal/store`, or any other framework internal.
- No new `skill` field on planner nodes; no orchestrator changes to thread skills through `DelegateTask`. Sub-tasks continue to land on slaves' default executor (claude), which calls MCP tools.
- No authentication, GC, or quota for the reference HTTP transport — it's testdata-grade.
- No real-camera capture; the capture tool synthesizes a deterministic noise PNG.

## Architecture

### Data flow (happy path)

```
[user] --POST {skill:fanout, prompt:"capture then compress"}--> [master-agent]
                                                                     |
                                          master.planner (real claude) emits DAG:
                                          [{id:n1, target_id:slave-cap, prompt:"capture a 256x256 image",  depends_on:[]},
                                           {id:n2, target_id:slave-comp, prompt:"compress {{n1.output}} at q=50", depends_on:[n1]}]
                                                                     |
                                          DelegateTask(n1) -> slave-cap.claude
                                              -> calls MCP image.capture(width:256,height:256)
                                              -> capture tool generates PNG, transport.Put -> Handle{type:image_url, url:http://A:7001/...}
                                              -> returns Handle.Marshal() as MCP tool result
                                              -> slave-cap.claude returns that JSON string as task output
                                                                     |
                                          orchestrator stores outputs[n1] = "<handle JSON>"
                                          orchestrator Renders n2.prompt: substitutes {{n1.output}} with full handle JSON
                                                                     |
                                          DelegateTask(n2) -> slave-comp.claude
                                              -> sees handle JSON in prompt, extracts url
                                              -> calls MCP image.compress(url:"...", quality:50)
                                              -> compress tool: HTTP GET url, jpeg.Encode, transport.Put -> Handle{...mime:image/jpeg, ratio:0.4}
                                              -> returns Handle.Marshal() as MCP tool result
                                              -> slave-comp.claude returns that JSON as task output
                                                                     |
                                          master.reducer (real claude) summarizes:
                                          "Captured 51234-byte PNG, compressed to 20100 bytes (ratio 0.39). Final URL: http://B:7002/..."
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
│       ├── mcp-image-capture/
│       │   ├── main.go
│       │   └── main_test.go           # stdio JSON-RPC smoke
│       ├── mcp-image-compress/
│       │   ├── main.go
│       │   └── main_test.go
│       ├── e2e-driver/
│       │   └── main.go                # Go binary: discovers master via agentsdk,
│       │                              #   DelegateTask(skill=fanout), WaitForTask,
│       │                              #   asserts reducer output + downloads final URL
│       └── scripts/
│           └── e2e.sh                 # builds, launches master + 2 slaves, runs driver, asserts
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
- No auth, no TTL — the process owns its state and dies when the MCP tool process exits.

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

## Reference MCP tools

Both tools follow the existing stdio MCP shape used by `multi-agent/testdata/fake-mcp-stdio/main.go`. Each is a tiny Go `main` that reads JSON-RPC requests on stdin, writes responses on stdout, and prints diagnostics on stderr. Each starts an HTTP transport on its own ephemeral port at boot.

### `mcp-image-capture`

| Field | Value |
|---|---|
| Server name (in slave config) | `image` |
| Tool | `capture` |
| Input args | `{"width":int=256,"height":int=256,"seed":int=42}` (all optional) |
| Behavior | Generate a width×height PNG of seeded pseudo-random RGBA pixels. `transport.Put` it, set `Type="image_url"`, return `Handle.Marshal()` as the MCP tool result string. |
| Capability change | `false`. |
| Stderr boot line | `LISTEN <host:port>` (for e2e log capture). |
| Failure modes | width/height out of bounds (1..4096) → MCP error. |

### `mcp-image-compress`

| Field | Value |
|---|---|
| Server name | `image` |
| Tool | `compress` |
| Input args | `{"url":string (required),"quality":int=50}` |
| Behavior | HTTP GET the url, `image.Decode`, `jpeg.Encode(quality)`, `transport.Put` the JPEG bytes. Return new Handle with `Type="image_url"`, `MIME="image/jpeg"`, `Meta:{"original_bytes":"<n>","ratio":"<r>"}` (ratio = compressed/original to 2 decimals). |
| Capability change | `false`. |
| Stderr boot line | `LISTEN <host:port>`. |
| Failure modes | url missing → MCP error; HTTP GET fails or non-2xx → MCP error; image decode fails → MCP error; quality outside 1..100 → MCP error. |

### Smoke tests for the MCP tools

`mcp-image-capture/main_test.go`:
- Spawn the binary as a subprocess (`go test` builds via `os.Executable` path or `exec.Command("go", "run", ...)`); send `tools/call` JSON-RPC with `name=capture, arguments={width:64,height:64}`; assert the response's `content[0].text` parses as a Handle with `MIME==image/png`, `Bytes>0`. Then HTTP GET the Handle URL and assert byte length matches.
- Same with default args.
- Send `width=0` → expect MCP error response.

`mcp-image-compress/main_test.go`:
- Bring up an `httptest.Server` that serves a known PNG. Spawn the binary; call `compress` with that URL and `quality=50`; assert the returned Handle is `image/jpeg`, `Bytes < served PNG length`, and `Get` on the returned URL yields valid JPEG (decode succeeds).
- Call with missing url → expect MCP error.
- Call with url returning 500 → expect MCP error.

## Slave / master configurations

The e2e script generates these on the fly into the work dir; checked-in `config.example.yaml` files under each MCP tool dir document the shape.

### slave-A (capture provider)

```yaml
server: { url: ${AGENTSERVER_URL}, name: slave-cap-e2e }
claude: { bin: claude }
mcp_servers:
  image:
    transport: stdio
    command: ${WORK}/bin/mcp-image-capture
discovery:
  display_name: image-capture
  description: |
    Image capture agent. Provides MCP tool `capture` (server=image, tool=capture).
    Input: {"width":int,"height":int,"seed":int} (all optional, defaults 256/256/42).
    Output: a JSON handle string {"type":"image_url","url":"http://...","bytes":N,"mime":"image/png"}.
    Use this agent to PRODUCE an image. Do NOT pass an existing image to it.
  skills: [chat, mcp]
```

### slave-B (compress provider)

```yaml
server: { url: ${AGENTSERVER_URL}, name: slave-comp-e2e }
claude: { bin: claude }
mcp_servers:
  image:
    transport: stdio
    command: ${WORK}/bin/mcp-image-compress
discovery:
  display_name: image-compress
  description: |
    Image compression agent. Provides MCP tool `compress` (server=image, tool=compress).
    Input: {"url":"<image url>","quality":int=50}. Reads the image from url, re-encodes as JPEG.
    Output: a JSON handle string {"type":"image_url","url":"http://...","bytes":N,"mime":"image/jpeg",
    "meta":{"original_bytes":"<n>","ratio":"<r>"}}.
    Use this agent to COMPRESS an image you already have a url for.
  skills: [chat, mcp]
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
- `--config` (path to a yaml with the driver's own agent registration credentials, same shape as slave config but no executors needed; alternatively `--reuse-creds` points at slave-cap's config to piggyback)
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

Steps:

1. `work=$(mktemp -d)`. Trap on EXIT: kill all spawned PIDs, `rm -rf "$work"`.
2. From module root, `go build` five binaries into `$work/bin/`:
   - `./cmd/master-agent`
   - `./cmd/slave-agent`
   - `./examples/image-pipeline/mcp-image-capture`
   - `./examples/image-pipeline/mcp-image-compress`
   - `./examples/image-pipeline/e2e-driver`
3. Materialize three working dirs `$work/{master,slave-cap,slave-comp}` with respective `config.yaml` files (templates above).
4. Launch in order, each backgrounded with stdout+stderr to `$work/<name>.log`:
   - `slave-cap` (cwd=`$work/slave-cap`)
   - `slave-comp` (cwd=`$work/slave-comp`)
   - `master` (cwd=`$work/master`)
5. Wait for registration: there is no log line that signals "card published successfully" — the existing code only logs on failure. Instead: poll `master`'s data.db for the `credentials` written back by `tunnel.EnsureRegistered` (the master writes credentials into `master/config.yaml` after device-flow completes; on subsequent runs they're already present). The script handles two cases:
   - **First run** (no credentials in any config): the script aborts with a clear message, instructs the user to perform initial device-flow registration manually for each of the three configs, and rerun. (Auto-handling device flow in a script is out of scope.)
   - **Subsequent runs** (credentials present): each agent skips device flow; the script polls until `agentsdk.DiscoverAgents` (called from the driver during step 6) sees all three agents — this is the real readiness check.
6. Run the driver: `$work/bin/e2e-driver --reuse-creds $work/slave-cap/config.yaml --target-display-name master-e2e-image --prompt "<prompt>" --master-data-db $work/master/data.db --timeout 300s`.
7. Driver exit code is the script's exit code. Print `OK image-pipeline e2e` on success; on failure, print log paths (`master.log`, `slave-cap.log`, `slave-comp.log`).

The credentials concern means **the e2e is a "second-run" scenario**: a one-time interactive setup is required. This matches the existing `cmd/master-agent/scripts/e2e.sh` (which also requires pre-registered slaves) and the e2e doc strings.

## Test matrix summary

| Layer | Location | Runner | Required env |
|---|---|---|---|
| Unit — handle JSON | `pkg/transport/transport_test.go` | `go test` | none |
| Unit — http transport | `pkg/transport/http/http_test.go` | `go test -race` | none |
| Unit — sharedfs transport | `pkg/transport/sharedfs/sharedfs_test.go` | `go test` | none |
| Smoke — capture MCP tool | `examples/image-pipeline/mcp-image-capture/main_test.go` | `go test` | none |
| Smoke — compress MCP tool | `examples/image-pipeline/mcp-image-compress/main_test.go` | `go test` (uses `httptest.Server` for the upstream image) | none |
| E2E — full pipeline | `examples/image-pipeline/scripts/e2e.sh` (which runs `e2e-driver`) | bash, manual | pre-registered configs + `AGENTSERVER_URL`, `ANTHROPIC_API_KEY`, `claude`, `go`, `sqlite3` |

CI gates the first five (already covered by the existing `go test ./...` invocation once the new packages exist). The bash e2e is documented as manual-run, mirroring the existing `cmd/master-agent/scripts/e2e.sh` pattern.

## Open risks and mitigations

- **Real claude as planner may not always emit `{{n1.output}}`.** Mitigation: the master prompt is explicit about it; if it still misbehaves, the `USE_FAKE_PLANNER=1` fallback path is documented (with the caveat that the fake's hardcoded `target_id` values need swapping in via a small sed step the script can perform after discovering the real agent IDs).
- **Slave A's HTTP transport URL must be reachable from slave B.** Both slaves run on the same host in the e2e (default `127.0.0.1:0` random port); this is fine for the bash script. A cross-host run would need each MCP tool to bind a routable address — handled via `Options.Addr` and `Options.PublicURL`. Out of scope for the script; in scope for the package design.
- **MCP tool process lifetime.** The HTTP transport server lives inside the MCP tool subprocess; if the slave's MCP executor recycles the subprocess between calls, the URL becomes invalid. The current `internal/executor/mcp.go` keeps stdio MCP processes alive in `e.stdios` for the executor's lifetime, so within one task the URL stays valid. Documented in the README; if the framework later changes that policy, swap `pkg/transport/sharedfs` (path-based, persistent across processes).
- **Race between agent registration and master task submission.** There is no log line emitted on successful card publish (only on failure). The driver mitigates by polling `agentsdk.DiscoverAgents` until all three expected agents are visible (60s ceiling), then proceeds.
- **Test cost.** Real claude is invoked at least three times per e2e run (planner, two sub-task chats, reducer = 3-4 calls). Documented in script header so users know.

## Deliverables checklist

- [ ] `multi-agent/pkg/transport/transport.go` + test
- [ ] `multi-agent/pkg/transport/http/http.go` + test
- [ ] `multi-agent/pkg/transport/sharedfs/sharedfs.go` + test
- [ ] `multi-agent/examples/image-pipeline/README.md`
- [ ] `multi-agent/examples/image-pipeline/mcp-image-capture/main.go` + smoke test
- [ ] `multi-agent/examples/image-pipeline/mcp-image-compress/main.go` + smoke test
- [ ] `multi-agent/examples/image-pipeline/e2e-driver/main.go`
- [ ] `multi-agent/examples/image-pipeline/scripts/e2e.sh`
- [ ] All of the above pass `go build ./...`, `go vet ./...`, `go test ./...`
- [ ] `examples/image-pipeline/scripts/e2e.sh` runs green end-to-end on a host with pre-registered configs + real `AGENTSERVER_URL`, `ANTHROPIC_API_KEY`, and `claude`
