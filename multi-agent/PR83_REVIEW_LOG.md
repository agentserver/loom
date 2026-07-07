# PR #83 Independent Review Log

**Reviewer**: fresh independent Claude subprocess (not the PR author, not the codex reviewer)
**Base**: `paper/v3-integration` @ `86bc2fd`
**Head**: `paper/v3/fix-81-writesnap` @ `7c3e99c`
**Diff scope**: 4 source files (`internal/observerweb/server.go`,
`internal/driver/observer_relay.go`, `internal/driver/capability_tools.go`,
`internal/driver/capability_snapshots_test.go` [new]) + 2 test files
(`internal/driver/capability_tools_test.go`,
`internal/driver/observer_relay_test.go`)

Instrument: adversarial 1-pass round + 1 dedicated Explore-subagent audit of
11 P0/P1 threat vectors + fresh unit-test rerun with `-race`.

---

## Round 1 (2026-07-07)

### Reviewer activity

- Read spec §1–§10 + plan (both pages) end-to-end; validated spec `v8` was
  the CLEAN one and cross-referenced the `revision log` for pre-existing
  fixed-and-closed findings (do not re-raise).
- Read the 4 diffs and inspected the whole file for the write-site.
- Traced supporting non-diff code to understand correctness:
  `observerstore/capability_snapshots_writer.go` (WriteSnapshot semantics,
  `ErrSnapshotContainsSecret` path, ablation short-circuit); `internal/
  capability/snapshot.go` (`NewSnapshot` shape rules, `JSONContainsRawToken`
  regex catalogue, `SetDisableUpload`+atomic mirror, `CanonicalJSON`);
  `internal/observerweb/server.go` (`authenticate()` → `identityFromToken`
  → `static.Resolver.Resolve` → `SQLiteStore.ValidateToken`); `internal/
  driver/tools.go` (`observerRelay()` accessor + `logHelperErr`).
- Cross-checked test harnesses `newTestHandler` (observerweb/`server_test.go`)
  and `newTestTools` (driver/`tools_test.go`) to confirm the new tests
  correctly seed a `RoleDriver` agent + bearer token → `authenticate()`
  succeeds without needing `agentserver` external-identity flow.
- Spawned Explore subagent (`a867a0e288b403317`, 21 tool calls,
  ~10 min) to independently audit 11 concrete P0/P1 threat vectors
  (secret leak, forgery, ablation bypass, DoS, race, nil-deref,
  weak-assertion tests, SQL injection, HTTP method/auth gaps, spec/code
  delta, 422-marker false-positive).

### Ran

```
go vet ./internal/driver/ ./internal/observerweb/
go test ./internal/driver/ ./internal/observerweb/ -race -timeout 120s
go test ./internal/driver/ -race -run "TestDryRunContract|TestObserverRelay_WriteCapabilitySnapshot" -v -timeout 120s
```

All green. Every new test PASSes; no `-race` warnings.

### Adversarial threat-vector walk (both reviewer + subagent findings)

