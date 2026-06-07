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
grep -q 'name: observer-test-observer-migrate-0-1-0' <<<"$rendered"
if grep -q 'helm.sh/hook' <<<"$rendered"; then
  echo "default migration job must not render helm hook annotations" >&2
  exit 1
fi

named="$(helm template observer-test "$CHART_DIR" \
  --set migration.jobNameSuffix=schema-20260607)"
grep -q 'name: observer-test-observer-migrate-schema-20260607' <<<"$named"

long_release="observer-release-name-123456789012345678901234567890"
long_a="$(helm template "$long_release" "$CHART_DIR" \
  --set migration.jobNameSuffix=alpha20260607)"
long_b="$(helm template "$long_release" "$CHART_DIR" \
  --set migration.jobNameSuffix=beta20260607)"
grep -q 'migrate-alpha20260607' <<<"$long_a"
grep -q 'migrate-beta20260607' <<<"$long_b"
retention_name="$(awk '
  $0 == "kind: CronJob" { in_cronjob = 1; next }
  in_cronjob && $0 == "metadata:" { in_metadata = 1; next }
  in_cronjob && in_metadata && /^  name: / { print $2; exit }
' <<<"$long_a")"
if [[ "$retention_name" != *-retention ]]; then
  echo "retention CronJob name must keep -retention suffix: $retention_name" >&2
  exit 1
fi
if (( ${#retention_name} > 52 )); then
  echo "retention CronJob name exceeds 52 chars: $retention_name" >&2
  exit 1
fi

hooked="$(helm template observer-test "$CHART_DIR" \
  --set migration.useHelmHook=true)"
grep -q 'name: observer-test-observer-migrate' <<<"$hooked"
grep -q 'helm.sh/hook' <<<"$hooked"

agentserver_only="$(helm template observer-test "$CHART_DIR" \
  --set secret.create=true \
  --set secret.databaseUrl='postgres://observer:observer@postgres:5432/observer?sslmode=disable' \
  --set secret.s3AccessKey=minioadmin \
  --set secret.s3SecretKey=minioadmin \
  --set secret.telemetryKeys.telemetry-global-key=ops-secret \
  --set config.apiKeys=null \
  --set config.identity.legacyAPIKeys.enabled=false \
  --set config.identity.agentserver.enabled=true \
  --set config.identity.agentserver.url=https://agentserver.example.com)"
grep -q 'legacy_api_keys:' <<<"$agentserver_only"
grep -q 'enabled: false' <<<"$agentserver_only"
grep -q 'url: "https://agentserver.example.com"' <<<"$agentserver_only"
