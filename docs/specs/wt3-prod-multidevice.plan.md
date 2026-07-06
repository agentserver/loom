# WT-3-prod-multidevice — Plan

> Companion to `docs/specs/wt3-prod-multidevice.spec.md`.
> Branch: `paper/v3/p3-prod-multidevice`. Base: `origin/paper/v3-integration` HEAD `1748802`.
> **Stage 2 plan.** Stage-3 review judges the diff against this plan +
> the spec's security items (a)–(k).
>
> **Scope reminder** (verbatim from spec §0): 本 worktree = harness + config
> + dry-run smoke，不接真设备、不做真 OAuth、不创建/不使用/不销毁真云
> droplet。真设备真跑归后续 worktree `paper/v3/p3-prod-multidevice-run`。
> **Plan MUST NOT include** real OAuth device flow execution, real cloud
> droplet creation, real workspace_id allocation, or real cross-machine
> tunnel establishment.

---

## Step order

Each step lists the files it touches (all under `multi-agent/`), key
behavior, and the pytest / bats / shell tests that light up at its end.
Steps are ordered so every test can pass at its own completion — no
"tests that pass only after step N+3" back-references.

### Step 1 — Per-device config schema + topology enum

Files:

- `tests/prod_test/multidevice/topology.schema.json` — JSON schema
  (draft-07). Defines: `topology_enum` ∈ `{4-device, 3-device-nowin,
  3-device-nocloud, 2-device-min}`; per-device sub-schemas for
  `laptop`, `headless`, `windows`, `cloud`; `workloads` array
  (constrained to size ≤ 2 whose items ∈ `{cross-device-code-mod,
  windows-only-artifact}`); `per_workload_reps` (int, default 3);
  `cloud.vendor` enum ∈ `{digitalocean, e2b, vps}`; `oauth_placeholder`
  field required to equal literal `<OAUTH_TOKEN_HERE_DO_NOT_COMMIT>`
  (so a real token value fails schema validation).
- `tests/prod_test/multidevice/laptop.yaml.template` — driver device;
  path (a) local proxy config; port 18092 bind `127.0.0.1`; role:
  `driver`.
- `tests/prod_test/multidevice/headless.yaml.template` — observer :18091
  + slave-A :18093; role: `observer,slave-a`.
- `tests/prod_test/multidevice/windows.yaml.template` — slave-B :18094;
  role: `slave-b`; workload capability advertises windows-only artifact
  tooling (dummy strings — no real PowerShell modules probed here).
- `tests/prod_test/multidevice/cloud.yaml.template` — slave-C; single
  `vendor:` line; role: `slave-c`.
- `tests/prod_test/multidevice/topologies/4-device.yaml.sample`
- `tests/prod_test/multidevice/topologies/3-device-nowin.yaml.sample`
- `tests/prod_test/multidevice/topologies/3-device-nocloud.yaml.sample`
- `tests/prod_test/multidevice/topologies/2-device-min.yaml.sample`
- `tests/prod_test/multidevice/tests/__init__.py`
- `tests/prod_test/multidevice/tests/test_schema.py` — validates every
  `*.yaml.template` + every `*.yaml.sample` against the schema; asserts
  positive on the four enum values and negative on unknown enum /
  workload set of size 3 / unknown workload name / real-looking OAuth
  token in `oauth_placeholder`.

Tests light up here:
`test_schema_positive_all_templates`,
`test_schema_positive_all_topology_enums`,
`test_workload_hard_cap`,
`test_workload_name_whitelist`,
`test_oauth_placeholder_literal_required`,
`test_cloud_vendor_yaml_single_line`.

### Step 2 — Startup wrapper skeletons

Files:

- `tests/prod_test/multidevice/wrappers/laptop_up.sh`,
  `wrappers/headless_up.sh`, `wrappers/cloud_up.sh` — bash;
  `set -euo pipefail` first line; parse `--config <path>`; read
  the corresponding YAML; guard block:
  ```bash
  MODE="${LOOM_DEPLOY_MODE:-stub}"
  if [[ "$MODE" == "prod" ]] && [[ "${ALLOW_PROD_DEPLOY:-0}" != "1" ]]; then
      echo "wrapper: ALLOW_PROD_DEPLOY not set; falling back to --mode stub" >&2
      MODE=stub
  fi
  exec /path/to/deploy.sh --mode "$MODE" --observer-port ... ...
  ```
  (`exec` is the only external call; wrappers never build state locally
  beyond argument marshalling.)