| # | Vector | Verdict |
|---|---|---|
| 1 | `logHelperErr(err)` at capability_tools.go:315 leaks attacker fields | **SAFE**. Traced every branch of `WriteCapabilitySnapshot`'s error return: (a) `canonicalize snapshot: %w` — stdlib text over typed `Snapshot`, no attacker string; (b) `snapshot body %d bytes exceeds observer cap %d` — integers only; (c) `http.NewRequestWithContext` errors — operator URL only; (d) `r.http.Do(req)` — network text; (e) `observer capability_snapshots status %d: %s` — body is bounded 1 KiB and the observer handler emits **only** ten fixed strings (all read at server.go:1450-1487), zero echo of request body. So the audit sink at `tools.go:279` sees no attacker bytes. |
| 2 | Attribution forgery (body `agent_id`/`workspace_id`) | **SAFE**. Handler decode struct has only `Snapshot json.RawMessage`; extras ignored. Test `TestCapabilitySnapshots_AttributionFromAuth_NotBody` genuinely POSTs `{"agent_id":"drv-VICTIM","workspace_id":"ws-VICTIM","snapshot":…}` and asserts zero usage rows for both forged ids AND two rows under real workspace `ws-A`. |
| 3 | `NoDryRun` bypass | **SAFE**. `capability_tools.go:259` short-circuits BEFORE the `if len(args.CapabilitySnapshot) > 0` block; `TestDryRunContract_NoDryRunAblation_NoSnapshotWrite` asserts `obs.calls == 0`. |
| 4 | `NoCapabilityDiscovery` bypass | **SAFE**. Double-guard as specified. Driver-side guard at capability_tools.go:300 (`IsUploadDisabled()` reads atomic mirror, race-free per snapshot.go:663 doc). Observer-side guard inside `observerstore.WriteSnapshot`. Tests L13 + L13b independently pin both surfaces. |
| 5 | Body-size DoS | **SAFE**. Driver pre-caps `len(body) > 256 KiB`; observer wraps `http.MaxBytesReader(w, r.Body, defaultMaxEventBodyBytes=256 KiB)`. `TestCapabilitySnapshots_TooLarge_413` posts a valid-JSON >300 KiB body and asserts 413. Minor: driver's pre-cap is on `body` (canonical snapshot) but sent payload is `{"snapshot":<body>}` (+~14 wrapper bytes); a snapshot exactly at 256 KiB would pass the driver pre-cap and get 413 from the observer. Cosmetic diagnostic; degrades gracefully to warning. Not P0/P1. |
| 6 | Race on `DisableUpload` / `nowUTC` globals | **SAFE**. `DisableUpload` reads go through `IsUploadDisabled()` = atomic Load. Writer (`SetDisableUpload`) does atomic Store then raw bool. Tests toggle via `SetDisableUpload` and use `t.Cleanup` restore; none call `t.Parallel()`. `nowUTC` race is pre-existing writer-side and out of scope (documented in writer test file header). |
| 7 | Nil deref / panic | **SAFE**. `WriteCapabilitySnapshot` has `if r == nil { return nil }`. Handler asserts `managed, ok := h.s.(observerstore.ManagedStore)` and returns 503 before `.DB()`. `if report.Warnings == nil { report.Warnings = []string{} }` explicit init. |
| 8 | Weak-assertion tests | **NONE**. `AttributionFromAuth_NotBody` posts real forged fields, asserts zero forged-id rows AND full auth-id counts. `SecretScanRejected_422` uses free-form `Tools[].Version` (accepted by `NewSnapshot`) so the observer-side secret-scan is actually exercised. `MalformedSnapshot_LogDoesNotLeakSecret` greps captured stderr for `ghp_…` and `NOT_A_VALID_KIND` — genuine leakage check. |
| 9 | SQL injection | **SAFE**. `insertCapabilitySnapshotSQL` / `insertCapabilitySnapshotUsageSQL` are file-level `const` strings; `?` placeholders only (verified writer.go:34-51). |
| 10 | HTTP method/auth-gate ordering | **SAFE**. Handler order: method → authenticate → role → managed-store → body. Tests pin: `MethodNotPost_405`, `AuthNotDriver_403`. Auth failure short-circuits before body read. |
| 11 | `strings.Contains(err.Error(), "status 422")` false positive | **P2 at most**. All non-422 observer responses have fixed-text bodies without the substring `"422"`. The only real-world misfire is a 422 caused by `"invalid snapshot shape"` (upstream NewSnapshot reject) being mislabelled as `(secret scan)` in `report.Warnings` — but the driver already runs `NewSnapshot` locally at capability_tools.go:271 with the same snap, so a version-matched deploy will never see the observer-side shape 422. Version-skew impact = wrong diagnostic label, no security consequence, no state change. Logged below as P2. |
| 12 (my own) | Missing `http.Client` timeout on `ObserverRelay.http` | **NOT INTRODUCED BY THIS PR** (pre-existing at observer_relay.go:53, all 6+ existing methods share it). The new `WriteCapabilitySnapshot` uses the same `http.DefaultClient`. Timeout is provided via `ctx` from the caller (`dryRunContractTool.Call`'s incoming ctx). Pre-existing; separate PR territory. |
| 13 (my own) | Warning slice unbounded across repeated calls | Report is per-call, freshly allocated in `analyzeContractCapabilities`; slice bounded ≤ 1 per call. Safe. |
| 14 (my own) | `NewSnapshot` twice (driver-side + observer-side) burns CPU | Correct-by-design: driver runs it locally to catch shape errors early (returns fixed classifier); observer re-runs to canonicalise. Cheap check, both cases return early on error. Not an issue. |

### Findings this round

- **P0**: 0
- **P1**: 0
- **P2**: 1
  - Warning classifier heuristic `strings.Contains(err.Error(), "status 422")`
    unconditionally rewrites any 422 to `"rejected (secret scan)"`.
    A hypothetical version-skew scenario (driver older than observer, or
    a future 422 code added to the observer) would mislabel the warning.
    Recommended forward-compat: pattern-match on the specific fixed text
    `"snapshot contains raw token; rejected by secret scan"` in the 1 KiB
    body substring instead of the status code. Deferred.
- **P3**: 0

### Verdict

**CLEAN** — 0 P0 + 0 P1. No commits required.

---

## Summary

| Round | P0 fixed | P1 fixed | P2 logged | Verdict |
|---|---|---|---|---|
| 1 | 0 | 0 | 1 | CLEAN |
