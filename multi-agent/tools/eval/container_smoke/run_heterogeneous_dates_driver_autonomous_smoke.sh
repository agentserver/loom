#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
OUT_ROOT="${1:-/root/paper_writing/paper_outputs/multi_agent_driver_autonomous_smoke}"

DRIVER_PROMPT_MODE=autonomous \
  "${ROOT_DIR}/run_heterogeneous_dates_driver_mcp_smoke.sh" "$OUT_ROOT"
