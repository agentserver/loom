# image-pipeline

End-to-end example demonstrating how to wire `multi-agent`'s `master-agent`
to two custom workspace agents that exchange a non-text artifact (an image)
through a side channel, while the inter-node "you've finished, here's a
handle" message still flows through the framework's standard
`{{nX.output}}` template path.

## Why this exists

The framework substitutes upstream sub-task outputs into downstream prompts
via the `{{nX.output}}` template (see `internal/orchestrator/dag.go:204`).
That works for short text, but stuffing a base64 PNG into a prompt is
expensive and forces intermediate slaves to act as byte couriers. Instead,
the producer can return a small **handle JSON** as its output:

```json
{"type":"image_url","url":"http://127.0.0.1:54321/blobs/9f86d081884c7d65","bytes":51234,"mime":"image/png"}
```

The framework substitutes that string into the next prompt as usual. The
consumer parses the URL out and `Get`s the bytes from the side channel
(here: HTTP, but the same pattern works for shared FS or any other
transport you implement). See `pkg/transport` for the small library that
makes this convenient.

## Layout

- `internal/imageops/`   — `SynthPNG` + `EncodeJPEG` (deterministic PNG generator + JPEG encoder)
- `internal/handlepick/` — `FirstURL` regex used by the compress agent
- `internal/agentboot/`  — shared "load yaml → register or set creds → publish card → Connect" glue
- `agent-image-capture/` — custom agent: on any task, returns a handle JSON for a fresh 256x256 PNG
- `agent-image-compress/`— custom agent: on any task, finds the first URL in the prompt, downloads, re-encodes as JPEG, returns a new handle JSON
- `e2e-driver/`          — Go binary that DelegateTasks the master and asserts the reducer output
- `scripts/e2e.sh`       — bash wrapper that builds, launches, runs the driver, and reports

## First-time setup

The e2e expects four pre-registered configs (each tied to its own agentserver
sandbox identity). Register them once by running each binary interactively:

```bash
# From multi-agent/ module root, for each of master / capture / compress / driver:
cp examples/image-pipeline/agent-image-capture/config.example.yaml /tmp/capture.yaml
$EDITOR /tmp/capture.yaml      # set server.url to your agentserver
go run ./examples/image-pipeline/agent-image-capture --config /tmp/capture.yaml
# Open the printed verification URL in a browser, complete login.
# The binary writes credentials back into /tmp/capture.yaml; ctrl-C when registered.
```

Repeat for `agent-image-compress`, `cmd/master-agent`, and `e2e-driver` (the
driver also needs an identity to call DelegateTask — the same minimal config
shape works; you can reuse the agentboot config file for the driver).

Note: master-agent uses a different config schema (`cmd/master-agent/config.example.yaml`),
not the agentboot shape. Set `discovery.display_name: master-e2e-image` and
`skills: [route, fanout]`.

## Running the e2e

Once you have four configs with credentials:

```bash
export AGENTSERVER_URL=https://your-agentserver
export ANTHROPIC_API_KEY=sk-...
export MASTER_CONFIG=/tmp/master.yaml
export CAPTURE_CONFIG=/tmp/capture.yaml
export COMPRESS_CONFIG=/tmp/compress.yaml
export DRIVER_CONFIG=/tmp/driver.yaml

cd multi-agent/   # module root
./examples/image-pipeline/scripts/e2e.sh
```

Expected output ends with `OK image-pipeline e2e`. On failure, the script
prints the tail of master.log / capture.log / compress.log.

Real `claude` is invoked twice per run (planner + reducer); sub-task work is
done in-process by the Go agents, so the run cost is small and bounded.

## Adapting the pattern

The `pkg/transport` interface is intentionally small: `Put(mime, reader) →
Handle` and `Get(handle) → reader`. Swap `pkg/transport/http` for
`pkg/transport/sharedfs` (or your own implementation) if your topology
favors a shared filesystem, S3, or anything else. The framework doesn't
care; only the producer and consumer agents need to agree on a transport.
