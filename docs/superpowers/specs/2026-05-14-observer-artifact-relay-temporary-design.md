# Observer Artifact Relay Temporary Design

Date: 2026-05-14

## Context

`multi-agent` currently assumes an agentserver route shaped like:

```http
/api/agent/peer/{short_id}/proxy/{path...}
```

The intended purpose is agent-to-agent HTTP forwarding over the target agent's
agentserver tunnel. The driver uses it to expose local `/files/*` endpoints so
master/slave agents can read user-mentioned files and PUT generated outputs
back to the user's machine.

Published `github.com/agentserver/agentserver v0.48.1` does not expose this
route or an SDK `PeerProxy` helper. The long-term owner for this capability
should remain agentserver because it already owns agent identity, workspace
membership, short IDs, live tunnels, and proxy-token authentication.

## Decision

Temporarily move only the artifact-transfer use case into `observer-server`.
Do not move the full generic peer HTTP proxy into observer. The default
temporary mode should be lazy relay: register file handles at task submission
time, but upload file bytes only when a slave actually requests them.

Observer should act as an artifact relay:

1. `driver-agent` registers read files or directory roots with observer and
   places observer URLs in the task manifest.
2. Slave agents fetch those URLs with their observer bearer token.
3. If the artifact content is not present yet, observer records a fetch request
   for the owning driver and returns a pending response.
4. `driver-agent` polls observer for pending artifact requests, reads the local
   file, and uploads the requested content.
5. Slave agents retry or long-poll until observer can stream the artifact body.
6. Slave agents upload write results to observer using write tokens from the
   manifest.
7. `driver-agent` retrieves completed write artifacts from observer and writes
   them to the local paths it registered.

This satisfies the immediate file exchange requirement without requiring a
new persistent tunnel between every agent and observer.

## Non-Goals

- Observer will not forward arbitrary HTTP methods to arbitrary agent-local
  handlers.
- Observer will not become the permanent peer-proxy authority.
- Observer will not replace agentserver workspace discovery or task delegation.
- Observer will not expose user-local files by path; all reads/writes must use
  opaque artifact IDs and write tokens.

## Proposed Observer API

All endpoints use the existing observer bearer-token model. The token identifies
`workspace_id`, `agent_id`, and role.

```http
POST /api/artifacts
Authorization: Bearer <driver-token>
Content-Type: application/json
```

Request:

```json
{
  "path": "/home/me/input.txt",
  "kind": "file",
  "mime": "text/plain",
  "bytes": 123,
  "sha256": "optional-if-known",
  "mode": "lazy"
}
```

Returns:

```json
{
  "artifact_id": "art_...",
  "url": "https://observer.example.com/api/artifacts/art_...",
  "state": "registered"
}
```

```http
GET /api/artifacts/{artifact_id}
Authorization: Bearer <agent-token>
```

Allowed only within the same workspace. If content has already been uploaded,
the response streams the artifact body. If content is not available, observer
records a fetch request for the owning driver and returns:

```http
HTTP/1.1 202 Accepted
Retry-After: 2
Content-Type: application/json
```

```json
{
  "state": "pending",
  "artifact_id": "art_...",
  "request_id": "fetch_..."
}
```

```http
GET /api/artifact-requests
Authorization: Bearer <driver-token>
```

Returns pending fetch requests for the authenticated driver:

```json
{
  "requests": [
    {
      "request_id": "fetch_...",
      "artifact_id": "art_...",
      "kind": "file",
      "path": "/home/me/input.txt"
    }
  ]
}
```

```http
PUT /api/artifacts/{artifact_id}/content
Authorization: Bearer <driver-token>
Content-Type: application/octet-stream
```

Uploads the requested content. Observer validates owner, updates sha256/bytes,
stores the body, and marks the artifact available.

```http
POST /api/write-tokens
Authorization: Bearer <driver-token>
Content-Type: application/json
```

Request:

```json
{
  "task_id": "t-...",
  "path": "/local/output.txt",
  "overwrite": true
}
```

Returns:

```json
{
  "write_id": "wr_...",
  "put_url": "https://observer.example.com/api/writes/wr_..."
}
```