- `tests/prod_test/multidevice/wrappers/windows_up.ps1` — PowerShell 7+;
  `Set-ExecutionPolicy -Scope Process Bypass` (exactly this — no
  LocalMachine / CurrentUser); parses `-Config <path>`; mirrors the
  `ALLOW_PROD_DEPLOY` gate before invoking `deploy.ps1 -Mode`.
- `tests/prod_test/multidevice/wrappers/README.md` — usage examples;
  scope banner.
- `tests/prod_test/multidevice/tests/test_wrappers.py` — parses each
  wrapper for the gate + the `Set-ExecutionPolicy` scope + bind values.

Tests light up here:
`test_no_prod_deploy_without_env`,
`test_windows_execpolicy_scope_process`,
`test_tunnel_no_zero_bind` (parses `--observer-port` / `--driver-port`
arguments in the wrappers and asserts host is always `127.0.0.1`).

### Step 3 — `dry_run_all.sh` (loopback fake-daemon smoke)

Files:

- `tests/prod_test/multidevice/dry_run_all.sh` — bash. Spawns 4 stub
  daemons via Python one-liner `python3 -m http.server` on `127.0.0.1`
  ports 18091–18094; writes fake OAuth stub token to a scratch dir
  that is `mktemp -d`'d **outside** the repo (never under
  `multidevice/tokens/`); calls each wrapper with `LOOM_DEPLOY_MODE=stub`
  (never sets `ALLOW_PROD_DEPLOY`); collects fake N=3 metric rows
  per workload; writes `dry_run_smoke/topology_used.json` (hostname
  hashed); teardown kills the four background PIDs on trap EXIT.
- `tests/prod_test/multidevice/fixtures/fake_prod/` — small JSON fixtures
  representing 2 workloads × 4 metrics × N=3 rows.
- `tests/prod_test/multidevice/tests/test_dry_run.py` — runs the shell
  script under pytest, asserts the three output files exist, asserts
  no real OAuth token or `sk-` string in any output, asserts
  `topology_used.json.hosts[*].hostname_sha8` matches
  `sha256("alice-laptop")[:8]` when the fake hostname is fed in via the
  script's `HOSTNAME` env override.

Tests light up here:
`test_dry_run_smoke_produces_outputs`,
`test_topology_used_hostname_redacted`.

### Step 4 — Tunnel + workspace coordination doc

Files:

- `tests/prod_test/multidevice/README.md` — includes:
  - Scope banner (mirrors spec §0).
  - Per-device wrapper usage examples.
  - `## workspace_id lifecycle` section: 1 workspace per experiment
    session, destroyed via `teardown.sh --execute` in the real-run
    worktree; this worktree never creates one.
  - `## Tunnel coordination`: describes agentserver-signed tunnel
    endpoints (never hardcoded); links to `E2E_RUNBOOK.md` Multi-device
    section.
  - `## Handoff` pointer to `paper/v3/p3-prod-multidevice-run`.
- `tests/prod_test/multidevice/tests/test_readme.py` — asserts required
  sections + follow-up worktree name.

Tests light up here:
`test_workspace_id_lifecycle_documented`,
`test_readme_names_followup_worktree`.

### Step 5 — `build_prod_vs_stub.py` (CLI + fake-input unit test)

Files:

- `tests/prod_test/multidevice/build_prod_vs_stub.py` — Python 3.10+;
  argparse; per spec §5:
  - `--stub-table` path must equal one of the two allow-listed paths;
    else exit 2 with clear message.
  - Reads stub CSV (via `csv` stdlib), computes SHA256 of file bytes.
  - `--sample-mode` uses fixture from `fixtures/fake_prod/`.
  - Writes output CSV with comment-header meta (stub path, SHA, prod
    dir, generated_at from `--generated-at` flag, script_version).
  - Computes `abs_diff`, `rel_diff_pct`, `verdict` per metric rules
    from spec §5.4.
