# WT-2-flag-integration — Plan

> Companion to `wt2-flag-integration.spec.md`. TDD order **within
> each numbered item**: write the failing test first, then the
> minimal impl, then the refactor. The item ordering below moves
> outside-in: types + pure functions (Phase A) → mutation seams
> (Phase B–C) → CLI-facing helpers (Phase D) → wire-up + CSV
> migration (Phase E) → README (Phase F) → cross-cutting audit
> tests (Phase G). This matches the ordering convention documented
> in `wt2-baselines.plan.md`. Every test named below maps to at
> least one spec §7 (a)–(h) security item.

## 0. Baseline HEAD

`origin/paper/v3-integration` at `1c41b29`.

## 1. Files (created in this order)

Everything lands in `multi-agent/tools/eval/runner/` under
`package main` (spec §2). No new sub-package.

### Phase A — pure functions + types (no I/O, no ablation mutation)

0. `runner.go` **Opts extension** (types-first): append two fields
   to the existing `Opts` struct:
   `AblationFlags []ablation.FlagName` +
   `BaselineName string`. Add matching `TestOpts_HasAblationFields`
   in `runner_test.go` (reflect-based check the fields exist with
   the expected types). Zero behaviour change; existing direct
   `Run(ctx, Opts{})` callers compile unchanged (nil / "" defaults).
0b. `writer.go` **RunRow + CSV extension** (types-first): append
   `BaselineOrAblation string` to `RunRow`; append
   `"baseline_or_ablation"` to `CSVColumns()`; append the same
   value to `rowAsCSVRecord`. Test
   `TestCSVColumns_IncludesBaselineOrAblation` asserts the column
   is at the last position; existing frozen-header tests still
   pass because the migration is append-only.

1. `flags.go` **skeleton** — const declarations
   (`DefaultBaselineName = "full_loom"`,
   `MaxBaselineOrAblationLen = 200`,
   `EnvNoAcceptanceGate = "LOOM_ABLATION_NOACCEPTANCEGATE"`),
   sentinel errors
   (`ErrBaselineOrAblationInvalid`,
   `ErrBaselineOrAblationTooLong`),
   pre-compiled regex vars
   (`baselineNameRe`, `ablationLabelRe`),
   `flagDescriptions map[ablation.FlagName]string`
   (initially just the 8 canonical entries),
   and empty type stubs (`AblationList`,
   the `String() / Set() / Values()` method signatures).
   No behaviour yet.
2. `flags_test.go` — `TestConstantsAndRegex_Sanity`: the two regexes
   accept their canonical example and reject one obvious wrong
   input; `DefaultBaselineName` matches `baselineNameRe`.
3. Implement `ValidateBaselineOrAblation(s string) error`.
   Tests (each fails first, then passes):
   - `TestValidateBaselineOrAblation_BaselineHappy` — `"full_loom"`,
     `"my_run"` return nil.
   - `TestValidateBaselineOrAblation_AblationHappy` — `"NoObserver"`,
     `"NoObserver+NoAcceptanceGate"` return nil.
   - `TestValidateBaselineOrAblation_Empty` — `""` returns
     `ErrBaselineOrAblationInvalid`.
   - `TestValidateBaselineOrAblation_TooLong` — a 201-char string
     returns `ErrBaselineOrAblationTooLong`.
   - `TestValidateBaselineOrAblation_MalformedAblation` —
     `"NoObserver+"`, `"NoObserver+++"`, `"Noobserver"` (lowercase
     first letter) all reject.
   - `TestValidateBaselineOrAblation_MalformedBaseline` — `"FullLoom"`
     (CamelCase, doesn't match either regex),
     `"has spaces"`, `"has/slash"` all reject.
4. Implement `ComputeBaselineOrAblation(flags []ablation.FlagName, baseline string) (string, error)`.
   Tests:
   - `TestComputeBaselineOrAblation_BaselineBranch` — empty flags +
     `"full_loom"` returns `"full_loom"`.
   - `TestComputeBaselineOrAblation_BaselineDefaultUsed` — empty
     flags + `""` — behaviour is `Run`'s responsibility (it
     substitutes the default), so this test asserts
     `ComputeBaselineOrAblation(nil, "")` returns
     `ErrBaselineOrAblationInvalid` (validator rejects empty), NOT
     that it silently substitutes.
   - `TestComputeBaselineOrAblation_AblationSingle` — one flag
     returns just its name.
   - `TestComputeBaselineOrAblation_AblationJoined` — two flags
     sorted alphabetically joined by `+`.
   - `TestComputeBaselineOrAblation_OrderStable` — 12 random
     permutations of a 4-flag set all produce byte-identical output
     (spec §7(b)). Uses `rand.New(rand.NewSource(0xDEADBEEF))` for
     the shuffle so the test is deterministic (no `time.Now()`
     seed) — the shuffled set is generated inside the test from
     `[]ablation.FlagName{NoObserver, NoAcceptanceGate, NoDryRun, NoRegistryLookup}`
     (no external helper needed).
   - `TestComputeBaselineOrAblation_DedupeDirectCaller` — passing
     `[fn("NoObserver"), fn("NoObserver")] (where fn = ablation.FlagName)` returns `"NoObserver"`, not
     `"NoObserver+NoObserver"` (spec §4 step 2 dedup; spec §7(b)
     correctness gate).
   - `TestComputeBaselineOrAblation_IgnoresBaselineWhenFlagsPresent`
     — passing `flags: [ablation.FlagName("NoObserver")], baseline: "my_run"` returns
     `"NoObserver"`, NOT `"my_run"`; a stderr log is expected but
     not asserted (out-of-scope for this pure func).
   - `TestComputeBaselineOrAblation_AllEightFlagsFitCap` — pass all
     8 flags, assert `len(returned) < MaxBaselineOrAblationLen`.
     This is the plan.md §3 §7(h)-related canary from spec
     `TestBaselineOrAblationCapCoversAll8Flags`.

