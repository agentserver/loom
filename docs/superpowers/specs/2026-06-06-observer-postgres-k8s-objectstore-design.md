# Observer PostgreSQL / K8s / Object Store Design

Date: 2026-06-06
Status: Draft for implementation planning

## Context

`observer-server` currently runs as a single process backed by SQLite. It
handles several different responsibilities:

- agent registration and per-agent observer tokens
- optional task telemetry ingest through `/api/events`
- task and subtask projection tables
- artifact and write relay
- task contracts and resource snapshots
- userspace package registry metadata and blobs

This is acceptable for local and small deployments, but it is not a good
production shape. SQLite gives us a single-writer database, artifact and write
content currently live in database BLOB columns, and there is no Kubernetes
deployment model. Separately, driver and slave telemetry must remain opt-in:
agents must not report task telemetry unless explicitly enabled and authorized.

## Goals

1. Make production observer use PostgreSQL instead of SQLite.
2. Deploy observer on Kubernetes with multi-replica API pods.
3. Move artifact/write binary content out of the relational database and into
   S3-compatible object storage.
4. Require an operations telemetry API key for `/api/events`; agent identity
   alone is not enough to submit telemetry.
5. Keep event ingest simple in the first production version because telemetry
   is opt-in and expected to be low volume.
6. Keep SQLite available only for unit tests and local developer smoke tests.

## Non-goals

- Do not build a general-purpose observability platform.
- Do not introduce Kafka, NATS, ClickHouse, Prometheus remote write, or OLAP
  storage in the first version.
- Do not proxy large artifact/write content through observer by default.
- Do not allow anonymous, bootstrap-key-only, or agent-token-only telemetry
  ingest.
- Do not make schema migrations run in every observer pod at startup.

## Decisions

| Area | Decision |
| --- | --- |
| Production database | PostgreSQL only |
| Local/unit database | SQLite remains allowed for tests and local smoke |
| K8s packaging | Helm chart under `deploy/charts/observer/` |
| DB migration | Dedicated Kubernetes Job, not observer pod startup |
| Artifact/write content | S3-compatible object store; PostgreSQL stores metadata only |
| Object store dev target | MinIO for local/prod_test |
| Object store production target | External S3-compatible service |
| Artifact/write transfer | Presigned PUT/GET URL by default |
| Observer proxy transfer | Small-file fallback only, with strict size limit |
| Telemetry default | Driver/slave default off |
| Telemetry authorization | Agent token plus operations telemetry API key |
| Telemetry projection | Synchronous projection in first version |
| Future projection | Store boundaries leave room for async worker |
| Retention | Configurable event retention, default 30 days |
| Rate limiting | Required for `/api/events` |
| Query shape | Paginated list APIs only; no unbounded task/event list |

## Architecture

```
agent
  | register / task contracts / resource snapshots / telemetry metadata
  v
K8s Service: observer-server
  | SQL metadata, tokens, projections
  v
PostgreSQL

agent
  | presigned PUT/GET for artifact/write bodies
  v
S3-compatible object store

operator
  | Secret: DB DSN, object-store credentials, telemetry ops keys
  v
K8s observer deployment
```

The observer API remains the control plane. It authenticates agents, validates
workspace boundaries, creates metadata rows, and signs object-store URLs. Large
content moves directly between agents and object storage.

## PostgreSQL Store

Introduce a PostgreSQL implementation of the observer store behind the existing
observerweb store interface. The first implementation should live next to the
SQLite store rather than replacing it in place.

Proposed package split:

- `internal/observerstore/sqlite`: current SQLite implementation moved or
  wrapped here.
- `internal/observerstore/postgres`: new PostgreSQL implementation.
- `internal/observerstore`: shared types and interfaces.

Production `observer-server` config selects the store:

```yaml
store:
  driver: postgres
  postgres:
    dsn_env: OBSERVER_DATABASE_URL
    max_open_conns: 20
    max_idle_conns: 10
    conn_max_lifetime: 30m
```

SQLite config remains available for tests:

```yaml
store:
  driver: sqlite
  sqlite:
    path: observer.db
```

Production validation rejects `store.driver: sqlite` unless
`allow_sqlite_in_production: true` is explicitly set. That override is only for
emergency/manual deployments, not the default Helm values.

## PostgreSQL Schema

Use PostgreSQL-native types where useful:

- timestamps: `timestamptz`
- booleans: `boolean`
- JSON payloads: `jsonb`
- content references: `text object_key`, not `bytea`

Core tables keep the current logical model:

