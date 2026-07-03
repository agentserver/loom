# WT-2-baselines — Plan

> Companion to `wt2-baselines.spec.md`. TDD order: tests first per file,
> then minimal impl, then refactor.

## Files (created in this order)

Order goes bottom-up inside `harness/` first, then per baseline; per
baseline is manual_ssh → single_machine → cloud_sandbox (increasing
complexity + external dependency).

### `harness/` — shared package (Go)

1. `harness/row.go` + `harness/row_test.go` — `BaselineRunRow` struct;
   `ValidateBaselineName` regex; nothing else. Pure functions, easiest
   brick to lock.
2. `harness/writer.go` + `harness/writer_test.go` — CSV writer, header +
   1 row, refuse-if-exists guard, stable column order literal test.
3. `harness/env.go` + `harness/env_test.go` — `WhitelistEnvForAgent` +
   `WhitelistEnvForOracle` (two separate calls per §7(a)); table-driven
   test proving oracle-env excludes per-baseline creds.
4. `harness/workspace.go` + `harness/workspace_test.go` — `SetupWorkspace`
   + `Cleanup`; symlink-escape refusal; perms=0700.
5. `harness/scan.go` + `harness/scan_test.go` — `ScanTreeForSecrets(root)
   ([]string, error)` walks the tempdir, calls `secretscrub.Sanitize`,
   returns rejected relative paths. Skip files > 8 MiB and NUL-in-first-512
   binary files per §7(b) invariants.
6. `harness/oracle.go` + `harness/oracle_test.go` — `RunOracle(ctx, spec,
   workspace, env, timeout) (oracleResult, error)`. Reuses subprocess
   discipline conceptually from `tools/eval/runner/subprocess.go` but is
   its own copy (spec §6 rationale). Enforces 1 MiB stdout cap and
   first-line JSON parse.
7. `harness/flags.go` + `harness/flags_test.go` — `Opts` struct +
   `NewFlagSet` factory + `ValidateOpts` validator. Test covers the
   post-parse required-flag rejection and the baseline-name regex hook
   (defence in depth on top of the ValidateBaselineName tests).
8. `harness/run.go` + `harness/run_test.go` — orchestrator; takes
   `BaselineImpl` + `Opts`; implements §4 pipeline steps 1–10; tested
   with a stub `BaselineImpl` that projects a pre-baked mock workspace.

### `manual_ssh/`

9. `manual_ssh/workloads.go` — the per-workload bash script table. Each
   entry: `map[workloadID]string`. Scripts are stdlib-only (`printf`,
   `sha256sum` etc.), reproduce the maintainer-facing `mock_workspace`.
10. `manual_ssh/impl.go` + `manual_ssh/impl_test.go` — `ManualSSHImpl`;
    tests: no-ssh-binary invocation (grep-guarded + runtime constant);
    dry-run degrades to mock_workspace projection.
11. `manual_ssh/main.go` — CLI shim (~15 lines).
12. `manual_ssh/run.sh` — bash entrypoint (`go run` in dev, prebuilt
    binary in CI).

### `single_machine/`

13. `single_machine/workloads.go` — per-workload prompt table for the
    Claude CLI: `map[workloadID]promptSpec{Prompt, ExpectedOutputs}`.
14. `single_machine/impl.go` + `single_machine/impl_test.go` —
    `SingleMachineClaudeCodeImpl`; tests: dry-run never invokes `claude`;
    real-mode failure when `claude` missing → `ErrClaudeCLIUnavailable`;
    per-baseline env allowlist includes `ANTHROPIC_API_KEY` for agent
    env only.
15. `single_machine/main.go` — CLI shim.
16. `single_machine/run.sh`.

### `cloud_sandbox/`

17. `cloud_sandbox/e2b.go` + `cloud_sandbox/e2b_test.go` — thin E2B
    client interface + fake for tests; `Do(req)` panics on `--dry-run`
    per §7(f).
18. `cloud_sandbox/workloads.go` — per-workload `remoteExecPlan`
    (upload list + exec cmd + fetch list).
19. `cloud_sandbox/impl.go` + `cloud_sandbox/impl_test.go` —
    `CloudSandboxE2BImpl`; tests: pre-upload secret scan reject,
    dry-run no-dial, real-mode API-call counter accuracy.