### Phase B — the flag-list `flag.Value` type

5. Implement `AblationList` as `flag.Value` (`String()`, `Set()`,
   `Values() []ablation.FlagName`). Tests:
   - `TestAblationList_SingleEntry` — `Set("NoObserver")` →
     Values() = `[ablation.FlagName("NoObserver")]`.
   - `TestAblationList_CommaJoined` — `Set("NoObserver,NoAcceptanceGate")`
     → Values() = `[fn("NoObserver"), fn("NoAcceptanceGate")] (where fn = ablation.FlagName)` (insertion order).
   - `TestAblationList_RepeatedSet` — two `Set` calls append and
     dedupe.
   - `TestAblationList_DedupWithinSingleSet` —
     `Set("NoObserver,NoObserver")` returns nil, Values() =
     `[ablation.FlagName("NoObserver")]`.
   - `TestAblationList_UnknownFlagRejected` — `Set("NoTpedContracts")`
     returns error wrapping `ablation.ErrUnknownFlag` (spec §7(a.1)
     typo defence).
   - `TestAblationList_EmptyEntryRejected` — `Set("")` and
     `Set(",,")` return errors.
   - `TestAblationList_WhitespaceEntry_Trimmed` —
     `Set("  NoObserver  , NoAcceptanceGate ")` succeeds with the
     trimmed names.
   - `TestAblationList_WhitespaceOnlyEntryRejected` — `Set("   ")`
     rejects.
   - `TestAblationList_String_Roundtrip` — after several `Set`
     calls, `String()` returns a comma-joined stable form (used by
     `-h` output).

### Phase C — registry binding + Python bridge + env scrub

