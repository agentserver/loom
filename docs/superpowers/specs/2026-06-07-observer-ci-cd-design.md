# Observer CI/CD Design

## Goal

Add GitHub Actions CI/CD for the observer production path and make the Helm
chart deploy the full observer stack in `k8s-nj-prod/dev-yuzishu`: observer,
PostgreSQL, and MinIO.

## Decisions

- GitHub Actions is the CI/CD platform because the repository remote is
  `github.com:agentserver/loom`.
- CI uses Go, Helm, and Python checks only; it does not require cluster access.
- CD builds and pushes the observer image to the Nanjing registry using the
  public endpoint `registry.nj.cs.ac.cn:10062/loom/observer`.
- Kubernetes pulls the same image through the Nanjing internal endpoint
  `registry.nj.cs.ac.cn/loom/observer`.
- CD uses kubeconfig secret `KUBECONFIG_NJ_PROD` with Kubernetes API server
  `https://k8s-prod.nj.cs.ac.cn:10062`.
- The deployment namespace is `dev-yuzishu`.
- PostgreSQL and MinIO are managed by the observer Helm chart with `ReadWriteOnce`
  PVCs using `csi-rbd-sc`.
- CD does not manage `HTTPRoute`; the existing public route
  `https://loom.nj.cs.ac.cn:10062/` remains platform-managed because the current
  namespace identity cannot create, read, patch, or update HTTPRoutes.
- Pushes to `master` deploy an isolated smoke release named
  `observer-ci-<github.run_number>`, test it through `kubectl port-forward` on
  local runner port `18190`, then uninstall the release and delete its PVCs.
- The Helm release name `observer` and the public route are used only by manual
  release deployments.
- Observer production identity uses agentserver by default in CD. Legacy
  observer API-key identity stays disabled.

## Required GitHub Secrets

- `REGISTRY_NJ_USERNAME`: Nanjing registry robot account name.
- `REGISTRY_NJ_PASSWORD`: Nanjing registry robot account token.
- `KUBECONFIG_NJ_PROD`: non-interactive kubeconfig for `k8s-nj-prod`.
- The smoke deploy path generates PostgreSQL, MinIO, and telemetry secrets at
  runtime and does not require production database/object-store secrets.

Manual release deploys additionally require:

- `OBSERVER_POSTGRES_PASSWORD`: PostgreSQL password for the chart-managed
  `observer` user.
- `OBSERVER_DATABASE_URL`: PostgreSQL DSN consumed by observer-server. This is
  explicit to avoid password URL-encoding bugs.
- `MINIO_ROOT_USER`: root/access key for chart-managed MinIO.
- `MINIO_ROOT_PASSWORD`: root/secret key for chart-managed MinIO.
- `OBSERVER_TELEMETRY_KEY`: operations telemetry API key required by
  `/api/events`.

## Release Deploy Constraint

The disposable live-smoke stack that previously owned release resource names
such as `deployment/observer-observer`, `service/observer-observer`,
`statefulset/observer-postgres`, and `service/observer-minio` has been deleted
from `dev-yuzishu`. Smoke deploys use a separate release name and do not
conflict with release resources. The manual release workflow still checks for
non-Helm resources with release names and fails clearly if a future manual stack
recreates them.

## Verification

- Pull requests and pushes run Go tests, module tidy diff, Helm lint/render
  tests, and Python unit tests.
- The deploy workflow pushes immutable commit SHA tags and `master-latest`.
- The smoke workflow runs `helm upgrade --install` for a temporary release,
  waits for PostgreSQL, MinIO, MinIO bucket initialization, observer migration,
  and observer rollout, then validates `/readyz` and `/healthz` through local
  port-forwarding.
- The manual release workflow deploys release `observer`, waits for the rollout,
  then runs a public route smoke against `https://loom.nj.cs.ac.cn:10062/` using
  a non-health endpoint expectation that can tolerate the route returning 404 at
  `/`.
