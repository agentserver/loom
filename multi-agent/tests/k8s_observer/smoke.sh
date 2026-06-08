#!/usr/bin/env bash
set -euo pipefail

CTX="${KUBE_CONTEXT:-k8s-nj-prod}"
RELEASE="${OBSERVER_RELEASE:-observer}"
NAMESPACE="${OBSERVER_NAMESPACE:-dev-yuzishu}"
CHART="${OBSERVER_CHART:-multi-agent/deploy/charts/observer}"
VALUES="${OBSERVER_VALUES:-multi-agent/deploy/charts/observer/values-production.example.yaml}"

kubectl --context "$CTX" get namespace "$NAMESPACE" >/dev/null
helm --kube-context "$CTX" lint "$CHART"
helm --kube-context "$CTX" template "$RELEASE" "$CHART" -n "$NAMESPACE" -f "$VALUES" >/tmp/observer-rendered.yaml
grep -q 'kind: Deployment' /tmp/observer-rendered.yaml
grep -q "name: $RELEASE-observer" /tmp/observer-rendered.yaml
grep -q "name: $RELEASE-observer-postgresql" /tmp/observer-rendered.yaml
grep -q "name: $RELEASE-observer-minio" /tmp/observer-rendered.yaml
grep -q "name: $RELEASE-observer-minio-create-bucket" /tmp/observer-rendered.yaml
kubectl --context "$CTX" -n "$NAMESPACE" get pods
kubectl --context "$CTX" -n "$NAMESPACE" rollout status "deploy/$RELEASE-observer" --timeout=180s
