# Commander state-persistence k8s e2e

End-to-end test for the multi-pod commander login + session fix. Stands up
the production-shaped topology in a local minikube cluster and asserts the
cross-pod contracts that the unit + integration tests in
`internal/commanderhub/` cover at the Go level.

## Topology

```
                    +-----------------------+
                    | mock-agentserver (1×) |
                    +-----------+-----------+
                                |
                    +-----------+-----------+
                    | observer (3×, ClusterIP, NO sessionAffinity) |
                    +-----------+-----------+
                                |
                          postgres (1×)
```

The bug that motivated this fix: production `observer-server` with
`replicaCount: 3` behind a Service without `sessionAffinity` ⇒ POST /login
hits pod A, the 1.5 s-later GET /poll hits pod B which has no in-memory
state for the `login_id` ⇒ HTTP 404 "unknown login". This e2e reproduces
the exact topology (3 replicas, no sessionAffinity) and proves the fix.

## Prereqs

- `docker` running
- `minikube` ≥ 1.38
- `kubectl` in `$PATH` (any 1.31-ish client works)
- This repo checked out at the `worktree-commander-state-persistence` branch
  (or master after merge)

## Run

```bash
# 1. Start minikube (one-time per host).
minikube start --driver=docker --force --cpus=4 --memory=6g --kubernetes-version=v1.31.4

# 2. Build images INSIDE minikube's docker daemon so they're available
#    to the cluster without a registry push:
eval $(minikube docker-env)
docker build -f cmd/observer-server/Dockerfile -t observer-server:e2e .

# Mock agentserver — build the binary outside docker (uses repo go.mod),
# then bake into the small image:
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -trimpath -ldflags='-s -w' \
    -o tests/k8s_commander/mock-agentserver/mock-agentserver \
    ./tests/k8s_commander/mock-agentserver
docker build -t mock-agentserver:e2e tests/k8s_commander/mock-agentserver/

# 3. Apply manifests + run e2e:
kubectl apply -f tests/k8s_commander/manifests.yaml
kubectl -n commander-e2e wait --for=condition=Ready pod -l app=observer-server --timeout=180s
tests/k8s_commander/run_e2e.sh
```

Expected output ends with:

```
[e2e][PASS] step 1: POST /login on pod A returned login_id=...
[e2e][PASS] step 2: GET /poll on pod B returned 200 pending (pre-fix this would have been 404)
[e2e][PASS] step 3: [C1] returned 200 ok + Set-Cookie; sid prefix=...
[e2e][PASS] step 4: cookie authenticates on all 3 pods (cross-pod GetSession via shared DB)
[e2e][PASS] step 5: logout on pod A invalidates cookie on every pod
[e2e][PASS] step 6: cap holds (~1024) and enforces (some 429s); advisory lock works

[e2e] ALL 6 STEPS PASSED — multi-pod commander state persistence verified.
```

## What each step proves

| Step | Asserted property | Pre-fix behavior |
|---|---|---|
| 1 | POST /login on pod A → 200 + login_id | 200 (same) |
| 2 | GET /poll on **pod B** → 200 pending | **404 "unknown login"** ← the production bug |
| 3 | [C1] inline Set-Cookie 200 ok via Service round-robin | first wrong-pod /poll returned 404 |
| 4 | Cookie authenticates on **every pod** (cross-pod GetSession via shared DB) | only the pod that issued it accepted it |
| 5 | Logout on one pod invalidates cookie **everywhere** | other pods kept accepting it |
| 6 | 1100 concurrent POST /login → 200 count never exceeds MaxActiveLogins=1024, and some 429s observed → cap is enforced under real concurrency | per-pod 64-cap allowed 3× upstream amplification |

## Cleanup

```bash
kubectl delete namespace commander-e2e
# Optional — destroy minikube entirely:
minikube delete
```

## Re-running

The DB persists state between runs. Step 6 leaves ~1024 reservation rows;
either wait `loginTTL = 10min` for them to sweep, or:

```bash
kubectl -n commander-e2e exec deploy/postgres -- \
  psql -U observer -d observer -c 'TRUNCATE commander_logins, commander_sessions'
```
