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
observer_service="$(awk '
  $0 == "---" {
    if (doc ~ /kind: Service/ && doc ~ /name: observer-test-observer/) {
      print doc
    }
    doc = ""
    next
  }
  { doc = doc $0 "\n" }
  END {
    if (doc ~ /kind: Service/ && doc ~ /name: observer-test-observer/) {
      print doc
    }
  }
' <<<"$rendered")"
grep -q 'app.kubernetes.io/component: observer' <<<"$observer_service"
grep -q 'name: observer-test-observer-migrate-0-1-0' <<<"$rendered"
grep -q 'name: observer-test-observer-postgresql' <<<"$rendered"
grep -q 'name: observer-test-observer-minio' <<<"$rendered"
grep -q 'storageClassName: csi-rbd-sc' <<<"$rendered"
grep -q 'name: observer-test-observer-minio-create-bucket' <<<"$rendered"
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
hooked_migration="$(awk '
  $0 == "---" {
    if (doc ~ /kind: Job/ && doc ~ /name: observer-test-observer-migrate/) {
      print doc
    }
    doc = ""
    next
  }
  { doc = doc $0 "\n" }
  END {
    if (doc ~ /kind: Job/ && doc ~ /name: observer-test-observer-migrate/) {
      print doc
    }
  }
' <<<"$hooked")"
if grep -q 'serviceAccountName:' <<<"$hooked_migration"; then
  echo "hooked migration job must not depend on chart-created service account" >&2
  exit 1
fi
grep -q 'resources:' <<<"$hooked_migration"
grep -q 'cpu: 50m' <<<"$hooked_migration"
grep -q 'memory: 128Mi' <<<"$hooked_migration"

agentserver_only="$(helm template observer-test "$CHART_DIR" \
  --set secret.create=true \
  --set secret.databaseUrl='postgres://observer:observer@postgres:5432/observer?sslmode=disable' \
  --set secret.s3AccessKey=minioadmin \
  --set secret.s3SecretKey=minioadmin \
  --set secret.telemetryKeys.telemetry-global-key=ops-secret \
  --set config.apiKeys=null \
  --set config.identity.legacyAPIKeys.enabled=false \
  --set config.identity.agentserver.enabled=true \
  --set config.identity.agentserver.url=https://agentserver.example.com \
  --set postgresql.enabled=false \
  --set minio.enabled=false)"
grep -q 'legacy_api_keys:' <<<"$agentserver_only"
grep -q 'enabled: false' <<<"$agentserver_only"
grep -q 'url: "https://agentserver.example.com"' <<<"$agentserver_only"

managed_stack="$(helm template observer-test "$CHART_DIR" \
  --set secret.create=true \
  --set secret.databaseUrl='postgres://observer:observer@observer-test-observer-postgresql:5432/observer?sslmode=disable' \
  --set secret.s3AccessKey=minioadmin \
  --set secret.s3SecretKey=minioadmin \
  --set secret.telemetryKeys.telemetry-global-key=ops-secret \
  --set config.identity.agentserver.enabled=true \
  --set config.identity.agentserver.url=https://agentserver.example.com \
  --set postgresql.enabled=true \
  --set postgresql.auth.username=observer \
  --set postgresql.auth.password=observer \
  --set postgresql.auth.database=observer \
  --set minio.enabled=true \
  --set minio.auth.rootUser=minioadmin \
  --set minio.auth.rootPassword=minioadmin)"
grep -q 'kind: StatefulSet' <<<"$managed_stack"
grep -q 'name: observer-test-observer-postgresql' <<<"$managed_stack"
grep -q 'POSTGRES_PASSWORD' <<<"$managed_stack"
grep -q 'name: observer-test-observer-minio' <<<"$managed_stack"
grep -q 'MINIO_ROOT_PASSWORD' <<<"$managed_stack"
grep -q 'name: observer-test-observer-minio-create-bucket' <<<"$managed_stack"
grep -q 'name: wait-for-postgresql' <<<"$managed_stack"
grep -q 'pg_isready -d "$OBSERVER_POSTGRES_WAIT_DSN"' <<<"$managed_stack"
grep -q 'image: "registry.nj.cs.ac.cn/dockerhub/postgres:16-alpine"' <<<"$managed_stack"
grep -q 'name: wait-for-observer-schema' <<<"$managed_stack"
grep -q 'psql "$OBSERVER_POSTGRES_WAIT_DSN"' <<<"$managed_stack"
grep -q 'SELECT 1 FROM telemetry_api_keys LIMIT 1' <<<"$managed_stack"

