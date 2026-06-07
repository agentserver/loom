# Observer k8s-nj-prod Smoke

Acceptance cluster:

```yaml
- cluster:
    server: https://k8s-prod.nj.cs.ac.cn
  name: k8s-nj-prod
```

All real deployment and smoke commands must use:

```bash
kubectl --context k8s-nj-prod
helm --kube-context k8s-nj-prod
```

Render check:

```bash
cd /root/multi-agent/.worktrees/observer-postgres-k8s-design
multi-agent/tests/k8s_observer/smoke.sh
```

The smoke script requires read access to the `observer` namespace for pods and
rollout status. Chart lint and template rendering are non-mutating checks.

## 2026-06-07 Verification

Completed non-mutating checks on `k8s-nj-prod`:

```bash
kubectl --context k8s-nj-prod config current-context
helm --kube-context k8s-nj-prod lint multi-agent/deploy/charts/observer
helm --kube-context k8s-nj-prod template observer multi-agent/deploy/charts/observer -n observer -f multi-agent/deploy/charts/observer/values-production.example.yaml >/tmp/observer-rendered.yaml
grep -q 'kind: HTTPRoute' /tmp/observer-rendered.yaml
```

Result: context printed `k8s-nj-prod`, Helm lint passed, render passed, and
the rendered manifest included `HTTPRoute`.

Full smoke is currently blocked for this kube user:

```text
Error from server (Forbidden): namespaces "observer" is forbidden: User "yuzishu15@mails.ucas.ac.cn" cannot get resource "namespaces" in API group "" in the namespace "observer"
```

Telemetry gate curl checks still require a deployed observer URL, an agent
token, and an operations telemetry key.