- `tests/prod_test/multidevice/tests/test_build_prod_vs_stub.py` —
  invokes `--sample-mode`, asserts column header, at least 8 rows
  (2 workloads × 4 metrics), verdict values ∈ enum, stub SHA meta
  matches independent `sha256sum`.

Tests light up here:
`test_build_prod_vs_stub_sample_columns`,
`test_stub_source_pinned`,
`test_stub_source_allowlist_reject`.

### Step 6 — `analysis_template.md` + required-section grep

Files:

- `tests/eval/results/prod/dry_run_smoke/analysis_template.md` — per
  spec §6:
  - First paragraph: 4 literal scope lines (grep -qF each).
  - 4 metric-header `##` sections.
  - 4 root-cause candidate names embedded (in the shared "根因候选"
    checklist under every metric section).
  - A dedicated `## 降级 2 设备 影响段` fallback impact section
    (spec §2.2 requires this section any time `topology_enum` is
    `2-device-min`; the template ships the section pre-authored so the
    real-run worktree only fills concrete numbers — never has to add
    a new heading).
  - Trailing disclaimer naming the follow-up worktree.
- `tests/prod_test/multidevice/tests/test_analysis_template.py` — grep
  -qF style assertions (uses `pathlib.read_text().splitlines()`
  contains checks — no shell subprocess needed).

Tests light up here:
`test_analysis_template_scope_declaration_lines`,
`test_analysis_template_metric_sections`,
`test_analysis_template_required_root_causes`,
`test_analysis_template_disclaimer_names_followup`,
`test_analysis_template_2device_impact_section` (asserts the `##
降级 2 设备 影响段` heading is present + both dropped-axis impact
sentences appear).

### Step 7 — `E2E_RUNBOOK.md` Multi-device append

Files:

- `tests/prod_test/E2E_RUNBOOK.md` — append-only. Snapshot the file's
  pre-append SHA256 (in the test) and confirm the first N bytes (where
  N = pre-append length) remain identical after the edit.
- `tests/prod_test/multidevice/tests/test_runbook_append_only.py` —
  reads the current file's `## Multi-device deployment (§C5 smoke)`
  section boundary, asserts everything above that heading matches the
  base commit's version of the file (fetched via
  `git show origin/paper/v3-integration:multi-agent/tests/prod_test/E2E_RUNBOOK.md`);
  asserts the section contains: scope banner, cross-reference to
  `multidevice/README.md`, ASCII topology diagram, port/tunnel table,
  OAuth device flow steps (documentation only), handoff pointer.
