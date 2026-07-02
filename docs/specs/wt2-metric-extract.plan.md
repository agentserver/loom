# WT-2-metric-extract — Plan

> Companion to `docs/specs/wt2-metric-extract.spec.md`. Read the spec
> first — this plan operationalizes each spec section into a concrete
> build order and a pytest matrix that maps 1:1 onto the spec's
> §2 metric catalog and §7 security items (a)–(h). No scope changes
> here.

## 1. Build order

Nine short steps; each ends at a `pytest -q` gate that adds ≤ ~10
tests. TDD throughout: each step writes failing tests first, then the
minimum production code to make them green.

| Step | Deliverable | Gate |
|---|---|---|
| 1 | `pyproject.toml` + `eval_metrics/{__init__,__main__,cli}.py` (argparse skeleton, exit codes 0/2/3, `--help`) | `python -m eval_metrics --help` exits 0; `pytest tests/test_cli_help.py` passes |
| 2 | `eval_metrics/paths.py` — `--observer-db` (§7 (b)) and `--out` (§7 (d)) path validators; magic-byte probe; `/proc/self/fd/<fd>` Linux TOCTOU handoff | `pytest tests/test_paths.py` (≤ 12 tests) |
| 3 | `eval_metrics/db.py` — read-only SQLite open helper wrapping step 2's fd; `PRAGMA query_only=ON` belt; missing-table vs empty-table detection helpers | `pytest tests/test_db_readonly.py` (≤ 6 tests) |
| 4 | `eval_metrics/filter.py` — `--runs-filter` denylist + parametric extraction + sqlglot AST walk + tautology guard (both `1=1` and `col1 = col2` cases) | `pytest tests/test_filter.py` (≤ 15 tests) |
| 5 | `eval_metrics/metrics/__init__.py` + `metrics/lifecycle.py` — the 9 §2.1 metrics + fixture 1 golden | `pytest tests/test_metrics_lifecycle.py` (≤ 12 tests including fixture-1 golden) |
| 6 | `metrics/contracted.py` + `metrics/semantic.py` — the 7 §2.2 metrics (all null today) + 3 §2.4 metrics + fixture 2 golden | `pytest tests/test_metrics_contracted.py tests/test_metrics_semantic.py` (≤ 10 tests) |
| 7 | `metrics/user_promoted.py` + `metrics/overhead.py` — 13 §2.3 metrics (all null) + 9 §2.5 metrics (only #37 populated) + fixture 3 golden | `pytest tests/test_metrics_user_promoted.py tests/test_metrics_overhead.py tests/test_metrics_golden_fixture_3.py` (≤ 15 tests) |
| 8 | `eval_metrics/csv_out.py` + `eval_metrics/json_out.py` — serializers; §7 (d) formula-injection escape; `_notes` companion column / `notes` JSON key with the two-value closed set (`upstream data missing` / `denominator zero`) | `pytest tests/test_csv_out.py tests/test_json_out.py tests/test_metrics_null_contract.py` (≤ 20 tests) |
| 9 | End-to-end integration: empty-DB fixture, `--metric-set` subset selection including cross-listed members, missing-companion-table branch, CI-conditional perf gate | `pytest tests/test_empty_db.py tests/test_integration.py` + full `pytest -q` clean |

## 2. Pytest matrix (mapped to spec §7 (a)–(h) and §2 metrics)

The following table lists every test file the plan creates. Each test
maps to either a specific spec-§2 metric row (functional coverage) or a
spec-§7 security item (security coverage). Every §7 item has ≥ 1
passing test per §5.5 acceptance criterion.

| Test file | Test name | Verifies | Spec anchor |
|---|---|---|---|
| `test_cli_help.py` | `test_help_smoke` | `--help` prints usage; exit 0 | §3 CLI |
| `test_cli_help.py` | `test_extract_subcommand_registered` | `extract` is a subcommand of the top-level CLI | §3 CLI |
| `test_paths.py` | `test_observer_db_missing_file_exit2` | non-existent path → exit 2 with `FileNotFoundError` message | §7 (b) |
| `test_paths.py` | `test_observer_db_etc_reject` | `--observer-db /etc/passwd` (which is not a SQLite file but exists) → exit 2 via `/etc/` reject before magic probe | §7 (b) |
| `test_paths.py` | `test_observer_db_proc_reject` | `/proc/self/mem` → exit 2 | §7 (b) |
| `test_paths.py` | `test_observer_db_sys_reject` | `/sys/kernel/notes` → exit 2 | §7 (b) |
| `test_paths.py` | `test_observer_db_dev_reject` | `/dev/null` → exit 2 (even though `/dev/null` exists) | §7 (b) |
| `test_paths.py` | `test_observer_db_magic_bytes_reject` | non-SQLite file (e.g. empty bytes, PNG, plain text) → exit 2 before `sqlite3.connect` | §7 (b) |
| `test_paths.py` | `test_observer_db_symlink_swap_after_resolve` | Linux-only: install symlink, resolve, swap symlink target after resolve, verify `os.open(O_NOFOLLOW)` on step 2 rejects | §7 (b) TOCTOU |
| `test_paths.py` | `test_observer_db_proc_self_fd_open` | Linux-only: verify the fd handoff to sqlite3 via `/proc/self/fd/<fd>` — assert we opened the pre-verified inode, not a re-resolved path | §7 (b) TOCTOU |
| `test_paths.py` | `test_out_parent_must_exist` | `--out /nonexistent/dir/file.csv` → exit 2 (do NOT mkdir -p) | §7 (d) |
| `test_paths.py` | `test_out_refuse_overwrite` | existing file at `--out` path → exit 2 `ErrOutFileExists`; no `--force` flag | §7 (d) |
| `test_paths.py` | `test_out_refuse_symlink` | target is a symlink → exit 2 `ErrOutIsSymlink` | §7 (d) |
| `test_paths.py` | `test_out_atomic_create_o_excl` | race: create file between step 3 (symlink check) and step 4 (open) — assert `O_EXCL` open fails with `FileExistsError` | §7 (d) TOCTOU |
| `test_paths.py` | `test_out_dir_fd_bind` | Linux-only: rename resolved parent between resolve and open — assert the fd-based open opens the ORIGINAL parent's file, not the renamed target | §7 (d) TOCTOU |
| `test_db_readonly.py` | `test_readonly_open_mode` | connection is `file:...?mode=ro&immutable=1`; `PRAGMA query_only` returns 1 | §7 (a) |
| `test_db_readonly.py` | `test_update_raises_operational_error` | attempted `UPDATE runs SET ... ` raises `sqlite3.OperationalError` mentioning "readonly" | §7 (a) |
| `test_db_readonly.py` | `test_insert_raises_operational_error` | attempted `INSERT INTO runs ... ` raises `sqlite3.OperationalError` | §7 (a) |
| `test_db_readonly.py` | `test_delete_raises_operational_error` | attempted `DELETE FROM runs ... ` raises `sqlite3.OperationalError` | §7 (a) |
| `test_db_readonly.py` | `test_missing_companion_table` | when `route_reasons` is absent, the helper reports upstream-missing (not "no such table" exception) | §4.4 branch |
| `test_db_readonly.py` | `test_empty_companion_table` | when `route_reasons` is present but empty, denominator-zero semantics kick in (not upstream-missing) | §4.4 branch |
| `test_filter.py` | `test_denylist_semicolon_reject` | `run_id='x'; DROP TABLE runs` → exit 2 | §7 (c) |
| `test_filter.py` | `test_denylist_update_reject` | `1=1; UPDATE runs SET success_oracle_result='pass'` → exit 2 (the paper-safety flagship attack) | §7 (c) |
| `test_filter.py` | `test_denylist_select_reject` | `run_id='x' OR EXISTS (SELECT 1 FROM sqlite_master)` → exit 2 | §7 (c) |
| `test_filter.py` | `test_denylist_comment_dashdash_reject` | `run_id = 'x' -- ignore` → exit 2 | §7 (c) |
| `test_filter.py` | `test_denylist_comment_slashstar_reject` | `run_id = 'x' /* comment */` → exit 2 | §7 (c) |
| `test_filter.py` | `test_field_whitelist_secret_col_reject` | `secret_col = 'x'` → exit 2 `ErrRunsFilterFieldNotAllowed` | §7 (c) |
| `test_filter.py` | `test_field_whitelist_sqlite_master_reject` | `run_id IN (SELECT name FROM sqlite_master)` → exit 2 (belt + suspenders) | §7 (c) |
| `test_filter.py` | `test_tautology_1_eq_1_reject` | `run_id = 'x' OR 1 = 1` → exit 2 `ErrRunsFilterTautology` | §7 (c) |
| `test_filter.py` | `test_tautology_bare_reject` | `1=1` alone → exit 2 | §7 (c) |
| `test_filter.py` | `test_tautology_col_col_compare_reject` | `run_id = workload_id` → exit 2 `ErrRunsFilterColumnCompare` | §7 (c) |
| `test_filter.py` | `test_tautology_col_col_self_reject` | `experiment_id = experiment_id` → exit 2 `ErrRunsFilterColumnCompare` | §7 (c) |
| `test_filter.py` | `test_parametric_binding_used` | mock `cursor.execute`; assert user string literal appears in params tuple, NOT concatenated into SQL | §7 (c) |
| `test_filter.py` | `test_accept_equality_literal` | `run_id = 'run-abc'` accepted; cohort narrowed | §7 (c) |
| `test_filter.py` | `test_accept_in_literal_list` | `experiment_id IN ('E1','E3')` accepted | §7 (c) |
| `test_filter.py` | `test_accept_like_pattern` | `workload_id LIKE 'code-mod-%'` accepted | §7 (c) |
| `test_filter.py` | `test_accept_and_or_combination` | `baseline_or_ablation = 'FullLoom' AND experiment_id IN ('E1','E3')` accepted | §7 (c) |
| `test_metrics_lifecycle.py` | `test_fixture_1_task_success_rate` | fixture 1 → 0.6 | §2.1 #1, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_lifecycle_closure_rate` | fixture 1 → 0.6 (5-column AND) | §2.1 #2, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_time_to_completion` | fixture 1 → `{mean_seconds:29.2, p50_seconds:19.0, p95_seconds:76.5, count:10}` | §2.1 #3, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_human_context_selection_count` | fixture 1 → 8 | §2.1 #4, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_wrong_context_failure_rate` | fixture 1 → 0.3 (rows 3, 5, 9 — 5-tag D4 taxonomy) | §2.1 #5, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_artifact_correctness_rate` | fixture 1 → 1.0 (6/6) | §2.1 #6, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_manual_setup_step_count_null` | fixture 1 → `null`; `_notes` reason `upstream data missing` | §2.1 #7, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_config_touch_count_null` | fixture 1 → `null`; note upstream | §2.1 #8, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_state_continuity_rate_null` | fixture 1 → `null`; note upstream (§A6) | §2.1 #9, §4.1 |
| `test_metrics_lifecycle.py` | `test_fixture_1_hash_canonicalization` | assert the fixture DB stores 64-hex sha256 hashes, not short IDs (matches `evalrun/schema.go:42`) | §4.1 hash-format note |
| `test_metrics_contracted.py` | `test_fixture_2_all_contracted_null` | all 7 §2.2 metrics on fixture 2 → `null`; notes: #10 → cohort-attribution, #11..#13 → upstream (§A3/§A4), #14/#16 → upstream, #15 → upstream (§A6) | §2.2, §4.2 |
| `test_metrics_semantic.py` | `test_fixture_3_routing_accuracy` | fixture 3 → 0.75 | §2.4 #28, §4.3 |
| `test_metrics_semantic.py` | `test_fixture_3_capability_recall_null` | fixture 3 → `null`; note upstream (§F4) | §2.4 #29, §4.3 |
| `test_metrics_semantic.py` | `test_fixture_3_capability_precision_null` | fixture 3 → `null`; note upstream | §2.4 #30, §4.3 |
| `test_metrics_user_promoted.py` | `test_all_13_user_promoted_null` | fixture 3 → all 13 §2.3 metrics `null`; notes upstream | §2.3, §4.3 |
| `test_metrics_overhead.py` | `test_fixture_3_routing_latency_p50p95` | fixture 3 → `{p50_ns:1500000, p95_ns:3700000, count:4}` | §2.5 #37, §4.3 |
| `test_metrics_overhead.py` | `test_overhead_structured_shapes` | assert #35 has 5 sub-columns, #36 has 6 sub-columns, others have 3 | §2.5 shapes |
| `test_metrics_overhead.py` | `test_time_to_first_task_null` | fixture 3 → `null` | §2.5 #38 |
| `test_metrics_overhead.py` | `test_setup_failure_rate_null` | fixture 3 → `null` | §2.5 #39 |
| `test_metrics_golden_fixture_3.py` | `test_fixture_3_full_arithmetic` | fixture 3 → 8 populated + 33 null = 41 (per §4.3 total) | §4.3 |
| `test_csv_out.py` | `test_csv_formula_injection_escape_eq` | cell value `=SUM(A1:A9)` → prefixed with `'` in CSV output | §7 (d) |
| `test_csv_out.py` | `test_csv_formula_injection_escape_plus` | cell value `+cmd` → prefixed | §7 (d) |
| `test_csv_out.py` | `test_csv_formula_injection_escape_minus` | `-cmd` → prefixed | §7 (d) |
| `test_csv_out.py` | `test_csv_formula_injection_escape_at` | `@import` → prefixed | §7 (d) |
| `test_csv_out.py` | `test_csv_formula_injection_escape_tab` | `\t...` → prefixed | §7 (d) |
| `test_csv_out.py` | `test_csv_formula_injection_escape_cr` | `\r...` → prefixed | §7 (d) |
| `test_csv_out.py` | `test_csv_formula_injection_escape_newline` | `\n...` → prefixed | §7 (d) |
| `test_csv_out.py` | `test_csv_no_escape_when_safe` | cell value `run-abc` → unchanged | §7 (d) |
| `test_csv_out.py` | `test_csv_header_order_stable` | header order matches §2 catalog ordering across two runs | §3.3 CSV order |
| `test_csv_out.py` | `test_csv_notes_column_semicolon_separated` | `_notes` cell format is `<metric>: <reason>; <metric>: <reason>` | §3.3 |
| `test_json_out.py` | `test_json_object_keys` | keys are `{metric_set, row_count, metrics, notes}` in that order | §3.3 JSON |
| `test_json_out.py` | `test_json_notes_always_present` | notes key is present even when empty (JSON `{}`) | §3.3 |
| `test_metrics_null_contract.py` | `test_denominator_zero_outputs_null_not_nan` | ratio with 0 denominator → CSV empty cell / JSON `null`; assert string output contains neither `NaN` nor `Infinity` nor bare `0` for the metric | §7 (f) |
| `test_metrics_null_contract.py` | `test_denominator_zero_emits_note` | every denominator=0 metric gets `_notes` entry `"denominator zero"` | §7 (f), §7 (g) |
| `test_metrics_null_contract.py` | `test_upstream_missing_emits_note` | every upstream-missing metric gets `_notes` entry `"upstream data missing"` and stderr warning | §7 (g) |
| `test_metrics_null_contract.py` | `test_note_reason_closed_set` | every `null` cell has exactly one note; the reason string is one of the two closed-set values | §3.3, §7 (g) |
| `test_metrics_null_contract.py` | `test_count_metrics_zero_vs_null` | on empty cohort: `HumanContextSelectionCount = 0` (landed upstream), `ManualSetupStepCount = null` (unlanded); the two must be visibly different | §3.3 empty-DB, §7 (f) |
| `test_cli.py` | `test_metric_set_full_default` | `--metric-set full` = catalog default; emits all 41 metrics | §3.2 |
| `test_cli.py` | `test_metric_set_typo_reject` | `--metric-set liflecycle` → exit 2; allowed list printed | §7 (e) |
| `test_cli.py` | `test_metric_set_bare_missing_reject` | `--metric-set` without value → argparse exit 2 | §7 (e) |
| `test_cli.py` | `test_metric_set_case_sensitivity` | `--metric-set Lifecycle` (capitalized) → exit 2 (enum is case-sensitive, silent-typo protection) | §7 (e) |
| `test_cli.py` | `test_metric_set_lifecycle_cross_listed` | `--metric-set lifecycle` emits §2.1 + #22 + #23 (per §3.2 subset table) | §3.2 cross-list |
| `test_cli.py` | `test_metric_set_semantic_cross_listed` | `--metric-set semantic` emits §2.4 + #4 + #5 (per §3.2) | §3.2 cross-list |
| `test_cli.py` | `test_metric_set_overhead_cross_listed` | `--metric-set overhead` emits §2.5 + #7 + #8 | §3.2 cross-list |
| `test_cli.py` | `test_metric_set_user_promoted_cross_listed` | `--metric-set user-promoted` emits §2.3 + #1 + #3 + #40 + #41 | §3.2 cross-list |
| `test_cli.py` | `test_format_missing_reject` | omitting `--format` → argparse exit 2 (no default; silent-CSV hazard) | §3 CLI |
| `test_cli.py` | `test_format_typo_reject` | `--format js` → exit 2 | §3 CLI |
| `test_cli.py` | `test_out_stdout_when_omitted` | `--out` omitted → output goes to stdout (verified via captured `capsys`) | §3 CLI |
| `test_empty_db.py` | `test_empty_db_csv_header` | empty DB → CSV header row matches `metric_set, row_count, <41 metric names flattened>, _notes`; exactly 1 data row with `row_count=0` | §5.1 |
| `test_empty_db.py` | `test_empty_db_count_landed_zero` | `HumanContextSelectionCount = 0` on empty DB | §3.3 empty-DB |
| `test_empty_db.py` | `test_empty_db_count_unlanded_null` | `ManualSetupStepCount` cell is empty on empty DB | §3.3 empty-DB |
| `test_empty_db.py` | `test_empty_db_all_null_ratios_have_notes` | every null ratio cell has a `_notes` entry (with reason from the two-value set) | §7 (f), §7 (g) |
| `test_empty_db.py` | `test_empty_db_json_shape` | JSON output object keys, count-metric values, notes map correct on empty DB | §3.3 |
| `test_integration.py` | `test_end_to_end_fixture_1_csv` | pipe fixture 1 → CSV → parse with pandas → assert all populated metric values match §4.1 | §4.1 |
| `test_integration.py` | `test_end_to_end_fixture_2_json` | JSON output on fixture 2 has all 7 §2.2 metrics as `null` in `metrics` object; all 7 in `notes` map | §4.2 |
| `test_integration.py` | `test_end_to_end_fixture_3_csv` | fixture 3 → CSV → 8 populated + 33 null = 41 assertions | §4.3 |
| `test_integration.py` | `test_end_to_end_runs_filter_narrows_cohort` | `--runs-filter "baseline_or_ablation = 'FullLoom'"` on fixture 1 → same 10 rows (all FullLoom) | §3.1 |
| `test_integration.py` | `test_end_to_end_stderr_warnings` | run against fixture 3; assert stderr contains `warn: metric <name> returned null: upstream data missing (owner: 12号 §...)` lines | §7 (g) |
| `test_perf_gated.py` | `@pytest.mark.perf test_extract_10k_rows_completes_under_2s` | perf assertion skipped unless `CI=true` or `-m perf` explicitly enabled | §7 (h) |

**Test count sanity check**: 15 (paths) + 6 (db) + 15 (filter) + 10
(lifecycle golden) + 1 (contracted) + 3 (semantic) + 1 (user_promoted)
+ 4 (overhead) + 1 (fixture 3 sum) + 11 (csv_out) + 2 (json_out) + 5
(null_contract) + 11 (cli) + 5 (empty_db) + 5 (integration) + 1 (perf)
= **96 tests**. All must pass to close step 9's gate.

## 3. Fixture builders

Three helper scripts under `tests/fixtures/`:

- `build_fixture_1.py` — writes `fixture_1.db` from the §4.1 table.
  Applies the full observer schema DDL from
  `multi-agent/internal/observerstore/schema.sql`; INSERTs the 10
  rows; canonicalizes each short artifact-hash literal (`a1`, `a2`,
  …) to `hashlib.sha256(short.encode()).hexdigest()` before
  serialising into the `artifact_hashes` JSON array.
- `build_fixture_2.py` — same schema, populates the 6 §4.2 runs +
  6 `task_contracts` rows.
- `build_fixture_3.py` — same schema, populates 5 runs + 4
  `route_reasons` rows; run timestamps chosen so each dispatched
  run's `[start_time, end_time]` window contains exactly one
  decision timestamp per §4.3.

`expected_1.json`, `expected_2.json`, `expected_3.json` — the
hand-computed §4.x values verbatim; tests parse these and use them as
oracle values. Regenerating a fixture is `python tests/fixtures/build_fixture_N.py`
followed by a `pytest -k fixture_N` gate.

## 4. Dependency plan

`pyproject.toml`:

```toml
[project]
name = "eval-metrics"
version = "0.1.0"
requires-python = ">=3.10"
dependencies = [
  "sqlglot>=20,<27",    # AST parse for --runs-filter (§7 c); pure Python, no C ext
]

[project.optional-dependencies]
dev = [
  "pytest>=8",
  "pytest-cov>=5",
]

[project.scripts]
eval-metrics = "eval_metrics.cli:main"
```

Standard-library-only otherwise: `sqlite3`, `csv`, `json`, `os`,
`pathlib`, `hashlib`, `argparse`, `sys`. No pandas / numpy in
production; tests use `statistics` module (stdlib) for percentile
comparisons and optionally `pandas` in dev-only tests
(marked `@pytest.mark.pandas`, skipped if not installed).

## 5. What could go wrong (mitigations)

| Risk | Mitigation |
|---|---|
| sqlglot bumps a major version and its AST node names change | Pin `<27` and add a CI matrix job on the next major (see `test_filter.py` — it uses `sqlglot.parse_one` + `expressions.Column` / `.Comparison`; both names are stable across recent majors) |
| `/proc/self/fd/<fd>` on non-Linux (macOS) — plan step 2 branches | `sys.platform` check in `paths.py`; documented in spec §7 (b) |
| Fixture hash canonicalization drift (short → sha256) makes fixture regeneration non-reproducible | fixture-builder scripts commit their expected sha256 outputs (a `hashes.expected` sidecar); `pytest` re-derives and asserts equality — a builder bug fails fast |
| Codex reviewer at Phase 3 flags a residual metric-scope drift | Spec §2 metric catalog is 41 rows; the extractor's `metrics/__init__.py` MUST import each row's implementation module and construct a 41-element `ALL_METRICS` list; a `test_catalog_count == 41` assertion catches drift |
| CI perf assertion flakes | `@pytest.mark.perf` marker skips by default; see §7 (h) |
| Filter grammar false positives (legitimate query rejected) | Round-2 spec review flagged that `run_id = workload_id` should be rejected; the plan's `test_tautology_col_col_compare_reject` locks this in |

## 6. Definition of done

- All 96 tests in §2 pass (`pytest -q` clean under CI env).
- `python -m eval_metrics extract --observer-db <empty.db> --format csv | head -2` produces header row + 1 data row per §5.1.
- `git diff --name-only origin/paper/v3-integration...HEAD` shows only files under `multi-agent/tools/eval/metrics/` and the two spec/plan docs.
- No `.go` files touched.
- Commit trailers include `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- No push to origin; the branch stays local for review.

## 7. Change record

- 2026-07-03 (initial): plan written against
  `docs/specs/wt2-metric-extract.spec.md` at 14-round CLEAN state
  (catalog=41, security items (a)–(h), fixtures 1/2/3).
