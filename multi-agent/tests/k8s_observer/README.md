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

Render check:

```bash
cd /root/multi-agent/.worktrees/observer-postgres-k8s-design
multi-agent/tests/k8s_observer/smoke.sh
```

The smoke script reads the `dev-yuzishu` namespace for pods and rollout status.
Chart lint and template rendering are non-mutating checks.

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
Current full smoke reaches the rollout check and then stops because release
`observer` is not deployed in `dev-yuzishu`:

```text
Error from server (NotFound): deployments.apps "observer-observer" not found
```

Telemetry gate curl checks still require a deployed observer URL, an agent
token, and an operations telemetry key.