20. `cloud_sandbox/main.go` — CLI shim + `--e2b-api-key-env` extra flag.
21. `cloud_sandbox/run.sh` — includes the `CI=true → require --dry-run`
    guard per §7(h).

### Cross-cutting

22. `tests/eval/baselines/README.md` — overview + "how to add a baseline".
23. `tests/eval/baselines/matrix_test.go` — Go-level 15-run matrix test
    (`go test ./tests/eval/baselines/... -run TestMatrix -tags matrix`).
    Kept off the default test tag because it builds all three binaries.

## Test matrix

Row shape: `#` | `Test` | `File` | `Verifies` | `Security §`.

| # | Test | File | Verifies | §7 |
|---|---|---|---|---|
| 1 | `TestValidateBaselineName_ValidValues` | harness/row_test.go | `manual_ssh`, `single_machine_claude_code`, `cloud_sandbox_e2b`, `abc`, `x-y-z_1` all pass | (d) |
| 2 | `TestValidateBaselineName_RejectsSQLInjection` | harness/row_test.go | `"foo'; DROP TABLE runs; --"` → error `ErrBaselineNameInvalid` | (d) |
| 3 | `TestValidateBaselineName_RejectsLeadingDigit` | harness/row_test.go | `1abc` rejected; regex leading `[a-z]` rule | (d) |
| 4 | `TestValidateBaselineName_RejectsTooShort` / `_RejectsTooLong` | harness/row_test.go | length bounds `[3,64]` | (d) |
| 5 | `TestWriter_HeaderAndSingleRow` | harness/writer_test.go | write one row → `wc -l` == 2; column order matches spec §5 literal | interface |
| 6 | `TestWriter_RefuseIfExists` | harness/writer_test.go | second write to same `--out` errors, does not append | interface |
| 7 | `TestWhitelistEnvForAgent_OptInForwardsAnthropic` | harness/env_test.go | opt-in=true + parent `ANTHROPIC_API_KEY=sk-x`, agent env HAS the key | (a) |
| 7b | `TestWhitelistEnvForAgent_DefaultDropsAnthropicKey` | harness/env_test.go | opt-in=false (default) + parent `ANTHROPIC_API_KEY=sk-x`, agent env DOES NOT have the key (no baseline-name implicit forward) | (a) |
| 8 | `TestWhitelistEnvForOracle_MatchesRunnerContract` | harness/env_test.go | oracle env byte-for-byte matches WT-1-eval-runner-skeleton `WhitelistEnv`; opt-in-true still keeps oracle env clean | (a) |
| 9 | `TestWhitelistEnvForOracle_DropsAgentCreds_WhenOptedIn` | harness/env_test.go | opt-in-true + parent `ANTHROPIC_API_KEY=sk-x`: oracle env still LACKS the key | (a) |
| 10 | `TestWhitelistEnvForAgent_DropsGenericSecrets` | harness/env_test.go | `OPENAI_API_KEY`, `AWS_ACCESS_KEY_ID` never propagate (opt-in unrelated) | (a) |
| 11 | `TestSetupWorkspace_Mode0700` | harness/workspace_test.go | tempdir stat mode == 0700 | (e) via §7(g) |
| 12 | `TestSetupWorkspace_RejectsSymlinkEscape` | harness/workspace_test.go | symlink to `/etc/passwd` in fixtures → error | (e) |
| 13 | `TestSetupWorkspace_CleanupRemovesTempdir` | harness/workspace_test.go | after Cleanup, path missing | (e) |
| 13b | `TestSetupWorkspace_DoesNotMutateSourceFixtures` | harness/workspace_test.go | recursively sha256 the source fixtures dir before + after a full Run() (with a stub impl that writes new files into the workspace); assert the sha256 tree map is unchanged | (e) |
| 14 | `TestScanTreeForSecrets_DetectsSK` | harness/scan_test.go | fixture with `sk-testonly-secret-1234567890AB` → returned path list non-empty | (b) |
| 15 | `TestScanTreeForSecrets_DetectsJWTAndAWS` | harness/scan_test.go | fixtures with `eyJ...` JWT + `AKIA...` AWS key both flagged | (b) |
| 16 | `TestScanTreeForSecrets_SkipsBinary` | harness/scan_test.go | fixture with NUL in first 512 bytes → skipped, not flagged | (b) |
| 17 | `TestScanTreeForSecrets_SkipsLargeFile` | harness/scan_test.go | 9 MiB fixture → skipped, not flagged | (b) |
| 18 | `TestRunOracle_HappyPath_CrossDeviceCodeMod` | harness/oracle_test.go | run against real `cross-device-code-mod` mock_workspace → `passed=true`, exit 0 | interface |
| 19 | `TestRunOracle_RejectsOversizedStdout` | harness/oracle_test.go | custom oracle prints 2 MiB → error `ErrOracleOutputTooLarge`, subprocess killed | (f-equiv from runner) |
| 20 | `TestRunOracle_MalformedFirstLine` | harness/oracle_test.go | custom oracle prints `not json\n` → `passed=false`, `oracle_exit_code=1`, error `ErrOracleStdoutNotJSON` | interface |
| 20b | `TestRunOracle_RejectsExtraStdoutLine` | harness/oracle_test.go | oracle prints valid JSON line then any second line (even blank) → error `ErrOracleStdoutHasExtraLines`; run marked failed. 13 号 §1.3 exactly-one-line rule | interface |
| 21 | `TestRun_EndToEnd_StubImpl` | harness/run_test.go | fake `BaselineImpl` that projects mock_workspace → 2-line CSV, `passed=true`, columns 12–14 populated | acceptance |
| 22 | `TestRun_ExitCode1_OnOracleFail` | harness/run_test.go | fake impl produces empty workspace → oracle fails → exit 1, CSV still 2 lines | interface |
| 23 | `TestManualSSH_DoesNotShellSSH` | manual_ssh/impl_test.go | `grep -rn '"ssh"\|"scp"\|"rsync"\|"sftp"'` over `manual_ssh/*.go` returns nothing; runtime constant `bashPath == "/bin/bash"` | (c) |
| 24 | `TestManualSSH_HappyPath_CrossDeviceCodeMod` | manual_ssh/impl_test.go | real mode: script writes `patch.diff` + `test.log`, oracle passes | interface |
| 25 | `TestManualSSH_DryRun_DegradesToMockWorkspace` | manual_ssh/impl_test.go | dry-run: no bash script run; mock_workspace projected; oracle passes | (f) |
| 26 | `TestSingleMachine_DryRun_DoesNotInvokeClaude` | single_machine/impl_test.go | dry-run mode with `PATH` containing a fake `claude` that fails-on-invocation; impl still returns success | (f) |
| 27 | `TestSingleMachine_RealMode_MissingClaude_FailsFast` | single_machine/impl_test.go | real mode with `PATH=/nonexistent` → `ErrClaudeCLIUnavailable`, exit 2 | interface |
| 27b | `TestSingleMachine_ForwardFlag_ControlsKeyPropagation` | single_machine/impl_test.go | dry-run with `--forward-anthropic-api-key=false` + parent key set: subprocess env LACKS the key. Flag=true: key propagates | (a) |
| 27c | `TestCloudSandbox_ForwardFlag_ControlsKeyPropagation` | cloud_sandbox/impl_test.go | with `--forward-e2b-api-key=false` + `E2B_API_KEY` set: harness's in-process E2B client is nil / key blank. Flag=true: client holds the key | (a) |
| 28 | `TestCloudSandbox_PreUploadSecret_Rejected` | cloud_sandbox/impl_test.go | fixture with `sk-testonly-secret-1234567890AB` under workspace → `ErrPreUploadSecretDetected`, exit 2, ZERO API calls issued | (b) |
| 29 | `TestCloudSandbox_DryRun_NoDial` | cloud_sandbox/e2b_test.go | fake client that fails-on-`net.Dial`; `--dry-run` invocation never dials | (f) |
| 30 | `TestCloudSandbox_CIRunRequiresDryRun` | cloud_sandbox/impl_test.go | env `CI=true` without `--dry-run` → run.sh exits 2 with helpful message (test invokes run.sh via `bash -c`) | (h) |
| 31 | `TestCloudSandbox_RealMode_CountsAPICalls` | cloud_sandbox/impl_test.go | fake client records call count; real (non-dry-run) executes N calls; `metrics_baseline_api_calls == N` in CSV | (g) |
| 32 | `TestBaseline_MetricsFieldsPresent` | harness/run_test.go | happy path CSV row has non-empty columns 12–14 (wall_time_ms numeric ≥ 0, api_calls parseable int, upload_bytes int) | (g) |

