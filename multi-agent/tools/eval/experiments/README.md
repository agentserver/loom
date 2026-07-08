# tools/eval/experiments/

Per-workload runner scripts for the Phase 3 fulltable harness under
the codex-only baseline (`single_machine_codex`).

Each script pins one `workload_id`, applies workload-specific
preflight, and forwards remaining args to
`tools/eval/fulltable/run.sh --workload <id> --results-root <dir>`.

## Wrappers

| Script | Workload | Extra preflight | Wall-clock/rep (approx) |
|---|---|---|---|
| `cross_device_code_mod.sh` | `cross-device-code-mod` | codex bin + config | ~3 min |
| `missing_parser_converter.sh` | `missing-parser-converter` | codex bin + config | ~3 min |
| `remote_data_processing.sh` | `remote-data-processing` | + writable-tmp + fixture | ~4 min |
| `windows_only_artifact.sh` | `windows-only-artifact` | + `require_windows_host` (or `--skip-if-not-windows`) | ~3 min |
| `credential_bound_model.sh` | `credential-bound-model` | + route (a) required, route (b) unsupported here | ~5 min |

## Common flags

- `--dry-run` — forward to `run.sh --dry-run`; no dispatch.
- `--sample N` — sample cap 3 unless `ALLOW_FULL_RUN=1` (enforced by `run.sh`).
- `--results-root <abs-dir>` — override the default per-invocation results directory. Absolute, allowlisted.
- `-h|--help` — usage.

## Not supported

- `--reps` and `--config-subset` are RESERVED for a follow-up worktree. See `docs/specs/wt4-codex-only.handoff.md`.
- Caller-supplied `--workload` / `--filter-workload` (space or equals form) → wrappers exit 2 (pinned).

## Security notes

- Preflight NEVER invokes `codex`. Only `command -v codex >/dev/null`.
- Config parsing is read-only (via `python3 tomllib`). Helpers emit only `present` / `absent` / `error` — the wrappers never print the configured token value or the configured env-var name. (Static key names like `experimental_bearer_token` appear in wrapper messages as documentation, not as values.)
- Wrappers write only under `--results-root <dir>` (allowlisted). The default results-root is `<git-root>/multi-agent/tests/eval/results/experiments/<workload>/<isodate>-<pid>/` which is `.gitignored`.
- Route (b) [`env_key`] is DETECTED for operator visibility but DECLARED UNSUPPORTED in this PR — see spec §4.4 + handoff.

## Test seams

- `LOOM_FULLTABLE_WRAPPER_SHIM=1` (+ `--dry-run`) — dual-guard: `run.sh` prints `SHIM_WORKLOAD_FILTER` / `SHIM_RESULTS_ROOT` and exits 0 without dispatch or commit-tree preflight.
- `LOOM_CODEX_CONFIG_PATH` — overrides `~/.codex/config.toml` (test fixtures under `tests/fixtures/codex_config/`).
- `_UNAME_STUB_OUT` (via a shadowed `uname` in `$PATH`) — parametrizes Windows-host detection in `test_windows_guard.sh`.