- **Topology diagram content assertions** (spec §4.2 item 3 requires
  4 boxes + inter-device tunnels labeled with port ranges): the test
  asserts the ASCII diagram block (delimited by the fenced ``` code
  block within the Multi-device section) contains all four device
  labels (`laptop`, `headless`, `windows`, `cloud`) and each
  inter-device tunnel line carries at least one port token matching
  `-E ':1809[1-4]|:18080'` (so a diagram lacking port annotations
  fails). Assertion enforces cross-machine tunnel port annotations.

Tests light up here:
`test_runbook_host_mode_bytes_unchanged`,
`test_runbook_multidevice_section_required_content`,
`test_runbook_topology_diagram_has_all_devices_and_ports`.

### Step 8 — `teardown.sh` dual gate

Files:

- `tests/prod_test/multidevice/teardown.sh` — bash; `--dry-run` (default)
  prints planned commands for the 4 items (spec §7.1); `--execute`
  behavior: if `ALLOW_TEARDOWN=1` set, executes; else falls back to
  `--dry-run` and warns on stderr.
- `tests/prod_test/multidevice/tests/test_teardown.py` — invokes
  `--dry-run` (asserts ≥4 lines matching `-E 'curl|doctl|pwsh|rm -f.*token'`);
  invokes `--execute` **without** `ALLOW_TEARDOWN` env (asserts stderr
  contains "ALLOW_TEARDOWN not set" AND asserts no real curl / doctl /
  pwsh subprocess was invoked — which is trivially true because the
  script's fall-back branch skips the exec block entirely);
  does NOT test the with-env case (per spec §7.2).

Tests light up here:
`test_teardown_dry_run_prints_four`,
`test_teardown_double_gate`.

### Step 9 — `.gitignore` amendment + git check-ignore assertions

Files:

- `multi-agent/.gitignore` — add:
  ```
  !/tests/prod_test/multidevice/
  !/tests/prod_test/multidevice/**
  /tests/prod_test/multidevice/tokens/
  /tests/prod_test/multidevice/**/*.token
  /tests/prod_test/multidevice/**/*.pem
  /tests/prod_test/multidevice/**/tokens.yaml
  ```
  Ordering: the two whitelists come first, the four re-ignore patterns
  after — so the re-ignore wins for token-bearing paths (later patterns
  in `.gitignore` override earlier ones).
- `tests/prod_test/multidevice/tests/test_gitignore.py` — for each of
  the 6 invariant paths (2 tracked + 4 ignored), calls
  `subprocess.run(['git', 'check-ignore', ...])` and asserts return
  code 0 (ignored) or 1 (tracked) per the table:
  | Path | Expected `git check-ignore` |
  |---|---|
  | `tests/prod_test/multidevice/laptop.yaml.template` | rc=1 (tracked) |
  | `tests/prod_test/multidevice/wrappers/laptop_up.sh` | rc=1 (tracked) |
  | `tests/prod_test/multidevice/tokens/x.yaml` (fake path) | rc=0 (ignored) |
  | `tests/prod_test/multidevice/x.token` (fake path) | rc=0 (ignored) |
  | `tests/prod_test/multidevice/subdir/x.pem` (fake path) | rc=0 (ignored) |
  | `tests/prod_test/multidevice/tokens.yaml` (fake path) | rc=0 (ignored) |
- Also runs the negative secret-scan test on a scratch file that DOES
  contain `sk-abc123` to prove the scan command in
  `test_multidevice_dir_no_secrets` catches injections.

Tests light up here:
`test_gitignore_tokens`,
`test_multidevice_dir_no_secrets`,
`test_multidevice_dir_no_secrets_negative_case`.

### Step 10 — Secret-scan lint + bind-endpoint parser

Files:

- `tests/prod_test/multidevice/lint.sh` — driver script for CI. Runs:
  ```bash
  grep -REn 'sk-|ghp_|AKIA|xoxb|Bearer[[:space:]]+[A-Za-z0-9]|refresh_token' \
      multi-agent/tests/prod_test/multidevice/
  ```
  (uses ERE — pipe alternation with `|`, not `\|`.) Exits non-zero on
  any match unless it's the literal placeholder string.
- `tests/prod_test/multidevice/tests/test_bind_endpoints.py` — uses
  `urllib.parse.urlparse` (with the value prefixed `http://` if bare)
  to parse each `bind:` / `listen:` / `endpoint:` value from the four
  wrappers + the four sample yamls; positive cases (`127.0.0.1:18091`,
  `localhost:18092`, `[::1]:18093`) accepted; negative cases
  (`0.0.0.0:18091`, `:18091`, `[::]:18091`, `10.0.0.5:18091`)
  MUST be rejected. Test also runs on the wrappers with the
  disallowed values injected via a temp copy — proves the parser is
  actually rejecting.

Tests light up here:
`test_lint_runs_clean_on_current_tree`,
`test_bind_endpoints_parser_positive_and_negative`.

### Step 11 — Cloud upload secretscrub wiring

Files:

- `tests/prod_test/multidevice/secretscrub_python.py` — port of
  `internal/secretscrub/scrub.go` regex set into Python (imported by
  cloud upload code). Contains **only** the regex constants + a
  `sanitize(str) -> str` function; no expvar / no dependency on the
  Go source. Comment references the Go file's line numbers so drift
  is detectable.
- `tests/prod_test/multidevice/wrappers/cloud_upload.py` — helper
  called by `cloud_up.sh`. Reads a yaml, runs sanitize, exits non-zero
  if any redaction occurred (i.e. cloud upload is refused when it
  would carry a secret).
- `tests/prod_test/multidevice/tests/test_cloud_upload_secretscrub_wired.py`
  — injects fake `sk-abc123` into a scratch yaml; asserts
  `cloud_upload.py` exits non-zero AND stderr contains "secret detected".
  Also asserts a clean yaml (no secrets) passes.

Tests light up here:
`test_cloud_upload_secretscrub_wired`,
`test_cloud_upload_secretscrub_clean_passes`.

### Step 12 — Dry-run smoke integration

Files:

- Invoke `dry_run_all.sh` as the final integration step; writes:
  - `tests/eval/results/prod/dry_run_smoke/prod_vs_stub_sample.csv`
    (via `build_prod_vs_stub.py --sample-mode`)
  - `tests/eval/results/prod/dry_run_smoke/topology_used.json` (hostname
    redacted)
  - `tests/eval/results/prod/dry_run_smoke/.gitkeep`
- Existing `analysis_template.md` also lives in this directory (from
  step 6).
- `tests/prod_test/multidevice/tests/test_integration.py` — asserts
  all four artifacts exist after `dry_run_all.sh`; asserts no
  `sk-` / `Bearer <hex>` in any of them; asserts CSV column header
  matches; asserts topology_used.json hosts[*].hostname_sha8 length = 8.

Tests light up here:
`test_integration_dry_run_smoke_all_artifacts`,
`test_integration_no_secrets_in_smoke_outputs`.

---

## Test matrix — 1:1 mapping to spec security items (a)–(k)

**Secret-scan command (used verbatim in `test_multidevice_dir_no_secrets`)** —
placed outside the table to avoid pipe/markdown escaping ambiguity; the
implementation MUST use this exact command with the `|` alternation
(ERE), not `\|`:

```bash
grep -REn 'sk-|ghp_|AKIA|xoxb|Bearer[[:space:]]+[A-Za-z0-9]|refresh_token' multi-agent/tests/prod_test/multidevice/
```

| Test name | What it verifies | Spec security item |
|---|---|---|
| `test_multidevice_dir_no_secrets` | Runs the ERE grep command shown above; expects no hits in the current tree (placeholder `<OAUTH_TOKEN_HERE_DO_NOT_COMMIT>` is OK). | (a) |
| `test_multidevice_dir_no_secrets_negative_case` | Injects fake `sk-abc123` into a scratch file inside `multidevice/`; expects the same command to catch it (rc=0 on grep, test asserts non-empty match). | (a) |
| `test_gitignore_tokens` | For each of 6 invariant paths (2 tracked + 4 ignored), runs `git check-ignore` and asserts the expected rc. | (a) |
| `test_bind_endpoints_parser_positive_and_negative` | Parses `bind:` / `listen:` / `endpoint:` values from wrappers + samples with `urllib.parse.urlparse`; positive: `127.0.0.1`, `::1`, `localhost` accepted; negative: `0.0.0.0`, bare `:PORT`, `[::]:PORT`, external IP rejected with a specific reason string. | (b) |
| `test_windows_execpolicy_scope_process` | Greps every `.ps1` under `multidevice/`; every `Set-ExecutionPolicy` line's `-Scope` value literally equals `Process`; no `LocalMachine` / `CurrentUser`. | (c) |
| `test_cloud_upload_secretscrub_wired` | Injects fake `sk-abc123` into a scratch yaml; runs `cloud_upload.py`; asserts exit non-zero + stderr says "secret detected". | (d) |
| `test_cloud_upload_secretscrub_clean_passes` | Runs `cloud_upload.py` on a clean yaml (no secrets); asserts exit 0. | (d) |
| `test_workspace_id_lifecycle_documented` | Reads `multidevice/README.md`; asserts presence of `## workspace_id lifecycle` heading + all 4 items of the shutdown checklist. | (e) |
| `test_stub_source_pinned` | `build_prod_vs_stub.py --sample-mode` output CSV meta header has `# stub_source_sha256=<hex>` that equals `sha256sum` of the stub file. | (f) |
| `test_stub_source_allowlist_reject` | Passes a non-allowlisted `--stub-table` path; asserts exit 2 + stderr says "not in allow-list". | (f) |
| `test_topology_used_hostname_redacted` | Runs `dry_run_all.sh` with `HOSTNAME=alice-laptop` env; asserts `dry_run_smoke/topology_used.json` hosts[*].hostname_sha8 equals `sha256("alice-laptop")[:8]`. | (g) |
| `test_cloud_vendor_yaml_single_line` | Loads `cloud.yaml.template`; asserts `vendor:` key present with value in `{digitalocean, e2b, vps}`. | (h) |
| `test_analysis_template_scope_declaration_lines` | Reads `analysis_template.md`; asserts each of the 4 literal scope lines from spec §6.1 present via `line in text`. | (i) |
| `test_analysis_template_metric_sections` | Asserts 4 metric `##` headings present. | (i) |
| `test_analysis_template_required_root_causes` | Asserts the 4 root-cause literals (`OAuth round-trip`, `真 tunnel`, `跨机 RTT`, `云 sandbox 冷启动`) each present. | (i) |
| `test_analysis_template_disclaimer_names_followup` | Asserts last line names `p3-prod-multidevice-run`. | (i) |
| `test_teardown_dry_run_prints_four` | Runs `teardown.sh --dry-run`; captures stdout; asserts ≥4 lines match `-E 'curl|doctl|pwsh|rm -f.*token'`. | (j) |
| `test_teardown_double_gate` | Runs `teardown.sh --execute` without `ALLOW_TEARDOWN` env; asserts stderr contains "ALLOW_TEARDOWN not set" AND stdout still prints the dry-run commands (fall-back path). Does NOT test with-env case. | (j) |
| `test_no_prod_deploy_without_env` | For each wrapper (`laptop_up.sh`, `headless_up.sh`, `cloud_up.sh`, `windows_up.ps1`): shell-parses the file; every occurrence of `--mode prod` (or `-Mode prod`) must be preceded (in the same conditional branch) by the `ALLOW_PROD_DEPLOY` env check. | (k) |
| `test_dry_run_all_never_sets_allow_prod_deploy` | Greps `dry_run_all.sh`; asserts no assignment `ALLOW_PROD_DEPLOY=`. | (k) |

Every security item (a)–(k) has ≥1 test above (many have 2 including a
negative-case test). Column-3 coverage:

- (a) → 3 tests (2 secret-scan + gitignore)
- (b) → 1 test (positive + negative in same test)
- (c) → 1 test
- (d) → 2 tests (redact-when-dirty + pass-when-clean)
- (e) → 1 test
- (f) → 2 tests (SHA meta + allowlist reject)
- (g) → 1 test
- (h) → 1 test
- (i) → 4 tests (scope lines / metric sections / root causes / disclaimer)
- (j) → 2 tests (dry-run prints / double gate fall-back)
- (k) → 2 tests (per-wrapper gate + dry_run_all never sets env)

Additional tests beyond the security matrix (schema / workload / RUNBOOK
append / smoke integration) round out the spec's acceptance list from
§8.

---

## Degraded topology (2-device / 3-device) test coverage

- `test_schema_positive_all_topology_enums` (Step 1) validates each of
  the four topology enums has a matching sample file.
- `test_2_device_min_topology_notes` — parses
  `topologies/2-device-min.yaml.sample` and asserts its `notes:` block
  mentions both dropped coverage axes (no cross-OS coverage + no
  cross-internet tunnel). (The corresponding `analysis_template.md`
  section is checked separately by
  `test_analysis_template_2device_impact_section` in Step 6.)
- `test_3_device_nowin_notes_present` — same shape for
  `3-device-nowin.yaml.sample` (asserts `notes` mentions Windows
  drop).
- `test_3_device_nocloud_notes_present` — same for
  `3-device-nocloud.yaml.sample` (asserts `notes` mentions cloud drop
  + tailscale 伪云 label).

These live in `tests/test_topology_samples.py` (Step 1 module).

---

## Not in this plan (belongs to `p3-prod-multidevice-run`)

- Real device provisioning.
- Real OAuth device flow execution.
- Real cloud droplet creation / destruction (`doctl` / `e2b` / `ssh`).
- Real workspace_id allocation on `agent.cs.ac.cn`.
- Producing `tests/eval/results/prod/run-*`, `prod_vs_stub.csv` (no
  `_sample`), `analysis.md` (no `_template`).
- Actual `divergent-unexplained = 0` gate result.
- Extending workloads beyond `cross-device-code-mod` +
  `windows-only-artifact` (blocked by config schema).
- Any change under `multi-agent/deploy/`, `multi-agent/internal/`, or
  `multi-agent/tools/eval/` — those file domains are forbidden here.
