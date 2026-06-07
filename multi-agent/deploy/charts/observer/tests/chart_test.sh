#!/usr/bin/env bash
set -euo pipefail

CHART_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
rendered="$(helm template observer-test "$CHART_DIR" \
  --set gateway.enabled=true \
  --set gateway.host=observer.example.com \
  --set gateway.parentRefs[0].name=cilium-gateway \
  --set gateway.parentRefs[0].namespace=cilium-gateway)"

grep -q 'kind: HTTPRoute' <<<"$rendered"
grep -q 'apiVersion: gateway.networking.k8s.io/v1' <<<"$rendered"
grep -q 'observer.example.com' <<<"$rendered"
grep -q 'kind: Deployment' <<<"$rendered"
grep -q 'kind: Job' <<<"$rendered"
grep -q 'kind: CronJob' <<<"$rendered"
grep -q 'name: observer-test-observer-config' <<<"$rendered"
grep -q 'configMap:' <<<"$rendered"
