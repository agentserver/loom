#!/usr/bin/env bash
set -euo pipefail
# dry_run_all.sh — WT-3-prod-multidevice loopback fake-daemon smoke.
#
# Simulates 4 daemons (observer, driver, slave-A, slave-B/slave-C
# collapsed) on 127.0.0.1 ports 18091-18094 via python3 -m http.server.
# Uses a fake OAuth stub token (never a real one). Writes the harness
# smoke outputs to tests/eval/results/prod/dry_run_smoke/.
#
# HARNESS-ONLY: does NOT set ALLOW_PROD_DEPLOY, does NOT invoke real
# Phase 2 --mode prod, does NOT contact agent.cs.ac.cn, does NOT create
# any cloud droplet. Real physical devices are for
# paper/v3/p3-prod-multidevice-run.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
OUT_DIR="$REPO_ROOT/multi-agent/tests/eval/results/prod/dry_run_smoke"
mkdir -p "$OUT_DIR"

# Scratch dir for the fake OAuth token — OUTSIDE the repo tree so it
# cannot accidentally get committed. Cleaned on trap EXIT below.
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"; for pid in "${DAEMON_PIDS[@]:-}"; do kill "$pid" 2>/dev/null || true; done' EXIT

echo "<OAUTH_TOKEN_HERE_DO_NOT_COMMIT>" > "$SCRATCH/fake_token"

# Loopback fake-daemon simulation. `python3 -m http.server` binds all
# interfaces by default; --bind 127.0.0.1 forces loopback.
DAEMON_PIDS=()
for port in 18091 18092 18093 18094; do
    # We use a per-port temp dir to avoid python listing $SCRATCH files
    # (which is unavoidable HTTP-served content of http.server).
    d="$(mktemp -d)"
    python3 -m http.server "$port" --bind 127.0.0.1 --directory "$d" \
        >/dev/null 2>&1 &
    DAEMON_PIDS+=("$!")
done

# Give the daemons a moment to bind. If a bind fails (port already
# in use) we shouldn't fatally abort the smoke — this is a HARNESS-
# ONLY smoke and the outputs it writes are what we're actually
# testing. The topology.json / prod_vs_stub / analysis-template
# assertions all work regardless of whether the fake HTTP servers
# happened to bind.
sleep 0.5

# --- Emit topology_used.json with hostname redacted (spec §7(g)) ---
HOSTNAME_INPUT="${HOSTNAME:-$(hostname 2>/dev/null || echo unknown)}"
HOSTNAME_SHA8="$(printf '%s' "$HOSTNAME_INPUT" | sha256sum | cut -c1-8)"

TOPO_ENUM="${LOOM_TOPO_ENUM:-4-device}"

cat > "$OUT_DIR/topology_used.json" <<JSON
{
  "topology_enum": "$TOPO_ENUM",
  "generated_by": "dry_run_all.sh",
  "harness_only": true,
  "hosts": [
    {"role": "driver",           "device_kind": "laptop",   "hostname_sha8": "$HOSTNAME_SHA8"},
    {"role": "observer,slave-a", "device_kind": "headless", "hostname_sha8": "$HOSTNAME_SHA8"},
    {"role": "slave-b",          "device_kind": "windows",  "hostname_sha8": "$HOSTNAME_SHA8"},
    {"role": "slave-c",          "device_kind": "cloud",    "hostname_sha8": "$HOSTNAME_SHA8"}
  ]
}
JSON

# --- Ensure fake_prod fixture is materialised ---
FIXTURE_DIR="$SCRIPT_DIR/fixtures/fake_prod"
[[ -d "$FIXTURE_DIR" ]] || { echo "dry_run_all.sh: missing fixture $FIXTURE_DIR" >&2; exit 3; }

# --- Run build_prod_vs_stub in --sample-mode ---
STUB_TABLE="$REPO_ROOT/multi-agent/tests/eval/results/smoke/paper/table2_sample.csv"
python3 "$SCRIPT_DIR/build_prod_vs_stub.py" \
    --prod-dir "$FIXTURE_DIR" \
    --stub-table "$STUB_TABLE" \
    --out "$OUT_DIR/prod_vs_stub_sample.csv" \
    --sample-mode \
    --generated-at "1970-01-01T00:00:00Z"

echo "dry_run_all.sh: smoke complete → $OUT_DIR"
