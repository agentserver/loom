# WT-2-driver-promotion-chain — Sub-B6 plan

Implementation plan for `docs/specs/wt2-driver-promotion-chain-B6.spec.md`.
TDD throughout: every step lists tests first, code second. Section
references target the spec.

## Step order

Ordered so each step compiles + passes tests before the next starts. If
you skip ahead, `go build` breaks mid-step, obscuring test intent.

### Step 1 — `internal/promotionaudit/reason.go` + tests (spec §2.4, §7 (b), §7 (f))

Tests first:

- `TestReason_ParseAcceptsAllFour` — one subtest per enum literal.
- `TestReason_ParseRejectsCaseVariant` — `"Explicit_User_Request"`.
- `TestReason_ParseRejectsWhitespace` — `" explicit_user_request "`, `"explicit_user_request\n"`.
- `TestReason_ParseRejectsEmpty` — `""`.
- `TestReason_ParseRejectsUnknown` — `"foo"`.
- `TestReason_ParseRejectsUTF8Lookalike` — Cyrillic `а` (U+0430) in place of ASCII `a`.
- `TestReason_ErrorMessageDoesNotEchoInput` (§7 (f)) — assert
  `err.Error()` does NOT contain the caller-supplied raw string, so a
  secret-shaped input cannot leak through the error path. Fixed error
  message per §7 (f): `"promotion_reason must be one of:
  explicit_user_request, driver_agent_inferred, batch_import, ci_seed"`.
- `TestReason_StringMatchesLiteral` — `Reason.String()` == the same literal.

Then implement `type Reason string` + 4 consts + `Parse(s string)
(Reason, error)` + `ErrInvalidPromotionReason` sentinel.

### Step 2 — `internal/promotionaudit/fields.go` + tests (spec §3.1, §7 (a), §7 (c), §7 (e), §7 (g))

Tests first:

- `TestAuditFields_ValidateAcceptsCanonical` — a fully-populated valid record.
- `TestAuditFields_RejectsMissingUserID` (§7 (a)).
- `TestAuditFields_RejectsMissingThreadID` (§7 (a)).
- `TestAuditFields_RejectsMissingCandidateTaskID` (§7 (a)).
- `TestAuditFields_RejectsShortUserID` — 7 chars, one below the `{8,}` bound.
- `TestAuditFields_RejectsLongUserID` — 129 chars.
- `TestAuditFields_RejectsUserIDWithSlash` — `"abc/../x1"` (path traversal).
- `TestAuditFields_RejectsUserIDWithQuote` — `"abc'--xx"` (SQL meta).
- `TestAuditFields_RejectsUserIDWithLikeWildcard` — `"ab%_xxxx"`.
- `TestAuditFields_RejectsUserIDWithSpace` — `"abc defg"`.
- `TestAuditFields_UserIDNotEmailShape` (§7 (e)) — `"x@y.zzz01"` — regex
  character class rejects `@` and `.`.
- `TestAuditFields_RejectsUnknownAction` — anything not
  register/unregister/install.
- `TestAuditFields_RejectsUnknownReason` — reused from Step 1 via `Parse`.
- `TestAuditFields_AcceptsEmptyWorkspaceIDForInstall` (§2.3) — install
  with `WorkspaceID=""` is allowed (offline install).
- `TestAuditFields_RejectsEmptyWorkspaceIDForRegister` — register /
  unregister MUST have workspace.
- `TestAuditFields_RegistryHashAfterFormat` — must be 64 lowercase hex
  chars when non-empty; empty allowed only when `Action==ActionInstall`
  (offline install has no registry effect).
- `TestAuditFields_StageMustBeLowercaseOrEmpty` — B6 always passes empty;
  the invariant is still enforced now so B2 gets a clean surface.
- `TestAuditFields_ConsumerViewJoinKeyPresent` (§7 (g)) — a row where
  `CandidateSourceTaskID` is empty is rejected; the two sentinel values
  `ci-seed-<workspaceID>` and `batch-import-<workspaceID>` are accepted
  when `Reason` matches (invariant: sentinel iff reason ∈ {ci_seed,
  batch_import}, tested both directions).

Then implement `AuditFields` struct + `Validate` + the two regex
package-level `var`s (compiled once):

