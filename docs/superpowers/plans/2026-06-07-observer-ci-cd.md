# Observer CI/CD Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build GitHub Actions CI/CD and a Helm-managed observer stack with PostgreSQL and MinIO.

**Architecture:** GitHub Actions has one CI workflow for local checks and one deploy workflow for image build, registry push, isolated smoke deploy, and manual release deploy. Helm owns observer, PostgreSQL, MinIO, PVCs, bucket initialization, migration, and retention jobs, while HTTPRoute remains platform-managed.

**Tech Stack:** GitHub Actions, Docker, Go 1.26, Helm 3, Kubernetes, PostgreSQL 16, MinIO, `csi-rbd-sc`.

---

### Task 1: Helm Managed Data Services

**Files:**
- Modify: `multi-agent/deploy/charts/observer/values.yaml`
- Modify: `multi-agent/deploy/charts/observer/values-production.example.yaml`
- Modify: `multi-agent/deploy/charts/observer/templates/_helpers.tpl`
- Modify: `multi-agent/deploy/charts/observer/templates/secret.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/postgresql.yaml`
- Create: `multi-agent/deploy/charts/observer/templates/minio.yaml`
- Modify: `multi-agent/deploy/charts/observer/tests/chart_test.sh`

- [x] Add failing chart render checks for PostgreSQL StatefulSet, MinIO StatefulSet, PVC storage class `csi-rbd-sc`, and bucket init Job.
- [x] Add values for chart-managed PostgreSQL and MinIO.
- [x] Render PostgreSQL Service and StatefulSet with credentials from the observer config Secret.
- [x] Render MinIO Service, StatefulSet, and bucket creation Job with credentials from the observer config Secret.
- [x] Extend Secret rendering with PostgreSQL and MinIO credential keys when `secret.create=true`.
- [x] Run `PATH=/tmp/observer-helm-bin:$PATH ./deploy/charts/observer/tests/chart_test.sh`.

### Task 2: Observer Image Build

**Files:**
- Create: `multi-agent/cmd/observer-server/Dockerfile`

- [x] Add a multi-stage Dockerfile that builds `./cmd/observer-server` with `CGO_ENABLED=0`.
- [x] Use `golang:1.26-bookworm` for build and `debian:bookworm-slim` for runtime with CA certificates.
- [x] Run `docker build -f cmd/observer-server/Dockerfile -t observer-server:test .`.

### Task 3: GitHub Actions CI

**Files:**
- Modify: `.github/workflows/multi-agent.yml`

- [x] Update Go setup to use `multi-agent/go.mod`.
- [x] Add `go mod tidy -diff`.
- [x] Add Helm install/lint/render test steps.
- [x] Add Python unit test steps for `multi-agent/python`.
- [x] Keep contract tests.

### Task 4: GitHub Actions Deploy

**Files:**
- Create: `.github/workflows/observer-deploy.yml`

- [x] Add `workflow_dispatch` with `target=smoke|release` and `push` on `master`.
- [x] Login to `registry.nj.cs.ac.cn:10062` with `REGISTRY_NJ_USERNAME` and `REGISTRY_NJ_PASSWORD`.
- [x] Build and push `registry.nj.cs.ac.cn:10062/loom/observer:${{ github.sha }}` and `master-latest`.
- [x] Write `KUBECONFIG_NJ_PROD` to `~/.kube/config`.
- [x] For push/manual smoke, deploy `observer-ci-${{ github.run_number }}` with generated credentials, 1Gi PVCs, and no HTTPRoute.
- [x] Smoke `observer-ci-*` through `kubectl port-forward svc/<release>-observer 18190:8090` and `/readyz`.
- [x] Always uninstall the smoke release and delete PVCs labeled with its release instance.
- [x] For manual release, generate a temporary secret values JSON file from GitHub Secrets.
- [x] Run `helm upgrade --install observer ./multi-agent/deploy/charts/observer -n dev-yuzishu`.
- [x] Wait for PostgreSQL, MinIO, migration Job, bucket Job, and observer rollout.
- [x] Curl `https://loom.nj.cs.ac.cn:10062/` only for manual release and accept reachable HTTP status.

### Task 5: Verification

**Files:**
- Modify as needed from prior tasks.

- [x] Run `go test ./cmd/observer-server -count=1`.
- [x] Run `PATH=/tmp/observer-helm-bin:$PATH helm lint ./deploy/charts/observer`.
- [x] Run `PATH=/tmp/observer-helm-bin:$PATH ./deploy/charts/observer/tests/chart_test.sh`.
- [x] Run `go test ./... -count=1`.
- [x] Run `go mod tidy -diff`.
- [x] Run `git diff --check`.
- [x] Report external resources still required: GitHub Secrets and HTTPRoute platform ownership. The old live-smoke release-name resources have already been deleted.
