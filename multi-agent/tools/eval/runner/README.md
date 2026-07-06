# `eval-runner`

Phase 1 evaluation harness skeleton. Runs one workload end-to-end,
brings up `agentserver-stub`, invokes the workload's oracle, and
emits one D1 `runs`-row equivalent to a CSV. See
`docs/specs/wt1-eval-runner-skeleton.spec.md` for the full picture.

This README documents the WT-2-flag-integration CLI surface: the
`--ablation` flag, the `--list-ablations` verb, and the
`baseline_or_ablation` CSV column.

## `--ablation` and `--list-ablations`

Flip one or more ablation flags for a run:

```
eval-runner run --workload cross-device-code-mod \
                --stub-listen 127.0.0.1:18080 \
                --out /tmp/run.csv \
                --ablation NoObserver,NoAcceptanceGate
```

`--ablation` accepts a single flag, a comma-joined list, and
multiple `--ablation` invocations. Duplicates are deduped; the
order of the sorted `+`-join in `baseline_or_ablation` is
byte-identical regardless of the input order.

Unknown flag names (typos) exit 2 before the run starts; the
Python side of `NoAcceptanceGate` is bridged via
`LOOM_ABLATION_NOACCEPTANCEGATE=1`, which the runner sets in the
environment as part of applying the flag.

To print the flag names + one-line descriptions the runner
currently supports without running a workload:

```
eval-runner --list-ablations
```

Two tab-separated columns per line: the flag name, then a
one-line description carrying no path / env-var / config
content. To inspect a flag's runtime state, use the owner
package's `IsXxx()` accessor from Go tests or a debugger — the
CLI intentionally does not expose the current bool.

## The 8 ablation flags — mapping to metrics

Each row lists the flag, its owning package, the 12 号
§A/§B/§D section it belongs to, and the metric(s) an operator
should expect to move when the flag is applied. The
`TestReadmeMetricMapping` CI test cross-checks the section
column against the paper doc and asserts the "predicted metric
impact" cell names at least one canonical metric from that
same paper section.

| Flag                    | Owning package                         | Section | Predicted metric impact                                                                                                             |
| ---                     | ---                                    | ---     | ---                                                                                                                                 |
| NoCapabilityDiscovery   | `internal/capability`                  | §A1     | HumanContextSelectionCount rises; RoutingAccuracy falls when the skipped snapshot would have surfaced a fresh capability.           |
| NoTypedContracts        | `internal/contract`                    | §A2     | ContractCompleteness deficit rises; Rework rises with failure categories contract_field_missing / type_error.                       |
| NoContractFormalization | `internal/contract`                    | §A2     | ContractCompleteness goes to N/A; §A2 natural-language baseline; Rework rises and TaskSuccessRate falls versus NoTypedContracts.    |
| NoDryRun                | `internal/contract/validator`          | §A3     | Zeroes PreExecutionFaultCatchRate, MissingArtifactDetectionRate, PolicyViolationPreventionRate; DriverPlanningOverhead falls a bit. |
| NoUserPromotionPath     | `internal/driver`                      | §B1     | Zeroes PromoteCandidateSurfaceRate; LifecycleClosureRate falls when a workload depends on promotion.                                |
| NoAcceptanceGate        | `internal/ablation` (Python-companion) | §B3     | Bypasses the acceptance golden runner; post-hoc contract violations rise in D4 category acceptance_bypassed.                        |
| NoRegistryLookup        | `internal/driver`                      | §B4     | Zeroes RegistryLookupHitRate; ManualSetupStepCount rises when the missing hint would have short-circuited a lookup.                 |
| NoObserver              | `internal/evalrun`                     | §D1     | Suppresses runs-row writes via DisableTelemetry; the runs table stays empty for this run.                                           |

Row rules (asserted by `TestReadmeMetricMapping`):

- Exactly 8 rows; every flag name is in `ablation.KnownFlags()`.
- The `§Xn` section column matches the `[Xn]` prefix in
  `flags.go`'s `flagDescriptions` (single source of truth).
- The impact cell mentions at least one metric name that also
  appears in the 12 号 `§Xn` row's `metrics` column.

## `baseline_or_ablation` in the CSV

Every run emits one row with a stable `baseline_or_ablation`
value:

- `full_loom` — no ablation, no `--baseline-name` override.
- `my_run` — operator supplied `--baseline-name my_run`.
- `NoObserver` — single-flag ablation.
- `NoAcceptanceGate+NoObserver` — two-flag sorted `+`-join.

Character class matches
`^[a-z][a-z0-9_-]{2,63}$` (baseline form, shared with
`tests/eval/baselines/harness/writer.go`) OR
`^([A-Z][A-Za-z0-9]{2,31})(\+[A-Z][A-Za-z0-9]{2,31})*$`
(ablation form). Length capped at 200 chars (8-flag sorted join
is 136 chars, leaving slack for two more ~30-char flags before
a re-derivation is needed).

## D2 consumer view

The label is designed for `GROUP BY` in D2 metric extraction.
SQL against the observerstore:

```sql
SELECT baseline_or_ablation,
       COUNT(*) AS runs,
       AVG(CASE WHEN success_oracle_result = 'pass' THEN 1.0 ELSE 0.0 END) AS task_success_rate,
       AVG(CASE WHEN failure_category = 'contract_field_missing' THEN 1 ELSE 0 END) AS contract_field_missing_rate
FROM runs
WHERE experiment_id = 'E1'
GROUP BY baseline_or_ablation
ORDER BY baseline_or_ablation;
```

Pandas against the runner CSV:

```python
import pandas as pd
df = pd.read_csv("run.csv")
grouped = df.groupby("baseline_or_ablation").agg(
    runs=("run_id", "count"),
    task_success_rate=("probe_task_success_rate", lambda s: s.astype(bool).mean()),
    duration_ms_p50=("duration_ms", "median"),
    duration_ms_p95=("duration_ms", lambda s: s.quantile(0.95)),
).reset_index().sort_values("baseline_or_ablation")
print(grouped)
```

The sort-stable `+`-join keeps both queries safe against
invocation-order variation — passing `--ablation NoObserver,NoAcceptanceGate`
and `--ablation NoAcceptanceGate,NoObserver` both produce
`NoAcceptanceGate+NoObserver` in the CSV.

## Adding a new flag

1. Extend `internal/ablation/registry.go`'s `canonicalFlags`
   with the new `FlagName` constant.
2. Register a `*bool` target from the owning package's `init()`
   via `ablation.Default.Register(new_flag, &pkg.DisableFoo)`.
3. Add a one-line entry to `flagDescriptions` in `flags.go` and
   a matching row in this README's flag table.
4. If the new cap would push the 8→9-flag sorted join above 200
   chars, bump `MaxBaselineOrAblationLen`; the
   `TestBaselineOrAblationCapCoversAll8Flags` test catches this.
5. If the owner package ships an atomic mirror, add its `Sync…`
   accessor to `flags.go`'s `syncHooks` slice.
6. If the flag has a Python companion, add its env-var name to
   `ablationEnvExports`.

`TestApplyAblationFlags_AllKnownFlagsBind` and
`TestListAblations_CountMatchesRegistry` catch a canonical
constant added without the corresponding steps.