production_stack="$(helm template observer-prod "$CHART_DIR" \
  -f "$CHART_DIR/values-production.example.yaml" \
  --set existingSecret= \
  --set secret.create=true \
  --set "secret.clusterSecret=test-cluster-secret-32-chars-xxxx" \
  --set secret.databaseUrl='postgres://observer:observer@observer-prod-observer-postgresql:5432/observer?sslmode=disable' \
  --set secret.s3AccessKey=minioadmin \
  --set secret.s3SecretKey=minioadmin \
  --set secret.telemetryKeys.telemetry-global-key=ops-secret \
  --set postgresql.auth.password=observer \
  --set minio.auth.rootUser=minioadmin \
  --set minio.auth.rootPassword=minioadmin)"
grep -q 'max_bytes: 104857600' <<<"$production_stack"

production_observer="$(awk '
  $0 == "---" {
    if (doc ~ /kind: Deployment/ && doc ~ /name: observer-prod-observer/) {
      print doc
    }
    doc = ""
    next
  }
  { doc = doc $0 "\n" }
  END {
    if (doc ~ /kind: Deployment/ && doc ~ /name: observer-prod-observer/) {
      print doc
    }
  }
' <<<"$production_stack")"
grep -q 'cpu: 500m' <<<"$production_observer"
grep -q 'memory: 512Mi' <<<"$production_observer"
grep -q 'cpu: "2"' <<<"$production_observer"
grep -q 'memory: 2Gi' <<<"$production_observer"

production_postgresql="$(awk '
  $0 == "---" {
    if (doc ~ /kind: StatefulSet/ && doc ~ /name: observer-prod-observer-postgresql/) {
      print doc
    }
    doc = ""
    next
  }
  { doc = doc $0 "\n" }
  END {
    if (doc ~ /kind: StatefulSet/ && doc ~ /name: observer-prod-observer-postgresql/) {
      print doc
    }
  }
' <<<"$production_stack")"
grep -q 'cpu: "1"' <<<"$production_postgresql"
grep -q 'memory: 2Gi' <<<"$production_postgresql"
grep -q 'cpu: "2"' <<<"$production_postgresql"
grep -q 'memory: 8Gi' <<<"$production_postgresql"

production_minio="$(awk '
  $0 == "---" {
    if (doc ~ /kind: StatefulSet/ && doc ~ /name: observer-prod-observer-minio/) {
      print doc
    }
    doc = ""
    next
  }
  { doc = doc $0 "\n" }
  END {
    if (doc ~ /kind: StatefulSet/ && doc ~ /name: observer-prod-observer-minio/) {
      print doc
    }
  }
' <<<"$production_stack")"
grep -q 'cpu: 500m' <<<"$production_minio"
grep -q 'memory: 1Gi' <<<"$production_minio"
grep -q 'cpu: "2"' <<<"$production_minio"
grep -q 'memory: 8Gi' <<<"$production_minio"

# --- E2 validation guard tests ---

# Test E2.1: replicaCount > 1 with sqlite fails
echo "[test] E2.1 replicaCount > 1 + sqlite must fail"
out=$(helm template observer-test "$CHART_DIR" --set replicaCount=2 --set config.store.driver=sqlite 2>&1) && { echo "FAIL: expected fail; got success"; exit 1; }
echo "$out" | grep -q "replicaCount > 1 requires store.driver=postgres" || { echo "FAIL: error msg not found; got: $out"; exit 1; }

# Test E2.2: replicaCount > 1 without cluster.enabled fails
echo "[test] E2.2 replicaCount > 1 + cluster.enabled=false must fail"
out=$(helm template observer-test "$CHART_DIR" --set replicaCount=2 --set config.store.driver=postgres --set cluster.enabled=false 2>&1) && { echo "FAIL"; exit 1; }
echo "$out" | grep -q "replicaCount > 1 requires cluster.enabled=true" || { echo "FAIL: $out"; exit 1; }

# Test E2.3: cluster.enabled + secret.create without secret.clusterSecret fails
echo "[test] E2.3 cluster enabled + secret.create without clusterSecret must fail"
out=$(helm template observer-test "$CHART_DIR" --set replicaCount=2 --set config.store.driver=postgres --set cluster.enabled=true --set secret.create=true 2>&1) && { echo "FAIL"; exit 1; }
echo "$out" | grep -q "requires secret.clusterSecret" || { echo "FAIL: $out"; exit 1; }

# Test E2.4: clusterSecret too short fails
echo "[test] E2.4 clusterSecret < 32 chars must fail"
out=$(helm template observer-test "$CHART_DIR" --set replicaCount=2 --set config.store.driver=postgres --set cluster.enabled=true --set secret.create=true --set secret.clusterSecret=shortvalue 2>&1) && { echo "FAIL"; exit 1; }
echo "$out" | grep -q "must be >=32 chars" || { echo "FAIL: $out"; exit 1; }