## 15-run smoke matrix

Two levels:

* **Level A: Go-level** — `TestMatrix15Runs` in `matrix_test.go` (build tag
  `matrix`). Compiles all three binaries with `go build`, then for each
  `(workload, baseline)` pair runs `./bin/<baseline> run --workload <w>
  --dry-run --out …` in a subtest. Assertions per subtest:
  * exit code ∈ {0, 1} (2/3 = FAIL)
  * CSV file exists and `wc -l == 2`
  * data-row `baseline_or_ablation` column matches the baseline's default
  * data-row `dry_run` column == `true`
  * data-row `metrics_baseline_wall_time_ms` parses as int ≥ 0

* **Level B: Shell** — `docs/specs/wt2-baselines.spec.md` §8 snippet runs
  the same matrix through `run.sh` wrappers. Included in the Phase-3
  acceptance recipe; not a Go test.

Both are dry-run only. Real cloud execution is a Phase-3 concern (see
spec §7(h)).

## How each security item is locked

| §7 | Test row(s) |
|---|---|
| (a) env whitelist | 7, 7b, 8, 9, 10, 27b, 27c |
| (b) cloud upload secret scan | 14, 15, 16, 17, 28 |
| (c) manual_ssh no real ssh | 23 |
| (d) baseline_or_ablation regex | 1, 2, 3, 4 |
| (e) fixture copy tempdir | 11, 12, 13, 13b |
| (f) --dry-run honoured | 25, 26, 29 |
| (g) cost metrics recorded | 31, 32 |
| (h) CI must skip real cloud | 30 |

