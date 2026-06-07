# Observer k8s-nj-prod Smoke

Acceptance cluster:

```yaml
- cluster:
    server: https://k8s-prod.nj.cs.ac.cn
  name: k8s-nj-prod
```

All real deployment and smoke commands must use context `k8s-nj-prod` and
namespace `dev-yuzishu`:

```bash
kubectl --context k8s-nj-prod
helm --kube-context k8s-nj-prod
```

Production observer is exposed publicly at:

```text
https://loom.nj.cs.ac.cn:10062/
```

Use this URL for production smoke/e2e API traffic. Do not require a local
`kubectl port-forward` for driver/slave or manual API checks once this public
route is available. Port-forwarding remains only a fallback for disposable
local-runner debugging before the production route exists.

The cluster should pull public images through the Nanjing registry mirror:

```text
docker.io/<image> -> registry.nj.cs.ac.cn/dockerhub/<image>
ghcr.io/<image>   -> registry.nj.cs.ac.cn/ghcr/<image>
```

Examples used by the live smoke manifest:

```text
postgres:16-alpine -> registry.nj.cs.ac.cn/dockerhub/postgres:16-alpine
minio/minio:latest -> registry.nj.cs.ac.cn/dockerhub/minio/minio:latest
alpine:3.20        -> registry.nj.cs.ac.cn/dockerhub/alpine:3.20
```

Render check:

```bash
cd /root/multi-agent/.worktrees/observer-postgres-k8s-design
multi-agent/tests/k8s_observer/smoke.sh
```

The smoke script reads the `dev-yuzishu` namespace for pods and rollout status.
Chart lint and template rendering are non-mutating checks.

## Live Test Stack

`live-smoke.yaml` is a disposable test stack for the acceptance namespace. It
creates:

- `observer-postgres`: PostgreSQL 16 with `emptyDir` storage for live smoke.
- `observer-minio`: S3-compatible object storage with an `emptyDir` bucket.
- `observer-minio-create-bucket`: one-shot bucket creation Job.
- `observer-observer`: an Alpine runner that receives a locally built
  `observer-server` binary via `kubectl cp`.

This path is only for real smoke/e2e before a project observer image is
published to the registry. The production deployment path is the Helm chart.

Required Secret:

```bash
kubectl --context k8s-nj-prod -n dev-yuzishu create secret generic observer-live-smoke \
  --from-literal=postgres-user=observer \
  --from-literal=postgres-password='<password>' \
  --from-literal=database-url='postgres://observer:<password>@observer-postgres:5432/observer?sslmode=disable' \
  --from-literal=minio-access-key='<access-key>' \
  --from-literal=minio-secret-key='<secret-key>' \
  --from-literal=telemetry-key='<ops-telemetry-key>' \
  --from-file=observer.yaml=/path/to/observer.yaml
```

Deploy and inject the local binary:

```bash
kubectl --context k8s-nj-prod -n dev-yuzishu apply -f multi-agent/tests/k8s_observer/live-smoke.yaml
kubectl --context k8s-nj-prod -n dev-yuzishu rollout status deploy/observer-minio --timeout=180s
kubectl --context k8s-nj-prod -n dev-yuzishu rollout status deploy/observer-observer --timeout=180s
kubectl --context k8s-nj-prod -n dev-yuzishu rollout status statefulset/observer-postgres --timeout=180s

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o /tmp/observer-server.linux-amd64 ./multi-agent/cmd/observer-server
kubectl --context k8s-nj-prod -n dev-yuzishu cp /tmp/observer-server.linux-amd64 \
  deploy/observer-observer:/work/observer-server
kubectl --context k8s-nj-prod -n dev-yuzishu exec deploy/observer-observer -- \
  /work/observer-server --config /etc/observer/observer.yaml --migrate-only
kubectl --context k8s-nj-prod -n dev-yuzishu exec deploy/observer-observer -- \
  sh -c 'nohup /work/observer-server --config /etc/observer/observer.yaml >/work/observer.log 2>&1 &'
```

## 2026-06-07 Verification

Completed non-mutating checks on `k8s-nj-prod`:

```bash
kubectl --context k8s-nj-prod config current-context
helm --kube-context k8s-nj-prod lint multi-agent/deploy/charts/observer
helm --kube-context k8s-nj-prod template observer multi-agent/deploy/charts/observer -n dev-yuzishu -f multi-agent/deploy/charts/observer/values-production.example.yaml >/tmp/observer-rendered.yaml
grep -q 'kind: HTTPRoute' /tmp/observer-rendered.yaml
```

Result: context printed `k8s-nj-prod`, Helm lint passed, render passed, and
the rendered manifest included `HTTPRoute`.

Full smoke in `observer` namespace was blocked for this kube user:

```text
Error from server (Forbidden): namespaces "observer" is forbidden: User "yuzishu15@mails.ucas.ac.cn" cannot get resource "namespaces" in API group "" in the namespace "observer"
```

`dev-yuzishu` namespace permissions were checked and allow pod/deployment reads:

```bash
kubectl --context k8s-nj-prod get namespace dev-yuzishu
kubectl --context k8s-nj-prod auth can-i get pods -n dev-yuzishu
kubectl --context k8s-nj-prod auth can-i get deployments -n dev-yuzishu
```

Result: namespace exists, pods read is `yes`, deployments read is `yes`.
Before the live stack was applied, full smoke reached the rollout check and
then stopped because release `observer` was not deployed in `dev-yuzishu`:

```text
Error from server (NotFound): deployments.apps "observer-observer" not found
```

After applying `live-smoke.yaml`, building the local observer binary, copying it
into the runner pod, and running migrations against in-cluster PostgreSQL:

```bash
PATH=/tmp/observer-helm-bin:$PATH multi-agent/tests/k8s_observer/smoke.sh
```

Result: Helm lint passed, HTTPRoute rendered, the namespace pod listing
returned the live stack, and `deploy/observer-observer` reached ready state.

Manual API smoke initially used local port-forwards to `svc/observer-observer`
and `svc/observer-minio`:

```text
/readyz -> ready
/api/events telemetry gate -> 403 without key, 403 with invalid key, 202 with valid operations key
artifact direct PUT -> complete -> presigned GET -> downloaded bytes matched
write direct PUT -> complete -> list -> presigned GET -> downloaded bytes matched
```

Codex prod e2e was also run against the k8s observer using the rebuilt
`tests/prod_test/bin` amd64 binaries and the local `driver-codex-local` and
`slave-codex-local` configs pointed at the observer port-forward. The driver
delegated to `slave-codex-local`, the slave returned exactly `K8SOK`, and the
observer PostgreSQL database recorded driver/slave registrations in
`ws-local-codex`.

After production mode exposed `https://loom.nj.cs.ac.cn:10062/`, future
driver/slave e2e should point `observer.url` at that public URL and skip local
port-forward setup. The agentserver-only identity e2e was rerun with legacy
observer API-key registration disabled; the driver and slave were freshly
registered through agentserver, observer accepted only agentserver identity, and
the final slave output was exactly `K8SOK`.

Cleanup:

```bash
kubectl --context k8s-nj-prod -n dev-yuzishu delete -f multi-agent/tests/k8s_observer/live-smoke.yaml
kubectl --context k8s-nj-prod -n dev-yuzishu delete secret observer-live-smoke
```