echo "E2 validation tests passed"

# --- E5 cluster-mode rendering tests ---

# Block 1: Default (replicaCount=1) renders no cluster env or internal Service.
echo "[test] E5.1 default: no cluster env, no headless service, no internal port"
default="$(helm template observer-test "$CHART_DIR")"
! grep -q 'OBSERVER_CLUSTER_SECRET' <<<"$default" || { echo "FAIL: OBSERVER_CLUSTER_SECRET should not render in default"; exit 1; }
! grep -q 'observer-test-observer-headless' <<<"$default" || { echo "FAIL: headless service should not render in default"; exit 1; }
! grep -q 'containerPort: 8091' <<<"$default" || { echo "FAIL: containerPort 8091 should not render in default"; exit 1; }
echo "E5.1 passed"

# Block 2: Multi-pod with cluster.enabled renders envs + internal Service + strategy.
echo "[test] E5.2 multi-pod cluster: cluster env + headless service + strategy"
multi="$(helm template observer-test "$CHART_DIR" \
  --set replicaCount=2 \
  --set cluster.enabled=true \
  --set secret.create=true \
  --set "secret.clusterSecret=$(head -c 48 /dev/urandom | base64 | tr -d '+/=' | head -c 48)" \
  --set secret.databaseUrl='postgres://x' \
  --set secret.s3AccessKey=x --set secret.s3SecretKey=x \
  --set "secret.telemetryKeys.telemetry-global-key=x" \
  --set config.identity.legacyAPIKeys.enabled=true \
  --set "config.apiKeys[0].id=test" --set "config.apiKeys[0].key=test" \
  --set postgresql.enabled=false \
  --set minio.enabled=false)"
grep -q 'OBSERVER_CLUSTER_SECRET' <<<"$multi" || { echo "FAIL: OBSERVER_CLUSTER_SECRET missing"; exit 1; }
grep -q 'POD_IP' <<<"$multi" || { echo "FAIL: POD_IP env missing"; exit 1; }
grep -q 'observer-test-observer-headless' <<<"$multi" || { echo "FAIL: headless service name missing"; exit 1; }
grep -q 'clusterIP: None' <<<"$multi" || { echo "FAIL: clusterIP: None missing"; exit 1; }
grep -q 'containerPort: 8091' <<<"$multi" || { echo "FAIL: containerPort 8091 missing"; exit 1; }
grep -q 'name: assert-cluster-secret' <<<"$multi" || { echo "FAIL: assert-cluster-secret init container missing"; exit 1; }
grep -q 'maxUnavailable: 0' <<<"$multi" || { echo "FAIL: maxUnavailable: 0 missing in rolling strategy"; exit 1; }
echo "E5.2 passed"

# Block 3: Multi-pod without cluster.enabled fails fast (already covered by E2.2 but kept separate per spec).
echo "[test] E5.3 multi-pod without cluster.enabled fails fast"
out=$(helm template observer-test "$CHART_DIR" --set replicaCount=2 \
    --set config.store.driver=postgres 2>&1) && { echo "FAIL: expected fail-fast; got success"; exit 1; }
echo "$out" | grep -q 'cluster.enabled=true' || { echo "FAIL: cluster.enabled=true not in error: $out"; exit 1; }
echo "fail-fast detected as expected"
echo "E5.3 passed"

# Block 4: existingSecret + production values render fresh_ttl + revocation_channel
# into ConfigMap, and Secret is NOT rendered (existingSecret is set).
echo "[test] E5.4 existingSecret: production config renders into ConfigMap; no Secret"
prod="$(helm template observer-test "$CHART_DIR" \
  --set existingSecret=observer-prod-secret \
  -f "$CHART_DIR/values-production.example.yaml")"
configmap="$(awk '/^---$/{p=0} /kind: ConfigMap/{p=1} p' <<<"$prod")"
grep -q 'fresh_ttl: "30s"' <<<"$configmap" || { echo "FAIL: fresh_ttl missing from ConfigMap"; exit 1; }
grep -q 'revocation_channel: "postgres"' <<<"$configmap" || { echo "FAIL: revocation_channel missing from ConfigMap"; exit 1; }
# Secret was NOT rendered (existingSecret in use):
if grep -q 'kind: Secret' <<<"$prod"; then
  echo "FAIL: Secret should not render when existingSecret is set" >&2; exit 1
fi
echo "E5.4 passed"