- `api_keys`
- `workspaces`
- `agents`
- `events`
- `tasks`
- `subtasks`
- `mcp_servers`
- `artifacts`
- `artifact_requests`
- `writes`
- `task_contracts`
- `resource_snapshots`
- userspace metadata tables

`artifacts` and `writes` drop body columns in the PostgreSQL schema:

```sql
CREATE TABLE artifacts (
  workspace_id text NOT NULL,
  id text NOT NULL,
  owner_agent_id text NOT NULL,
  path text NOT NULL,
  kind text NOT NULL,
  mime text NOT NULL DEFAULT '',
  state text NOT NULL,
  bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  object_key text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);

CREATE TABLE writes (
  workspace_id text NOT NULL,
  id text NOT NULL,
  owner_agent_id text NOT NULL,
  writer_agent_id text NOT NULL DEFAULT '',
  task_id text NOT NULL,
  path text NOT NULL,
  overwrite boolean NOT NULL DEFAULT false,
  state text NOT NULL,
  mime text NOT NULL DEFAULT '',
  bytes bigint NOT NULL DEFAULT 0,
  sha256 text NOT NULL DEFAULT '',
  object_key text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
```

`events` stores raw payload as JSONB when valid JSON is present:

```sql
CREATE TABLE events (
  event_id text PRIMARY KEY,
  ts timestamptz NOT NULL,
  workspace_id text NOT NULL,
  agent_id text NOT NULL,
  agent_role text NOT NULL,
  type text NOT NULL,
  task_id text NOT NULL DEFAULT '',
  parent_task_id text,
  subtask_id text,
  child_task_id text,
  summary text,
  subtask_summary text,
  status text,
  target_agent_id text,
  target_role text,
  mcp_server_name text,
  mcp_tools jsonb NOT NULL DEFAULT '[]'::jsonb,
  payload jsonb
);
```

Required indexes:

```sql
CREATE INDEX idx_events_workspace_ts ON events(workspace_id, ts);
CREATE INDEX idx_events_workspace_task_ts ON events(workspace_id, task_id, ts);
CREATE INDEX idx_events_workspace_agent_ts ON events(workspace_id, agent_id, ts);
CREATE INDEX idx_events_workspace_type_ts ON events(workspace_id, type, ts);
CREATE INDEX idx_subtasks_child ON subtasks(workspace_id, child_task_id);
CREATE INDEX idx_tasks_workspace_updated ON tasks(workspace_id, updated_at);
CREATE INDEX idx_artifacts_owner_state ON artifacts(workspace_id, owner_agent_id, state);
CREATE INDEX idx_writes_owner_task_state ON writes(workspace_id, owner_agent_id, task_id, state);
```

## Event Ingest and Projection

First version keeps synchronous projection:

1. authenticate agent token
2. verify operations telemetry API key
3. validate event identity matches the agent identity
4. begin PostgreSQL transaction
5. insert raw event into `events`
6. update `tasks`, `subtasks`, or `mcp_servers` projection rows
7. commit
8. return `202 Accepted`

This is intentionally simple. Telemetry is default-off and gated by an
operations key, so expected volume is low.

The implementation should still separate the store methods:

```go
AppendEvent(ctx context.Context, ev observer.Event) (inserted bool, err error)
ApplyEventProjection(ctx context.Context, ev observer.Event) error
Ingest(ctx context.Context, ev observer.Event) error
```

`Ingest` calls the first two methods in one transaction. A later async worker
can reuse `AppendEvent` and move `ApplyEventProjection` behind a durable offset
without changing the HTTP contract.

## Telemetry Authorization

`/api/events` must require two credentials:

```http
Authorization: Bearer <agent-token>
X-Loom-Telemetry-Key: <ops-telemetry-api-key>
```

The agent token proves which workspace and agent is submitting the event. The
operations telemetry key proves this deployment is allowed to emit telemetry.

Missing or invalid agent token returns `401 Unauthorized`.

Missing telemetry key returns `403 Forbidden`.

Invalid telemetry key returns `403 Forbidden`.

An event whose `workspace_id`, `agent_id`, or `agent_role` does not match the
resolved agent identity returns `403 Forbidden`.

The telemetry key is independent of bootstrap API keys. Add a separate table:

```sql
CREATE TABLE telemetry_api_keys (
  id text PRIMARY KEY,
  key_hash text NOT NULL UNIQUE,
  note text NOT NULL DEFAULT '',
  workspace_id text NOT NULL DEFAULT '*',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL
);
```

Scope rules:

- `workspace_id = '*'` allows all workspaces.
- any other value allows only that exact workspace.
- disabled keys never authorize ingest.