6. Implement `applyAblationFlagsTo(reg *ablation.Registry, flags []ablation.FlagName) error`
   in three phases per spec §2.1. This is where the mutation lives.
   Tests use a **fresh registry** (`ablation.NewRegistry()`) for
   the negative paths and `ablation.Default` for the smoke path:
   - `TestApplyAblationFlagsTo_UnknownFlagRejectsBeforeMutating` —
     fresh registry, register only `NoObserver`, call
     `applyAblationFlagsTo(reg, []ablation.FlagName{ablation.FlagName("NoObserver"), ablation.FlagName("NoTpedContracts")})`.
     Assert (a) the error wraps `ErrUnknownFlag`, (b) the
     `NoObserver` target *bool is STILL false (Phase 1 validation
     ran before Phase 3 apply). Spec §7(a) "no exit-2 path mutates
     global state" invariant test.
   - `TestApplyAblationFlagsTo_UnregisteredFlagRejectsBeforeMutating`
     — fresh registry (no Register calls); call
     `applyAblationFlagsTo(reg, [ablation.FlagName("NoObserver")])`. Assert error wraps
     `ErrNotRegistered`. This is the spec §7(h) unreachability
     escape hatch — the ONLY way to exercise `ErrNotRegistered`
     without dismantling the runner binary's link graph.
   - `TestApplyAblationFlagsTo_ResetPhaseClearsPriorFlags` — fresh
     registry, register 2 flags, `applyAblationFlagsTo(reg, [flag1] (where flag1 = ablation.FlagName("NoObserver"), etc.))`,
     then `applyAblationFlagsTo(reg, []ablation.FlagName{flag2})`. Assert flag1's
     target is now false and flag2's is true. Spec §7(a.4) reset
     invariant.
   - `TestApplyAblationFlagsTo_NilResetsAll` — fresh registry,
     register 2 flags, `applyAblationFlagsTo(reg, []ablation.FlagName{flag1, flag2})`
     (both true), then `applyAblationFlagsTo(reg, nil)`. Assert
     BOTH targets are false. Spec §7(a.4) "nil = reset everything"
     invariant.
   - `TestApplyAblationFlagsTo_PhaseThreeSetsBoolsAndSyncsAtomic` —
     use `ablation.Default` (has the atomic-mirrored flags wired);
     `applyAblationFlagsTo(Default, [NoDryRun])`; assert
     `validator.IsDryRunDisabled() == true` (proves the SyncHook
     fired). Use `t.Cleanup` + `applyAblationFlagsTo(Default, nil)`
     to reset. Cross-thread safety not asserted (spec says
     pre-run-only mutation).
   - `TestApplyAblationFlags_AllKnownFlagsBind` — this is the
     spec §7(h)-`TestApplyAblationFlags_AllKnownFlagsBind` link-time
     test. Sub-test per canonical flag; each calls
     `ApplyAblationFlags([]FlagName{fn})` against `Default` and
     asserts nil error. If a Phase-2 worktree ships a canonical
     constant without linking the owner package into the runner's
     import graph, this test names the missing package.
7. Implement `ScrubAmbientAblationEnv()`. Tests:
   - `TestScrubAmbientAblationEnv_ClearsParent` — `t.Setenv` the
     Python-bridge env var, call `ScrubAmbientAblationEnv`,
     assert `os.Getenv(...)` returns `""`. Spec §7(a.2) ambient-env
     defence.
   - `TestScrubAmbientAblationEnv_Idempotent` — call twice in a
     row; second call is a no-op (spec §5.4 idempotency).
8. Implement Python-bridge sync inside `applyAblationFlagsTo`. Tests:
   - `TestApplyAblationFlags_PythonBridgeExportsEnv` — call
     `ApplyAblationFlags([ablation.FlagName("NoAcceptanceGate")])`, assert
     `os.Getenv("LOOM_ABLATION_NOACCEPTANCEGATE") == "1"`.
     `t.Cleanup` resets.
   - `TestApplyAblationFlags_NoBridgeWhenFlagUnset` — call
     `ApplyAblationFlags([ablation.FlagName("NoObserver")])` (a non-bridge flag), assert
     the acceptance-gate env var stays empty.
   - `TestPythonBridgeKeysAreCanonical` — grep-audit test: iterate
     `ablationEnvExports` keys and assert each is a member of
     `ablation.KnownFlags()`. Catches a rename in registry.go
     without a corresponding map key update.

### Phase D — `--list-ablations` renderer

9. Implement `ListAblationsText() string` — pure function, no I/O.
   Tests:
   - `TestListAblations_CountMatchesRegistry` — assert BOTH
     `strings.Count(out, "\n") == len(ablation.KnownFlags())`
     AND `len(ablation.Default.List()) == len(ablation.KnownFlags())`
     (spec §7(h)). The first assertion covers "flagDescriptions
     shrank"; the second covers "canonical constant added without a
     Register site linked into the runner binary" — without the
     second assertion, `--list-ablations` could shrink to 7 while
     the flag-count test still passed against 7.
   - `TestListAblations_LineFormat` — every line matches
     `^No[A-Z][A-Za-z0-9]{2,31}\t\[[A-D][0-9]\] [A-Za-z][A-Za-z0-9 ,.;()-]{4,119}\.$`
     (spec §7(d)).
   - `TestListAblations_NoSensitiveContent` — for each line, no
     substring in `["/","\\","LOOM_","AGENTSERVER_","OPENAI_","http://","https://","skills/","internal/","tools/","token","secret","bearer","key","config.toml",".yaml",".codex"]`.
     Note `key` covers "api key" / "api_key" / "bearer key" —
     spec §7(d) `credential-shaped strings (token, secret, key, bearer)`.
     Case-insensitive match on the substrings (a description
     saying "Key" instead of "key" still triggers).
     Spec §7(d) enforcement.
   - `TestListAblations_SortedAscending` — split on `\n`, verify
     the flag-name field is sorted ascending.
   - `TestListAblations_DescriptionCoversAll8Flags` — for each
     `KnownFlags()` entry, assert `flagDescriptions[fn]` is
     non-empty. Catches "new flag added to registry, description
     forgotten".