# Block 5: secret.create=true + cluster.enabled + agentserver.enabled renders
# fresh_ttl + revocation_channel into chart-managed Secret.
echo "[test] E5.5 secret.create + cluster: fresh_ttl + revocation_channel in Secret"
secret_out="$(helm template observer-test "$CHART_DIR" \
  --set replicaCount=2 --set cluster.enabled=true --set secret.create=true \
  --set secret.clusterSecret=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA \
  --set secret.databaseUrl='postgres://x' \
  --set secret.s3AccessKey=x --set secret.s3SecretKey=x \
  --set "secret.telemetryKeys.telemetry-global-key=x" \
  --set config.identity.agentserver.enabled=true \
  --set config.identity.agentserver.url=https://agentserver.example.com \
  --set config.identity.agentserver.freshTTL='30s' \
  --set config.identity.agentserver.revocationChannel='enabled' \
  --set postgresql.enabled=false \
  --set minio.enabled=false)"
secret_yaml="$(awk '/^---$/{p=0} /kind: Secret/{p=1} p' <<<"$secret_out")"
grep -q 'fresh_ttl: "30s"' <<<"$secret_yaml" || { echo "FAIL: fresh_ttl missing from Secret"; exit 1; }
grep -q 'revocation_channel: "postgres"' <<<"$secret_yaml" || { echo "FAIL: revocation_channel missing from Secret"; exit 1; }
echo "E5.5 passed"

# Block 6: revocationChannel=disabled emits explicit revocation_channel: ""
echo "[test] E5.6 revocationChannel=disabled emits empty string"
disabled="$(helm template observer-test "$CHART_DIR" \
  --set replicaCount=2 --set cluster.enabled=true \
  --set secret.create=true \
  --set "secret.clusterSecret=$(head -c 48 /dev/urandom | base64 | tr -d '+/=' | head -c 48)" \
  --set secret.databaseUrl='postgres://x' \
  --set secret.s3AccessKey=x --set secret.s3SecretKey=x \
  --set "secret.telemetryKeys.telemetry-global-key=x" \
  --set config.identity.legacyAPIKeys.enabled=true \
  --set "config.apiKeys[0].id=test" --set "config.apiKeys[0].key=test" \
  --set config.identity.agentserver.revocationChannel='disabled' \
  --set postgresql.enabled=false \
  --set minio.enabled=false)"
grep -q 'revocation_channel: ""' <<<"$disabled" || { echo "FAIL: revocation_channel empty string missing for disabled"; exit 1; }
echo "E5.6 passed"

# Block 7: invalid revocationChannel value fails fast
echo "[test] E5.7 invalid revocationChannel fails fast"
out=$(helm template observer-test "$CHART_DIR" --set replicaCount=2 \
    --set cluster.enabled=true \
    --set config.identity.agentserver.revocationChannel='bogus' 2>&1) && { echo "FAIL: expected fail; got success"; exit 1; }
echo "$out" | grep -q 'must be auto' || { echo "FAIL: expected enum error; got: $out"; exit 1; }
echo "revocationChannel enum fail-fast OK"
echo "E5.7 passed"

echo "E5 cluster-mode tests passed"

# --- Finding 1: cluster.enabled in ConfigMap + env-field names in ConfigMap ---
echo "[test] F1.1 cluster.enabled: true appears in ConfigMap when cluster.enabled=true"
f1_multi="$(helm template observer-test "$CHART_DIR" \
  --set replicaCount=2 \
  --set cluster.enabled=true \
  --set secret.create=true \
  --set "secret.clusterSecret=$(openssl rand -hex 32)" \
  --set secret.databaseUrl='postgres://x' \
  --set secret.s3AccessKey=x --set secret.s3SecretKey=x \
  --set "secret.telemetryKeys.telemetry-global-key=x" \
  --set config.identity.legacyAPIKeys.enabled=true \
  --set "config.apiKeys[0].id=test" --set "config.apiKeys[0].key=test" \
  --set postgresql.enabled=false \
  --set minio.enabled=false)"
f1_configmap="$(awk '/^---$/{p=0} /kind: ConfigMap/{p=1} p' <<<"$f1_multi")"
grep -q 'cluster:' <<<"$f1_configmap" || { echo "FAIL: cluster: block missing from ConfigMap"; exit 1; }
grep -q 'enabled: true' <<<"$f1_configmap" || { echo "FAIL: cluster.enabled: true missing from ConfigMap"; exit 1; }
grep -q 'advertise_url_env:' <<<"$f1_configmap" || { echo "FAIL: advertise_url_env missing from ConfigMap"; exit 1; }
grep -q 'secret_env:' <<<"$f1_configmap" || { echo "FAIL: secret_env missing from ConfigMap"; exit 1; }
grep -q 'internal_listen_addr:' <<<"$f1_configmap" || { echo "FAIL: internal_listen_addr missing from ConfigMap"; exit 1; }
echo "F1.1 passed"

echo "Finding 1 chart tests passed"