Observer config loads these keys from Secret-backed configuration:

```yaml
telemetry:
  enabled: true
  api_keys:
    - id: ops-global
      key_env: OBSERVER_TELEMETRY_KEY_GLOBAL
      workspace_id: "*"
      note: "global production telemetry gate"
```

Agents only send telemetry when their local config has telemetry enabled and a
telemetry key is configured:

```yaml
observer:
  enabled: true
  telemetry_enabled: true
  telemetry_api_key: ${OBSERVER_TELEMETRY_KEY}
```

## Rate Limiting

Rate limiting is required before enabling production telemetry.

Initial limiter:

- key: `workspace_id + agent_id + telemetry_key_id`
- default: 60 events per minute per key
- burst: 120 events
- response: `429 Too Many Requests`

K8s multi-replica means an in-memory limiter is approximate. This is acceptable
for the first version because telemetry is low volume and ops-key gated. If
production enables broad telemetry, replace it with Redis-backed or ingress
rate limiting.

## Event Size and Retention

Event body limit:

- default max request body: 256 KiB
- configurable upper bound: 1 MiB
- larger payloads must be stored as artifacts/object-store objects and referred
  to from the event payload

Retention:

- default event retention: 30 days
- cleanup runs as a Kubernetes CronJob
- first version may delete old rows by `ts`
- if event volume grows, move to monthly or weekly PostgreSQL partitions and
  drop old partitions

Projection tables (`tasks`, `subtasks`, `mcp_servers`) are not deleted by
event retention unless a separate workspace cleanup policy is configured.

## Object Store

Create an object-store abstraction for artifact/write/userspace blobs:

```go
type ObjectStore interface {
    PutPresignedURL(ctx context.Context, key string, mime string, expires time.Duration) (string, error)
    GetPresignedURL(ctx context.Context, key string, expires time.Duration) (string, error)
    Delete(ctx context.Context, key string) error
}
```

Object keys are generated by observer, not by agents:

```text
workspaces/<workspace_id>/artifacts/<artifact_id>
workspaces/<workspace_id>/writes/<write_id>
workspaces/<workspace_id>/userspace/<sha256>
```

Metadata flow:

1. agent creates artifact/write metadata row through observer
2. observer creates a pending row and returns a presigned upload URL
3. agent uploads content directly to object store
4. agent calls observer to mark upload complete with bytes and sha256
5. readers request a presigned download URL from observer

Small proxy fallback:

- default max proxy upload/download: 8 MiB
- disabled by default in production Helm values
- allowed in local/prod_test

## K8s Deployment

Ship observer Kubernetes deployment as a Helm chart under:

```text
deploy/charts/observer/
  Chart.yaml
  values.yaml
  values-local-minio.yaml
  values-production.example.yaml
  templates/
    _helpers.tpl
    deployment.yaml
    service.yaml
    httproute.yaml
    configmap.yaml
    secret.yaml
    migration-job.yaml
    retention-cronjob.yaml
    serviceaccount.yaml
    ingress.yaml
    notes.txt
```

Chart decisions:

- `values.yaml` is safe by default: PostgreSQL and object store are enabled by
  configuration, but real secrets are not embedded.
- `secret.yaml` creates Kubernetes Secrets only when values explicitly provide
  secret material; production should prefer `existingSecret`.
- `values-local-minio.yaml` targets kind/minikube/prod_test with MinIO.
- `values-production.example.yaml` documents managed PostgreSQL, PgBouncer, and
  external object-store settings.
- chart templates render migration and retention Jobs, but they are controlled
  by values flags.

Deployment template:

- run `observer-server`
- expose HTTP service
- mount ConfigMap for non-secret config
- read DB DSN, telemetry keys, and object-store credentials from Secret
- set resource requests and limits
- configure liveness and readiness probes

Gateway API HTTPRoute template:

- follow the current agentserver Helm chart style from
  `deploy/helm/agentserver/templates/httproute.yaml`
- use `apiVersion: gateway.networking.k8s.io/v1`
- gate rendering behind `gateway.enabled`
- configure parent Gateways through `gateway.parentRefs`
- configure the external hostname through `gateway.host`
- route `PathPrefix /` to the observer Service `backendRefs`
- do not add URL rewrites for observer's main route

Expected values shape:

```yaml
gateway:
  enabled: false
  parentRefs:
    - name: cilium-gateway
      namespace: cilium-gateway
  host: observer.example.com
```

Expected template shape:

```yaml
{{- if .Values.gateway.enabled }}
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ include "observer.fullname" . }}
spec:
  parentRefs:
    {{- toYaml .Values.gateway.parentRefs | nindent 4 }}
  hostnames:
    - {{ .Values.gateway.host | quote }}
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - name: {{ include "observer.fullname" . }}
          port: {{ .Values.service.port }}
{{- end }}
```

If a future observer subdomain needs an internal path mount, use the
agentserver `codex-auth` precedent: a dedicated HTTPRoute with
`filters.type: URLRewrite` and `urlRewrite.path.type: ReplacePrefixMatch`.
The main observer route must stay un-rewritten.

Health endpoints:

- `/healthz`: process is alive
- `/readyz`: PostgreSQL ping succeeds and object-store configuration is valid

Migration:

- `templates/migration-job.yaml` runs before rollout, either through a Helm
  hook or as an explicitly installed Job
- migration job is idempotent
- observer pod startup does not apply schema migrations

Helm hooks are allowed for local and simple deployments. Production may disable
the hook and run the rendered migration Job from CI/CD before `helm upgrade`.

Recommended production topology:

- observer API replicas: 2 minimum
- PostgreSQL: managed service or cluster outside this Deployment
- PgBouncer: recommended when replicas can scale above 2
- object store: managed S3-compatible service

## Query API Constraints

All list APIs must be paginated.

Defaults:

- default limit: 100
- max limit: 1000
- cursor: opaque token containing stable sort key

No endpoint should return all tasks, all events, all writes, or all artifacts
without a limit. Task detail endpoints may include recent events, but they must
also limit event count.

## Rollout Plan

Phase 1: Configuration and interfaces

- add store driver config
- add object store config
- add telemetry ops-key config
- add health endpoints

Phase 2: PostgreSQL store

- implement schema migrations
- implement PostgreSQL store methods equivalent to SQLite behavior
- keep SQLite tests
- add PostgreSQL integration tests using a test container or local service

Phase 3: Telemetry gate

- require `X-Loom-Telemetry-Key` on `/api/events`
- add telemetry key table/config
- update driver/slave observer client to send the key only when telemetry is
  explicitly enabled

Phase 4: Object store

- add S3-compatible object store adapter
- migrate artifact/write content paths to presigned URL flow
- keep proxy fallback for local/prod_test only

Phase 5: Helm deployment

- add Helm chart under `deploy/charts/observer/`
- add Gateway API `HTTPRoute` template using the same values shape as the
  current agentserver Helm chart
- add migration Job and retention CronJob
- add MinIO local values file
- document production Secret requirements

Phase 6: Load and failure testing

- verify event ingest under expected telemetry volume
- verify object upload/download without observer proxying content
- verify rate limit and retention behavior
- verify multi-replica observer pods against one PostgreSQL database

## Testing Strategy

Unit tests:

- telemetry API key validation
- event body limit
- missing/invalid key response codes
- projection logic shared between SQLite and PostgreSQL stores
- object key generation and presigned URL metadata flow

Integration tests:

- PostgreSQL schema migration on empty database
- event ingest inserts raw event and updates task projection
- duplicate event ID is idempotent
- telemetry key workspace scope enforcement
- artifact/write metadata round trip with MinIO
- Helm chart renders expected Deployment, Service, Job, CronJob, ConfigMap,
  Secret references, optional Ingress, and Gateway API HTTPRoute

Production smoke:

- deploy local K8s or kind with PostgreSQL and MinIO through Helm
- register driver/slave
- verify no telemetry is accepted without ops key
- enable telemetry explicitly and verify one event is accepted
- verify artifact/write bodies land in object store, not PostgreSQL

## Risks

PostgreSQL migration may expose SQLite-specific SQL assumptions. Mitigation:
keep store tests broad and run them against both backends.

Presigned URL flow changes artifact/write client behavior. Mitigation: keep a
small proxy fallback for local/prod_test while production defaults to direct
object-store transfer.

Synchronous projection can still create row-lock pressure if operators enable
high-frequency progress telemetry. Mitigation: rate limit from the first
version and keep `AppendEvent` / `ApplyEventProjection` separated for later
async projection.

K8s multi-replica makes in-memory rate limiting approximate. Mitigation: keep
initial limits conservative and document Redis or ingress-backed limiting as the
next step if broad telemetry is enabled.

## Open Implementation Questions

These are implementation choices, not product decisions:

- whether PostgreSQL migrations use embedded SQL files or a migration library
- whether the Helm chart should include optional PostgreSQL/MinIO subcharts or
  require external dependencies in all environments
- whether production should use Helm hooks for migrations or require CI/CD to
  run the migration Job explicitly before `helm upgrade`

The product decisions above do not depend on these choices.