### Phase E — main.go / runner.go wire-up

10. (Opts / RunRow / CSV additions already landed in Phase A
    items 0 and 0b — see above. Phase E starts at item 11.)
11. Modify `Run(ctx, opts)` per spec §2.3 flow, in EXACTLY this
    order (any deviation risks mutating global ablation state
    while a subsequent preflight still returns exit 2):

    ```
    ScrubAmbientAblationEnv()                 // NEW — first inside Run
    validateStubListen(...)                   // existing
    validateObserverDB(...)                   // existing
    validateCodexConfig(...)                  // existing
    LoadWorkloadSpec(...)                     // existing
    SetupWorkspace(...)                       // existing
    startStub(...) + waitStubReady(...)       // existing
    if err := ApplyAblationFlags(opts.AblationFlags); err != nil {  // NEW — AFTER every existing preflight
        return preflight(opts, err)
    }
    defer applyAblationFlagsTo(ablation.Default, nil)  // NEW — cleanup on any subsequent return
    derivedLabel, err := ComputeBaselineOrAblation(opts.AblationFlags, opts.BaselineName)
    if err != nil { return preflight(opts, err) }
    if err := ValidateBaselineOrAblation(derivedLabel); err != nil {
        return preflight(opts, err)
    }
    // derivedLabel is a local string; the RunRow it lands on is
    // NOT assembled here — the label plumbs through to the
    // EXISTING row-assembly site (runner.go:283), where the
    // one-line edit is `BaselineOrAblation: derivedLabel,`.
    // This preserves the WT-2-e1e6-probes / metric-extract
    // ordering: AgentStage → oracle → commit_meta → git_emails →
    // probe drains all still happen BEFORE the row is built.
    AgentStage(...)                           // existing
    resolveOraclePath(...)                    // existing
    RunSubprocess(...)                        // existing
    parseOracleStdout(...)                    // existing
    probes.Emit* (Edits 1–5)                  // existing — populate emitter
    collectCommitMeta(...) + git emails       // existing
    row := RunRow{ … BaselineOrAblation: derivedLabel, … }  // existing assembly site (runner.go:283); one-line new field
    probes.MergeIntoRow(&row, records)        // existing — Edit 6 stays AFTER row assembly
    Writer.Insert + WriteCSVRow               // existing
    …                                         // rest of Run unchanged
    ```

    Tests:
    - `TestRun_LabelDerivedFromApplied_NoAblation` — direct
      `Run(ctx, Opts{})` with a mock workload; the emitted CSV row
      has `baseline_or_ablation == "full_loom"`. Spec §7(a.3)
      forgery-reject test.
    - `TestRun_LabelDerivedFromApplied_SingleFlag` — direct
      `Run(ctx, Opts{AblationFlags: []ablation.FlagName{ablation.FlagName("NoObserver")}})`; row has
      `baseline_or_ablation == "NoObserver"`.
    - `TestRun_LabelDerivedFromApplied_MultiSortedJoined` — two
      flags in reverse order; row has sorted join.
    - `TestRun_ScrubsAmbientLoomAblationEnv_BeforeSubprocess` —
      `t.Setenv` the bridge env var to `"1"`. Register an
      `Opts.AgentStage` hook (existing seam) that captures
      `os.Getenv("LOOM_ABLATION_NOACCEPTANCEGATE")` at the moment
      it runs. Call `Run(ctx, Opts{AgentStage: hook})` (no
      ablation). Assert (a) the captured value is `""` — proves
      the scrub fired BEFORE `AgentStage`, and therefore BEFORE
      any `WhitelistEnv` construction downstream; (b) the env
      var is still `""` after `Run` returns. Spec §7(a.2) direct-
      caller loophole test. This is the direct-Run equivalent of
      `TestMain_ScrubsParentEnvBeforeParse`, and asserts the
      scrub is not just a post-hoc cleanup.
    - `TestRun_DeferResetsFlagsOnHappyPath` — call
      `Run(ctx, Opts{AblationFlags: []ablation.FlagName{ablation.FlagName("NoObserver")}})`, then
      after Run returns, assert
      `evalrun.DisableTelemetry == false`. Spec §7(a.4) deferred
      cleanup test.
    - `TestRun_DeferResetsFlagsOnExit2AfterApply` — force a
      subsequent-preflight failure (bad `resolveOraclePath`) with
      an ablation flag applied; assert Run returns exit 2 AND the
      flag was reset. Spec §7(a.4).
    - `TestRun_BaselineNameCollidesWithFlag_Exit2` — direct
      `Run(ctx, Opts{AblationFlags: nil, BaselineName: "NoObserver"})`
      (a canonical flag name as baseline). Assert Result.ExitCode == 2
      and Result.Err wraps `ErrBaselineOrAblationInvalid` (the
      collision reject reuses the invalid sentinel — the collision
      check is implemented inside `ComputeBaselineOrAblation` /
      `ValidateBaselineOrAblation` as a special case of "not a
      valid baseline name"). Stderr contains the substring
      `collides with an ablation flag name` (see spec §5.3 error
      table). This closes the direct-caller variant of the CLI
      collision case (`TestMain_BaselineNameCollidesWithFlag_Exit2`).
      Spec §5.3 + §7(a.3).
    - `TestRun_BaselineNameInvalid_Exit2` — direct
      `Run(ctx, Opts{BaselineName: "Has Space"})`. Assert exit 2
      via the `baseline_or_ablation` regex reject. Spec §7(c).
    - `TestRun_ApplyAfterAllExistingPreflights_NoFlipOnEarlyReject`
      — call `Run(ctx, Opts{StubListen: "8.8.8.8:80",
      AblationFlags: []ablation.FlagName{ablation.FlagName("NoObserver")}})`; existing
      `validateStubListen` rejects; assert
      `evalrun.DisableTelemetry` is STILL false. Spec §7(a) "no
      exit-2 path from a rejected preflight mutates state".
12. Modify `main.go` per spec §2.3:
    - Top-level `os.Args[1] == "--list-ablations"` dispatch → print
      `ListAblationsText()` + exit 0. Test uses `exec.Command` on
      the built binary in an integration test (`main_smoke_test.go`
      or extension of existing).
    - Inside `runMain`: `fs.Var(&ablationList, "ablation", ...)`,
      `fs.StringVar(&baselineName, "baseline-name",
      DefaultBaselineName, ...)`. Call
      `ScrubAmbientAblationEnv()` FIRST (before `fs.Parse`).
      Stash the parsed values on `Opts.AblationFlags` and
      `Opts.BaselineName`. Tests:
    - `TestMain_ListAblations_Exit0` — `exec.Command(binary,
      "--list-ablations")` succeeds with exit 0, stdout contains 8
      lines.
    - `TestMain_UnknownFlagExit2` — `exec.Command(binary, "run",
      ..., "--ablation", "NoTpedContracts")` exits 2. Spec §7(a.1)
      end-to-end.
    - `TestMain_ScrubsParentEnvBeforeParse` — set the bridge env
      via `Cmd.Env` before starting, run with `--baseline-name
      my_run` (no ablation), assert the resulting CSV row's
      `baseline_or_ablation == "my_run"` AND (by a fixture Python
      script wired as the oracle) that the child process saw the
      env var as empty. Spec §7(a.2) end-to-end.
    - `TestMain_BaselineNameInvalid_Exit2` — invoke with
      `--baseline-name "Has Space"` (fails
      `^[a-z][a-z0-9_-]{2,63}$`); assert exit 2 and stderr matches
      `must match \^\[a-z\]`. Spec §5.3 error row.
    - `TestMain_BaselineNameCollidesWithFlag_Exit2` — invoke with
      `--baseline-name NoObserver` (a canonical flag name, would
      confuse D2 group-by if allowed); assert exit 2 and stderr
      matches `collides with an ablation flag name`. Spec §5.3
      error row.
    - `TestMain_BaselineNameEmpty_UsesDefault` — invoke with
      `--baseline-name ""` (explicit empty); `Run` substitutes
      `DefaultBaselineName`; assert exit 0 and the CSV row's
      `baseline_or_ablation == "full_loom"`. Spec §7(c)
      empty-string substitution path.

### Phase F — README

13. Create `multi-agent/tools/eval/runner/README.md` per spec §2.5.
    Tests:
    - `TestReadmeMetricMapping` — parse the README's `##` section
      for the 8-row table, assert:
      - Exactly 8 rows.
      - Every FlagName column value is in `ablation.KnownFlags()`.
      - Every "Predicted metric impact" cell is non-empty and
        non-`TBD`.
      - For each row, the `[Xn]` section prefix in the impact cell
        matches the SAME `[Xn]` that appears in
        `flagDescriptions[fn]` (single source of truth check).
      - For each row, given the `[Xn]` label:
        1. Locate the `| Xn |` table row in
           `/root/paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md`
           whose first column starts with `Xn`.
        2. Grep that row for the flag's canonical name (e.g.
           `NoDryRun`). If not found, fail with "flag <fn>
           mapped to §<Xn> but §<Xn>'s row does not mention it —
           section letter drifted".
        3. Extract every metric name (`[A-Z][a-zA-Z0-9]+Rate|`
           `[A-Z][a-zA-Z0-9]+Count|` etc. — a regex over the
           row's `metrics` column). Assert that AT LEAST ONE of
           those metric names appears verbatim in the README's
           "Predicted metric impact" cell. Empty impact cells
           and impact cells that mention only invented metric
           names (like `dry_run_missed`) fail.
      - This is the round-3+round-5 codex-review strengthening;
        catches both "wrong section letter" (`NoDryRun` labelled
        `[B5]`) and "invented metric" (`dry_run_missed`) failure
        modes.
    - `TestReadmeContainsSQLAndPandasSnippets` — grep for the
      SELECT and pandas snippets from spec §7(g); assert both
      reference `baseline_or_ablation`.
    - `TestReadmeMentionsListAblations` — grep for
      `--list-ablations` usage snippet.

### Phase G — cross-cutting drift audit tests

14. `TestFlagsGo_NarrowAblationSurface` — go/parser + go/ast walk
    over `flags.go`. The check uses `ast.Inspect` and, for every
    `*ast.SelectorExpr`, RESOLVES the FULL dotted path to a
    canonical string (e.g.
    `ablation.Default.SetByName` → `"ablation.Default.SetByName"`,
    `evalrun.DisableTelemetry` → `"evalrun.DisableTelemetry"`).
    Chained selectors are handled by recursing into the outer
    `X` before appending `Sel.Name`, so calls like
    `ablation.Default.Register(...)` do not slip past a shallow
    `X == "ablation"` check.

    The audit walks the AST and, for every SelectorExpr, resolves
    the DEEPEST fully-qualified dotted path (the same path a
    reviewer would eyeball). It then reports a **single check per
    unique path**, whether the path appears as an identifier (an
    argument, e.g. `applyAblationFlagsTo(ablation.Default, ...)`),
    as a call target (e.g. `ablation.Default.Register(...)`), or
    as a value read (e.g. `evalrun.DisableTelemetry`). Skipping
    call-target selectors would let forbidden method calls slip
    past; the audit MUST NOT skip them. Every path — argument,
    call target, or value — is compared against the whitelist.

    Two checks:

    (a) Every fully-qualified LEAF path starting with `ablation.`
    must be in the ablation whitelist:
    `{"ablation.FlagName", "ablation.ErrUnknownFlag", "ablation.ErrNotRegistered", "ablation.KnownFlags", "ablation.Default", "ablation.Default.SetByName", "ablation.Default.List", "ablation.Registry"}`.
    `ablation.Default` is on the whitelist because it's a
    required argument for `applyAblationFlagsTo(reg *ablation.Registry, ...)`;
    `ablation.Default.SetByName` and `.List` are on the whitelist
    because they're direct method calls used inside
    `applyAblationFlagsTo`.
    Note that:
      - `ablation.Default.Register` is DELIBERATELY excluded —
        `flags.go` MUST NOT re-register a flag; that is the
        owner's job.
      - `ablation.NewRegistry` is DELIBERATELY excluded — it's
        `flags_test.go`-only.
      - `ablation.NoObserver` / `ablation.NoAcceptanceGate` /
        any of the 8 named constants directly are DELIBERATELY
        excluded — `flags.go` iterates `KnownFlags()` or uses
        `ablation.FlagName("NoXxx")` string casts (§7(e)).

    (b) Every fully-qualified path starting with `capability.`,
    `contract.`, `validator.`, `driver.`, or `evalrun.` must have
    the final component start with `Sync` (i.e. a sync-hook call
    from the §6 table). Direct owner-package state access like
    `evalrun.DisableTelemetry`, `capability.disableUploadAtomic`,
    or `driver.IsNoRegistryLookup()` fails the test — the spec
    §7(e) restriction says the ONLY owner-package touchpoints
    from `flags.go` are the exported `Sync…` calls. If a future
    refactor needs to read a flag from `flags.go`, the ablation
    package should grow a `Registry.IsSet(name) (bool, error)`
    accessor first, per plan.md §4 non-goals.

    Failing paths are printed with line numbers.
    `flags_test.go` is intentionally EXEMPT from both checks
    (tests need `ablation.NewRegistry`, `validator.IsDryRunDisabled`,
    etc.).
15. `TestFlagsGoBlankImports` — parse flags.go, extract the blank
    import list, assert it matches the spec §2.1 six-package set
    exactly. Spec §7(h) drift signal.
16. `TestSyncHooksTableIsUpToDate` — for each package in the §6
    "Sync hook" column, greps its `*.go` files for the
    `atomic.Bool` or `atomic.Pointer` idiom. If the table says
    "(none)" for a package that DOES contain an atomic-mirrored
    ablation target, the test fails with a directive to add the
    sync hook. Spec §6 drift signal.
17. `TestBaselineOrAblationCapCoversAll8Flags` — compute
    `len(strings.Join(sortedFlagNames, "+"))` from
    `ablation.KnownFlags()`; assert it is strictly less than
    `MaxBaselineOrAblationLen`. Bump either the cap or the flag
    count when this test fails. Spec §7(c).

## 2. Coding order

- Phase A: 0 → 0b → 2 (write `TestConstantsAndRegex_Sanity` and
  any other test scaffolding first, expect compile errors from
  missing symbols) → 1 (add minimal skeleton to make item 2 tests
  compile, still expected to FAIL) → run tests, confirm failure →
  3 (write `ValidateBaselineOrAblation` tests, then impl to make
  them pass) → 4 (write `ComputeBaselineOrAblation` tests, then
  impl). Every item's tests are written before the item's impl;
  the skeleton in item 1 exists only to make the item 2 test
  file compile against the referenced symbol names.
- Phase B: 5 (all `flag.Value` tests + impl).
- Phase C: 6 → 7 → 8. Note: 6 must precede 7 because
  `ScrubAmbientAblationEnv` depends on the `ablationEnvExports`
  map already existing.
- Phase D: 9.
- Phase E: 10 (types + CSV) → 11 (Run wiring + tests) → 12
  (main.go + smoke tests). The `main_smoke_test.go` at 12
  requires `go build` succeeding on the whole tree, so runs last.
- Phase F: 13. README lives close to code; a `TestReadme*` failure
  in phase G may reveal a spec-vs-README drift, fix in this phase.
- Phase G: 14 → 15 → 16 → 17. These are audit tests that live in
  `flags_test.go` (14, 15) or a new
  `spec_drift_test.go` (16, 17). They MUST pass on the initial
  commit; a subsequent PR that violates the spec fails them at
  CI time.

## 3. Test matrix by spec security item

| Spec item | Test(s) |
|---|---|
| §7(a.1) unknown flag typo → exit 2 | `TestAblationList_UnknownFlagRejected`; `TestMain_UnknownFlagExit2`; `TestApplyAblationFlagsTo_UnknownFlagRejectsBeforeMutating` (defence-in-depth in the `applyAblationFlagsTo` layer). |
| §7(a.2) ambient env auto-activation | `TestScrubAmbientAblationEnv_ClearsParent`; `TestScrubAmbientAblationEnv_Idempotent`; `TestMain_ScrubsParentEnvBeforeParse` (end-to-end); `TestRun_ScrubsAmbientLoomAblationEnv`. |
| §7(a.3) label forgery via Opts | `TestRun_LabelDerivedFromApplied_{NoAblation,SingleFlag,MultiSortedJoined}` — a caller who omits `AblationFlags` gets `full_loom` no matter what. Compile-time proof: `Opts` has no `BaselineOrAblation` field (asserted by `TestOptsHasNoBaselineOrAblationField` — a `go/ast` field-list audit test). |
| §7(a.4) stale state across `Run` calls | `TestApplyAblationFlagsTo_ResetPhaseClearsPriorFlags`; `TestApplyAblationFlagsTo_NilResetsAll`; `TestRun_DeferResetsFlagsOnHappyPath`; `TestRun_DeferResetsFlagsOnExit2AfterApply`; `TestApplyAblationFlags_ResetsStaleState` (spec §7(a.4) sequence test). |
| §7(b) sorted-stable dedup | `TestComputeBaselineOrAblation_OrderStable`; `TestComputeBaselineOrAblation_DedupeDirectCaller`; `TestAblationList_DedupWithinSingleSet`. |
| §7(c) length + regex + dual enforcement | `TestValidateBaselineOrAblation_*` (5 tests) cover the shared validator. `TestBaselineOrAblationCapCoversAll8Flags` covers the length invariant. The Run-side dual enforcement is covered by call-graph proof rather than a mock: `TestRun_CallsValidateBaselineOrAblation` uses `go/ast` to walk `runner.go`'s `Run` function body and asserts a `CallExpr` on `ValidateBaselineOrAblation` exists AFTER the `ComputeBaselineOrAblation` call. The composite-literal `RunRow{..., BaselineOrAblation: derivedLabel, ...}` further down the function is the row-assembly site; the AST test asserts the validator call precedes that composite literal (recognized by walking the file for `CompositeLit` whose `Type` is `RunRow` and checking `Elts` contains a `KeyValueExpr` with `Key.Name == "BaselineOrAblation"`). No build tag or link seam is needed — the spec §7(c) requirement is "the validator IS called at both sites"; the AST assertion catches a future refactor that removes the call. |
| §7(d) `--list-ablations` metadata only | `TestListAblations_LineFormat`; `TestListAblations_NoSensitiveContent`; `TestListAblations_DescriptionCoversAll8Flags`. |
| §7(e) narrow ablation API | `TestFlagsGo_NarrowAblationSurface`; `TestPythonBridgeKeysAreCanonical` (string-cast keys must be canonical). |
| §7(f) README metric mapping | `TestReadmeMetricMapping` (grep against 12 号 §A/§B/§D `metrics` column). |
| §7(g) consumer-view SQL/pandas | `TestReadmeContainsSQLAndPandasSnippets`. |
| §7(h) CI 8-flag count + bind | `TestListAblations_CountMatchesRegistry`; `TestApplyAblationFlags_AllKnownFlagsBind` (per-flag sub-test); `TestFlagsGoBlankImports`. |

Every spec security item has ≥ 2 tests; the mutation-invariant
items ((a.1), (a.4)) have 3 or more (parse layer + apply layer +
end-to-end).

## 4. Non-goals for this plan

- No changes to the Phase 1 `internal/ablation` package. If a
  refactor is needed there (e.g. exposing `Registry.IsSet(name)`),
  it belongs to a separate Phase 3 worktree.
- No changes to any owner package (`capability`, `contract`,
  `driver`, `evalrun`, `contract/validator`). Their `Register`
  call sites and `Sync…` accessors are consumed as-is.
- No changes to `internal/observerstore/schema.sql`. The new
  `baseline_or_ablation` column lives on the CSV `RunRow` only;
  the D1 SQLite schema already has the column
  (`internal/observerstore/schema.sql:199`).
- No changes to `tests/eval/baselines/`. The baselines emit their
  own `baseline_or_ablation` strings via a separate harness (see
  WT-2-baselines spec §5).
- No new sub-package under `tools/eval/runner/`. Everything lands
  in `package main`.

## 5. Rollback

If Phase E surfaces a preflight-order conflict with WT-2-e1e6-probes
or another WT-2 predecessor, the rollback is: revert commits from
Phase E (leaving A–D + G as pure additions plus the CSV column) and
land the wire-up in a follow-up PR after the conflict is resolved.
Phases A–D + G are backwards-compatible with the current runner
(they add unused symbols only) and can safely ship as an interim
commit.