## Non-test verification

* `go build ./tests/eval/baselines/...` from `multi-agent/` → all three
  binaries build with zero warnings.
* `go vet ./tests/eval/baselines/...` clean.
* `gofmt -l tests/eval/baselines` prints nothing.
* Spec §8 15-run smoke passes: 15 CSVs each 2 lines under `/tmp/wt2-smoke/`.

## Risks

1. **`claude` CLI is not on the eval host.** If real-mode
   `single_machine` needs a claude binary the CI runner does not have,
   test #26 (dry-run branch) is the only test that runs by default; test
   #27 covers the missing-binary failure. Real invocation is a
   Phase-3 exercise.
2. **E2B API surface drift.** The client in `cloud_sandbox/e2b.go` is a
   thin JSON POST/PUT/GET wrapper — the tests use a fake, so drift in
   E2B's real schema surfaces only in Phase 3. Documented as the
   `WT-3-stub-fulltable` risk to pick up.
3. **secretscrub false negatives.** `secretscrub.Sanitize` is
   defense-in-depth, not exhaustive (its own doc says so). A workload
   that ships a novel token shape may still be uploaded. Mitigation: the
   scan pattern list is a one-line addition to `internal/secretscrub`
   whenever a new shape is discovered; both baseline scan and the
   dispatch redaction benefit simultaneously. Not a bug we ship — a
   documented limit.
4. **Near-duplication of harness/workspace + fixtures with
   `tools/eval/runner`.** Called out in spec §6. Refactor into a shared
   `internal/` package is out of scope this worktree (would cross the
   file-domain constraint into `tools/eval/runner/`).

## Commit strategy

Ordered commits, each one green on `go test ./tests/eval/baselines/... -race`:

1. `wt2-baselines: harness/{row,writer,env,workspace,scan,oracle,flags,run}.go + tests` — shared package with impl-agnostic stub.
2. `wt2-baselines: manual_ssh baseline` — impl + tests + run.sh.
3. `wt2-baselines: single_machine_claude_code baseline` — impl + tests + run.sh.
4. `wt2-baselines: cloud_sandbox_e2b baseline` — impl + e2b client + tests + run.sh.
5. `wt2-baselines: matrix_test.go + README.md` — 15-run smoke test + docs.

Each commit ends with the required `Co-Authored-By: Claude Opus 4.8 (1M
context) <noreply@anthropic.com>` trailer.