```http
PUT /api/writes/{write_id}
Authorization: Bearer <slave-token>
Content-Type: application/octet-stream
```

Stores the output artifact and marks the write as completed.

```http
GET /api/writes?task_id=t-...
Authorization: Bearer <driver-token>
```

Returns completed writes so `driver-agent` can download and write them back to
the registered local paths.

## Manifest Shape

When observer relay is enabled, `driver-agent` should emit observer URLs instead
of agentserver peer-proxy URLs:

```json
{
  "files": [
    {
      "path": "/home/me/input.txt",
      "kind": "file",
      "url": "https://observer.example.com/api/artifacts/art_...",
      "sha256": "...",
      "bytes": 123
    }
  ],
  "writes": [
    {
      "path": "/home/me/output.txt",
      "kind": "file",
      "overwrite": true,
      "put_url": "https://observer.example.com/api/writes/wr_..."
    }
  ]
}
```

Future lazy directory roots should expose separate list and blob URLs:

```json
{
  "files": [
    {
      "path": "/home/me/data",
      "kind": "dir",
      "list_url": "https://observer.example.com/api/artifacts/art_dir/list",
      "blob_url": "https://observer.example.com/api/artifacts/art_dir/blob"
    }
  ]
}
```

`GET list_url` lazily asks the driver to produce a directory listing. `GET
blob_url?path=rel/file.txt` lazily asks the driver to upload only that file
inside the registered directory root. The first implementation should reject
symlinks and path escapes exactly as the existing driver `/files/dir/*` handler
does.

The current MVP implements lazy relay for individual files and writeback. It
rejects directory `read_paths` in `observer_lazy` mode with a clear error until
observer requests can distinguish directory listing requests from per-file blob
requests.

## Transport Modes

Driver configuration should support three modes:

```yaml
driver_defaults:
  artifact_transport: observer_lazy # peer_proxy | observer_eager | observer_lazy
  eager_upload_max_bytes: 1048576
  lazy_fetch_timeout_sec: 120
```

- `peer_proxy`: use agentserver `/api/agent/peer/{short_id}/proxy` when the
  deployment supports it.
- `observer_eager`: upload file bytes to observer before delegating the task.
  This is simple and reliable for small files.
- `observer_lazy`: register handles before delegation and upload bytes only
  after observer records a slave fetch request. This should be the default
  fallback while published agentserver lacks peer proxy support.

## Security Requirements

- Artifact and write access is workspace-scoped.
- Artifact IDs and write IDs must be opaque random identifiers.
- Write IDs are single-purpose and should be bound to driver owner, task ID,
  intended path metadata, and overwrite policy.
- Observer stores original local paths only as metadata visible to the owning
  driver; slaves should not need path authority.
- Lazy fetch requests must be visible only to the owning driver.
- Lazy fetch requests must expire and surface a clear error to slaves if the
  driver is offline or fails to upload content within the configured timeout.
- Enforce max artifact size and optional retention TTL.
- Persist sha256 and byte count for auditability.
- Avoid logging artifact contents.

## Trade-Offs

This is simpler than a generic observer-hosted tunnel and works with published
agentserver releases. Lazy relay avoids uploading files that no slave reads,
which is important for large local paths and directory roots. It does require
driver-agent to remain online while the task runs. If driver-agent is offline,
observer can record the request but cannot satisfy it.

Eager relay remains useful for small files because it makes slave reads
single-step and avoids pending/retry behavior. For very large directories, lazy
directory listing and per-file lazy blob fetch is preferred over zipping the
entire directory snapshot.

## Future Agentserver Requirement

The desired permanent implementation remains in agentserver:

```http
GET|POST|PUT|... /api/agent/peer/{target_short_id}/proxy/{path...}
Authorization: Bearer <caller_proxy_token>
```

Agentserver should:

1. Validate the caller proxy token.
2. Resolve `{target_short_id}` to a target sandbox in the same workspace.
3. Verify the target has an active tunnel.
4. Forward method, path, query, selected headers, and body through the target
   tunnel HTTP stream.
5. Return the target status, headers, and body to the caller.

When this lands in agentserver, `driver-agent` can switch back to peer-proxy
URLs and observer artifact relay can remain as an optional fallback for
deployments without peer proxy support.