```go
var userIDRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var threadIDRE = userIDRE
var candIDRE   = userIDRE
var mcpNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var stageRE    = regexp.MustCompile(`^[a-z_]{1,64}$`)
```

Sentinels: `ErrMissingField`, `ErrFieldFormat`, `ErrUnknownAction`,
`ErrInvalidRegistryHash`, `ErrMissingJoinKey`, `ErrSentinelReasonMismatch`.

### Step 3 — `internal/observerstore/schema.sql` DDL append + drift test (spec §3.2, §7 (c))

Tests first (in `internal/observerstore/store_test.go` — the file
already has parallel schema-shape tests):

- `TestSchema_PromotionAuditTableExists` — open a fresh store, run
  `PRAGMA table_info(promotion_audit)`, assert the exact column list
  and types + the two CHECK constraints via `sql_master`.
- `TestSchema_PromotionAuditIndexesExist` — three
  `idx_promotion_audit_*` indexes.
- `TestSchema_PromotionAuditDDLIdempotent` — apply the schema twice, no
  error.
- `TestSchema_PromotionAuditRejectsBadEnumViaCheck` — direct SQL insert
  with `action='bogus'` — expect constraint violation.

Then append the DDL to `internal/observerstore/schema.sql` per spec §3.2.
No `ensureColumns` migration needed on first commit — the CREATE TABLE
IF NOT EXISTS covers fresh DBs.

### Step 4 — `internal/promotionaudit/writer.go` + tests (spec §3.1, §7 (c), §7 (i))

Tests first:

- `TestSQLiteWriter_WritesCanonicalRow` — assert every column matches
  the input record.
- `TestSQLiteWriter_ParameterizedSQL_NoInjection` (§7 (c)) — write a
  row with `MCPName = "legal_name"` but where a synthetic
  `PromotedByUserID` uses a value that WOULD be an injection if
  concat'd (the regex prevents this in practice, but the test
  double-checks the writer path doesn't concat by construction: we
  temporarily bypass Validate via an internal helper, insert with `';
  DROP TABLE promotion_audit; --` and assert the row lands
  verbatim + the table still exists).
- `TestSQLiteWriter_RespectsNoObserverAblation` (§7 (i)) — flip
  `observer.DisableTelemetry = true`, call Write, expect 0 rows AND one
  log line matching `[ablation] NoObserver: dropped promotion_audit
  row_id=<...>`. Use `iotest.NewSyncBuffer`-style log capture via
  `log.SetOutput(&buf)` + `t.Cleanup` restore.
- `TestSQLiteWriter_ValidationStillRunsUnderNoObserver` — with
  `DisableTelemetry=true`, malformed input still returns the validation
  error (matching the WT-1-run-schema pattern).
- `TestSQLiteWriter_RowIDPrefix` — every generated row_id starts with
  `promaud_`.
- `TestSQLiteWriter_TSSerialisedUTC` — pass a non-UTC time, assert
  stored value ends in `Z`.
- `TestSQLiteWriter_ConcurrentSafeSameStore` — 10 goroutines × 100
  writes each, assert final `COUNT(*) == 1000` and no error.

Then implement `Writer` interface + `SQLiteWriter` struct + `Write` +
`Close`. Writer takes `*sql.DB` (matches `evalrun.NewSQLWriter` shape).
`init()` MUST NOT register any ablation flag — this writer respects the
EXISTING `NoObserver` (registered by `internal/evalrun`); no new
registration.

### Step 5 — `internal/driver/registryhash.go` + tests (spec §3.3, §7 (d), §7 (j))

Tests first:

- `TestComputeRegistryHash_EmptyNamesEqualsSHA256OfEmptyBytes` (§7 (d))
  — assert the constant value
  `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- `TestComputeRegistryHash_OrderIndependent` (§7 (d)) — same set of
  names in two different input orders → same hex.
- `TestComputeRegistryHash_ChangesOnDescriptorEdit` (§7 (d)) — swap one
  entry's descHash → hex changes.
- `TestComputeRegistryHash_ChangesOnNameEdit` — rename an entry → hex
  changes.
- `TestComputeRegistryHash_DeterministicAcrossRuns` — call twice, same result.
- `TestLastRegistryHash_ZeroValueReturnsEmptyHash` — brand-new process,
  `LastRegistryHash()` == the empty-bytes sha256 constant, NOT `""`.
- `TestSetLastRegistryHash_Publishes` — call `Set`, `Get` returns it.
- `TestLastRegistryHash_ConcurrentReadWrite` — 8 readers, 4 writers, no
  race; run under `-race`. Assert `LastRegistryHash()` never returns `""`.
- `TestDriverRegistryView_UndercountsUntilFirstRegister` (§7 (j)) — on a
  fresh view (no prior register), the per-slave map for a given
  slave_id is empty; call `noteRegister(slave, name, specHash)`, call
  again for a second name, then `LastRegistryHash()` reflects the two.
  Assert that no code path in this file opens a network call or reads a
  slave-side file. (Implementation-level: use a build-tag-free tree
  search of the file for `net.`, `http.`, `os.Open`, `os.ReadFile` and
  assert none present in the `registryhash*.go` files.)
- `TestDriverConstructor_NeverProducesAPIKeyShapedThreadID` (§7 (e)) —
  drive the (to-be-written) `deriveDriverThreadID(codexThread codextype)
  string` helper against a table of realistic thread inputs; assert
  none produce an `sk-` prefix. This tests the constructor, not the
  writer.
- `TestAudit_PerfBench_ConditionalOnShortOrCI` (§7 (h)) — run
  `ComputeRegistryHash` on a synthetic 1000-name registry
  unconditionally (keeps the compute path in the `-race` net), then
  guard the wall-clock assertion with
  `if testing.Short() { t.Skip("short mode") }`. Assert `< 5 ms` when
  not skipped. `go test -short ./...` bypasses the threshold; CI runs
  the full suite (no `-short`) and exercises it. Location: next to
  `TestComputeRegistryHash_*` in `registryhash_test.go`.

Then implement `ComputeRegistryHash`, `LastRegistryHash`,
`SetLastRegistryHash`, the private `sync.RWMutex`, and the per-slave
view helpers `noteRegister(slave, name, specHash)` /
`noteUnregister(slave, name)` / `snapshotAll() (names, descHashFn)`.

### Step 6 — `register_mcp_tool.go` + tests (spec §2.1, §3.4, §7 (a), §7 (i))

Tests first (extend `register_mcp_tool_test.go`):

- `TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingUserID` (§7 (a)).
- `TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingThreadID`.
- `TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingReason`.
- `TestRegisterSlaveMCP_RequiresAllFourAuditFields_MissingCandidateTaskID`.
- `TestRegisterSlaveMCP_RejectsMalformedUserID` — pass `"x"`, expect
  `MCPToolError` with `FailContractViolation`.
- `TestRegisterSlaveMCP_RejectsMalformedThreadID` (spec §7 (a) test row
  `TestRegister_MalformedThreadID_Rejects`) — pass `"th 01"` (space)
  and `"th'--"` (SQL meta) in subtests; both must reject with
  `FailContractViolation`.
- `TestRegisterSlaveMCP_RejectsMalformedCandidateTaskID` (spec §7 (a)
  test row `TestRegister_MalformedCandidateTaskID_Rejects`) — pass
  `"../etc/passwd"` (path traversal) and 7-char short input in
  subtests; both must reject with `FailContractViolation`.
- `TestRegisterSlaveMCP_RejectsUnknownReason` — pass `"garbage"`.
- `TestRegisterSlaveMCP_HappyPath_WritesAuditRow` — full valid call,
  assert `promotion_audit` gets 1 row with all fields matching.
- `TestRegisterSlaveMCP_HappyPath_PublishesRegistryHash` — after
  register, `driver.LastRegistryHash()` != the empty-bytes sha256.
- `TestRegisterSlaveMCP_AuditWriteFailure_DegradesButRegistrationStillSucceeds` (§3.4
  step 6) — inject an audit-writer error, assert (a) tool result still
  returns success, (b) `logHelperErr` was invoked with helper
  `"promotion_audit"`, (c) NO delegate task rollback attempted.
- `TestRegisterSlaveMCP_AuditWriteRespectsNoObserver` — flip
  `DisableTelemetry`, ensure the tool call returns success (audit write
  is no-op-with-log per Step 4).

Then modify `register_mcp_tool.go`:

- Extend the anonymous args struct + `InputSchema` string.
- Move regex validation into a `validateAuditFields(args)` helper that
  returns `*MCPToolError` — call it BEFORE `resolveAvailableAgent`
  (fast fail).
- After `waitDelegatedTask` success, run the per-slave view update +
  hash compute + audit write per §3.4 (this is the only place that
  imports `internal/promotionaudit`).

Threading dependency injection: the tool needs a `promotionaudit.Writer`.
Add a field `promoAudit promotionaudit.Writer` to `Tools`, wired at
construction. For tests, `newTestTools` (already exists) grows a
`WithPromoAudit(writer)` option — the default is an in-memory SQLite
writer.

### Step 7 — `unregister_mcp_tool.go` + tests (spec §2.2)

Same shape as Step 6 but for the unregister tool. Tests parallel Step
6 verbatim, s/register/unregister/. Additional test:

- `TestUnregisterSlaveMCP_ClearsSlaveViewEntry` — register X, unregister X, `LastRegistryHash()` returns to the pre-register value.

### Step 8 — `cmd/mcp-userspace/cmd_install.go` + tests (spec §2.3)

Tests first (create `cmd/mcp-userspace/cmd_install_test.go` if not
present):

- `TestInstall_RequiresAllFourAuditFlags` — table-driven, one subtest
  per missing flag.
- `TestInstall_RejectsMalformedUserID`.
- `TestInstall_HappyPathWithWorkspace_WritesAuditRow` — assert row
  `action='install'`, `workspace_id=<given>`.
- `TestInstall_HappyPathWithoutWorkspace_WritesAuditRowWithEmptyWorkspace`
  (§2.3 offline).
- `TestInstall_AuditWriteFailure_ExitsNonZero` — the CLI is
  installation-level; unlike the driver tool a failed audit for the
  cmd is a hard error because there is no persistent user context to
  fall back on.

Then modify `cmd_install.go` to:
- Add the four flags via `fs.String` + validate BEFORE the tarball fetch.
- Open a short-lived `*sql.DB` against the observer store path (path
  discovered from existing config — reuse whatever `mcp-userspace`
  already uses for `client.Install`).
- Call `promotionaudit.SQLiteWriter.Write`.

### Step 9 — Doc comment on `internal/evalrun/schema.go` (spec §9)

One-line doc-comment update on `DynamicMCPRegistryHash` pointing at
`driver.LastRegistryHash()`. NO other change in this file.

### Step 10 — Optional Postgres schema (spec §9)

Not modified. Assert-only test: none.

## Verification (Step 11, before commit)

Run in this order — each MUST pass before commit:

```
cd multi-agent
go vet ./internal/promotionaudit/... ./internal/driver/... ./internal/observerstore/... ./cmd/mcp-userspace/...
go test ./internal/promotionaudit/... ./internal/driver/... ./internal/observerstore/... -count=1 -race
go test ./internal/promotionaudit/... -count=1 -run . -race -shuffle=on   # shuffle=on catches order dependency
```

## Commit shape

Single commit at end of B6:

```
WT-2-driver-promotion-chain B6: promotion-audit fields + audit table + registry-hash publisher

- New internal/promotionaudit package: 4-field audit record, enum-typed
  Reason, SQLite writer respecting NoObserver ablation.
- register_slave_mcp / unregister_slave_mcp / mcp-userspace install
  now require promoted_by_user_id / driver_thread_id / promotion_reason
  / candidate_source_task_id (regex-bounded, enum-validated).
- observerstore/schema.sql: promotion_audit table with CHECK-enforced
  action + reason enums.
- internal/driver/registryhash.go: per-slave in-process view +
  ComputeRegistryHash + LastRegistryHash publisher for D1
  dynamic_mcp_registry_hash.
- Docs: docs/specs/wt2-driver-promotion-chain-B6.{spec,plan}.md.

Security: parameterized SQL; NoObserver dropped-row log preserves audit
story; regex bounds prevent SQL meta / path traversal; enum types stop
free-string reason at the caller; secret-shaped inputs fail fast at
the driver-side constructor per §7 (e).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```
