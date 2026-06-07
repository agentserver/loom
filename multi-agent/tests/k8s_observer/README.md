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
