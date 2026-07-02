# WT-2-driver-promotion-chain — Sub-B4 plan

Implementation plan for `docs/specs/wt2-driver-promotion-chain-B4.spec.md`.
TDD throughout.

## Step 1 — DDL + writer + tests (spec §5)

Tests first:

- `TestSchema_RegistryLookupSamplesExists` — column set: row_id, ts,
  run_id, workspace_id, query_hash_prefix, hit_count, registry_hits,
  userspace_hits, top_score.
- `TestRegistryLookupSamplesWriter_RoundTrip` — write, select, verify
  every field.
- `TestRegistryLookupSamplesWriter_ParameterizedSQL_NoInjection`
  — insert with meta chars in query_hash_prefix (defensive; the
  caller shouldn't put non-hex there).

Then implement:

- Append DDL to `internal/observerstore/schema.sql`.
- Create `internal/observerstore/registry_lookup_samples_writer.go`:
  `NewRegistryLookupSamplesWriter(db *sql.DB) RegistryLookupSamplesWriter`;
  `WriteRegistryLookupSample(ctx, RegistryLookupSampleRow) error`.

## Step 2 — CurrentRunID hook + tests (spec §5)

Tests first (in `internal/driver/current_run_id_test.go`):

- `TestSetCurrentRunID_RoundTrip`.
- `TestSetCurrentRunID_ConcurrentSafe` (run under -race).
- `TestCurrentRunID_ZeroValueEmpty`.

Then `internal/driver/current_run_id.go` with atomic.Pointer[string].

## Step 3 — Ablation + metrics helpers (spec §6, §7 (c))

Tests first (in `internal/driver/registrylookupmetrics_test.go`):

- `TestLookupHitRate_ZeroWhenNoQueries`.
- `TestLookupHitRate_IncrementsOnHit` — 3 queries, 2 hit → 0.667.

Then:

- `internal/driver/registrylookupmetrics.go`: atomic.Int64 counters +
  `LookupHitRate` + `bumpQuery`/`bumpHit` + `ResetLookupMetricsForTest`.
- `internal/driver/registry_lookup_ablation.go`: register
  `NoRegistryLookup` target; `IsNoRegistryLookup()`.

(The init-error surfacing test lives in Step 4 alongside `Lookup`.)

## Step 4 — Core `Lookup` + tests (spec §2, §3, §4, §7 all items)

Tests first — one per Security row in §8; use the same table-driven
pattern from earlier sub-tasks. Additional Step-4 tests explicitly
covering §5 sample-writer invariants (which live at the Lookup
boundary, not the writer boundary tested in Step 1):

- `TestLookup_WritesSampleRowPerCall` — 5 non-ablated calls → 5 rows
  in the injected sample-writer capture.
- `TestLookup_AblatedCallDoesNotWriteSample` — with NoRegistryLookup
  on, 0 sample-writer invocations.
- `TestLookup_SampleRowStoresHashPrefixNotQueryText` — pass a distinct
  raw query; assert the sample row's query_hash_prefix is exactly 8
  hex chars AND the sample-writer NEVER saw the raw string.
- `TestLookup_NilSampleWriteDegradesAndWarnsOnce` — with
  `LookupDeps.SampleWrite = nil`, two Lookup calls; assert Lookup
  returns hits normally AND exactly ONE `[warn] SampleWrite unwired`
  log line is emitted (sync.Once). Confirms §5 nil-safe degrade.
- `TestNoRegistryLookup_InitErrorSurfacedOnFirstLookup` — inject
  `noRegistryLookupInitErr = errors.New("simulated register clash")`,
  call `Lookup` twice; assert exactly ONE `[error] NoRegistryLookup
  ablation wiring inert` line in the captured log (spec §6 first-use
  surfacing).

Then implement `internal/driver/registry_lookup.go`:

- `LookupDeps` struct + `SetLookupDeps(d LookupDeps)`.
- `Hit`, `PackageHit`, `UserspaceSearcher` types.
- `Lookup(ctx, query)`: sanitize, ablation-short-circuit, registry
  search via `snapshotAll`, userspace search via
  `deps.UserspaceStore.SearchPackagesForIdentity`, merge + dedup + cap
  at 20, bump metrics, write sample row via `deps.SampleWrite`.

## Step 5 — draft_task_contract integration (spec §1 last para, §8)

Tests first (in `internal/driver/contract_tools_test.go`):

- `TestDraftTaskContract_CallsLookupAndAttachesHits` — set a mock
  UserspaceStore, seed one registry entry; assert the tool response
  carries `registry_hits`.
- `TestDraftTaskContract_UnderNoRegistryLookup_NoHits`.

Then modify `internal/driver/contract_tools.go:draftTaskContractTool.Call`
to call `Lookup(ctx, intent)` and attach hits to the returned draft
under a `registry_hits` field.

## Step 6 — driver-agent wiring (spec §9)

Modify `cmd/driver-agent/main.go`: after opening the observer store,
build a userspace-search adapter and construct an adapter that
converts `driver.RegistryLookupSample` (Lookup-side type) into
`observerstore.RegistryLookupSampleRow` (writer-side type), then
call
`driver.SetLookupDeps({UserspaceStore: ..., WorkspaceID: ...,
UserID: ..., SampleWrite: adapter, CurrentRunID: driver.CurrentRunID})`.

The adapter is a small function literal:

```go
sampleAdapter := func(ctx context.Context, s driver.RegistryLookupSample) error {
    return writer.WriteRegistryLookupSample(ctx, observerstore.RegistryLookupSampleRow{
        TS:              s.Queried,
        RunID:           s.RunID,
        WorkspaceID:     s.WorkspaceID,
        QueryHashPrefix: s.QueryHashPrefix,
        HitCount:        s.HitCount,
        RegistryHits:    s.RegistryHits,
        UserspaceHits:   s.UserspaceHits,
        TopScore:        s.TopScore,
    })
}
```

The two types are structurally identical but live in different
packages to keep `internal/driver` free of the `observerstore`
import cycle risk.

## Verification

```
cd multi-agent
go vet ./internal/driver/... ./internal/observerstore/...
go test ./internal/driver/... ./internal/observerstore/... -count=1 -race
```

## Commit shape

```
WT-2-driver-promotion-chain B4: registry_lookup + RegistryLookupHitRate metric + NoRegistryLookup ablation

- driver.Lookup(ctx, query) — merged view of in-process per-slave
  registry (from B6) + userspace FTS5 (from Phase 0). Sanitised
  query, 256-char cap, 20-hit cap. Ablatable via NoRegistryLookup.
- Per-invocation registry_lookup_samples row for D2's
  RegistryLookupHitRate aggregation; keyed by run_id (from the new
  driver.SetCurrentRunID/CurrentRunID pair) so Phase 3 parallel runs
  stay unambiguous. Row stores only query_hash_prefix — no raw text.
- Aggregate driver.LookupHitRate() is a debug convenience.
- draft_task_contract now calls Lookup before returning so the LLM
  sees candidate matches ("you may want to reuse this MCP") in one
  shot.
- New driver ablation target: NoRegistryLookup (name declared in
  Phase 1; target registered here).

Security: parameterised SQL; log echoes sanitised+hash-prefixed
query (not raw); sample table stores only hash prefix; sanitizer
strips FTS5 meta so lookup does not become a query-oracle;
NoRegistryLookup silence-broken by structured log line per §7 (c).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```
