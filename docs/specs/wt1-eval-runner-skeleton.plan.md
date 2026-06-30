# WT-1-eval-runner-skeleton — Plan

> Companion to `wt1-eval-runner-skeleton.spec.md`. TDD order:
> tests first per file, then minimal impl, then refactor.

## Files (created in this order)

1. `multi-agent/tools/eval/runner/redact.go` + `redact_test.go` — pure
   function, the easiest brick to lock down first.
2. `multi-agent/tools/eval/runner/writer.go` + `writer_test.go` — RunWriter
   interface + NoopWriter + CSVWriter; CSV row stability tested against a
   golden bytes literal.
3. `multi-agent/tools/eval/runner/subprocess.go` + `subprocess_test.go` —
   env whitelist + process group + timeout-kill primitives.
4. `multi-agent/tools/eval/runner/fixtures.go` + `fixtures_test.go` — copy
   to tempdir, perms, symlink containment, cleanup.
5. `multi-agent/tools/eval/runner/runner.go` — `Run(ctx, Opts) (Result, error)`
   orchestrator; assembles the above. Validators (`validateStubListen`,
   `validateObserverDB`) live here.
6. `multi-agent/tools/eval/runner/runner_test.go` — end-to-end harness on
   `cross-device-code-mod`.
7. `multi-agent/tools/eval/runner/main.go` — CLI shim. Tiny — flag parsing
   then `Run(ctx, opts)`; no test (covered by integration test that builds
   the binary and runs it).

## Test matrix (mirrors prompt; 12 rows)

Each row: test name → what it verifies → which §7 security item it locks.

| # | Test | Verifies | §7 |
|---|---|---|---|
| 1 | `TestRun_CrossDeviceCodeMod_HappyPath_CSVOneLine` | runner builds, executes 1 workload end-to-end against `cross-device-code-mod` (oracle pass=true on mock_workspace), CSV has exactly 2 lines (header + data), data line `passed` column = `true` | acceptance |
| 2 | `TestSubprocessEnv_WhitelistedOnly` | parent has `OPENAI_API_KEY=sk-real-12345` + `ANTHROPIC_API_KEY=...` + `AWS_ACCESS_KEY_ID=...`; oracle prints `env`; runner-collected stdout contains no secret keys; contains `PATH` and `AGENTSERVER_URL` | (a) |
| 3 | `TestFixturesCopiedToTempdir_NotInPlace` | snapshot fixtures dir hash before run; run; snapshot hash after; ASSERT equal; ASSERT tempdir contained copy of fixture files mid-run (via stdout marker from a custom oracle that lists pwd contents) | (b) |
| 4 | `TestTempdir_CleanedOnExit` | run completes; ASSERT tempdir path no longer exists. Separate sub-test for `--keep-tempdir` confirms the path DOES persist (and runner stderr prints the path) | (b) |
| 5 | `TestCommitMetaRedacted_Email` | commit_meta JSON synthesized with `author_email=user@example.com`, `committer_email=other@example.com`; CSV columns `author_email_sha8` and `committer_email_sha8` = expected `sha256(lower(addr))[:8]`; ASSERT no `@` in either column | (c) |
| 6 | `TestStubListen_RejectsNonLoopback_0000` | `--stub-listen 0.0.0.0:18080` → exit 2 + stderr contains `ErrStubMustBeLoopback` | (d) |
| 7 | `TestStubListen_RejectsNonLoopback_External` | `--stub-listen 10.0.0.5:18080` → exit 2 + `ErrStubMustBeLoopback` | (d) |
| 8 | `TestObserverDB_RejectsEtc` | `--observer-db /etc/passwd` → exit 2 + stderr contains `ErrObserverDBPathForbidden`; also covers `/proc/self/environ`, `/sys/kernel/...` in sub-tests | (e) |
| 9 | `TestOracleOutputTooLarge_Rejected` | custom oracle that prints 2 MiB of `x` then a JSON line → exit 2 + stderr `ErrOracleOutputTooLarge`; CSV NOT written | (f) |
| 10 | `TestTempdir_Perm0700` | run; before cleanup, stat the tempdir from a hook (use `--keep-tempdir`) and ASSERT mode bits == 0700 | (g) |
| 11 | `TestSubprocessGroup_KilledOnTimeout` | oracle is `sleep 90` shell script; `--timeout 2s`; ASSERT runner exits within 5s; ASSERT no `sleep` PID survives by sweeping `/proc` for the child PID after `wait` | (a) lifecycle |
| 12 | `TestRunWriter_NoopByDefault` | run without `--observer-db`; ASSERT CSV still written with full row; capture stderr — should not contain `run-schema integration pending` warning | interface |

### Additional always-on assertions (cross-cutting; not separate rows)

* `TestStubListen_AcceptsLocalhost_LoopbackOnly` — `--stub-listen
  localhost:18091` accepted when `localhost` resolves to `127.0.0.1`/`::1`
  only (smoke; skipped if `/etc/hosts` is non-standard). Not a security
  test in its own right but pinning the loopback-allow-list behaviour.
* `TestCSVRow_AppendForbidden` — invoking runner twice with the same
  `--out` second time exits with a clear error rather than mangling the
  CSV. Defends the "no accidental append" rule from §3.
* `TestEmailRedact_StablePerCall` — same email → same 8-hex across calls;
  different emails → different hex. Locks the determinism property
  reviewers rely on.

## How each test exercises the runner

* Tests #2, #3, #9, #11 use **swappable oracle scripts** generated into the
  test's t.TempDir(), then spec.yaml is rewritten to point at the test's
  oracle. The test does *not* edit `multi-agent/tests/eval/workloads/`
  in-tree — security item (b) applies to test infrastructure too.

* Test #1 (happy path) and test #5 (commit_meta) and test #12 (NoopWriter)
  use the **real** `cross-device-code-mod` workload with the maintainer
  `mock_workspace` artifact set — the same one §1.4 demonstrates with
  `./oracle.sh ./fixtures/mock_workspace`.

* Test #4 (cleanup) and test #10 (perms) need a hook to inspect tempdir
  before cleanup. Two approaches considered:
  1. Expose runner cleanup as deferrable hook in `Opts.OnTempdir func(string)`
     — clean, no debug flag pollution.
  2. Use `--keep-tempdir` and parse the path from stderr.

  Picked (1) for tests #4 sub-test "cleaned" and #10; `--keep-tempdir` still
  exists as an operator debugging flag, exercised by the #4 sub-test "kept".

* Test #11 — to verify no zombie, we record the child's PID inside
  `subprocess.go` (returned from `runWithTimeout`) and `os.FindProcess(pid)`
  then `proc.Signal(syscall.Signal(0))` post-wait — non-nil error
  (`os: process already finished`) is the proof. Plus check no process
  exists with our recorded process-group ID still alive
  (`syscall.Kill(-pgid, 0)` returning ESRCH).

## How commit_meta is invoked from a Go test

`commit_meta` is Python; runner shells out to it. In tests, we stub by:

* env override `LOOM_EVAL_COMMIT_META_CMD` — when set, runner runs that
  command instead of `python -m commit_meta.collect`. The test sets it to
  a small shell script that emits a controlled JSON. This is the standard
  "subprocess seam" pattern and is itself on the env whitelist (key
  matches `LOOM_*`).

* For author/committer email: similarly `LOOM_EVAL_GIT_EMAIL_CMD`
  override exists; default is `git log -1 --format='%ae|%ce' HEAD` run in
  the loom repo root.

Tests #1 and #5 set both env-overrides so they're hermetic w.r.t. the
host's actual git state.

## Tooling

```bash
cd multi-agent
go test ./tools/eval/runner/... -count=1 -shuffle=on -race
go vet ./...
gofmt -l tools/eval/runner
go build -o /tmp/eval-runner ./tools/eval/runner

# End-to-end smoke
/tmp/eval-runner run \
    --workload cross-device-code-mod \
    --workload-dir tests/eval/workloads \
    --stub-listen 127.0.0.1:18080 \
    --out /tmp/run.csv
[ "$(wc -l < /tmp/run.csv)" = "2" ] || { echo "smoke fail"; exit 1; }
```

## Risks & known gaps (declared so review catches them)

* **Stub port collision.** Default `127.0.0.1:18080` collides with anything
  else holding the port. Runner doesn't retry; operator picks the port.
  Acceptable for skeleton; future enhancement: auto-pick free loopback
  port if `--stub-listen` flag omitted.
* **commit_meta as subprocess.** Adds Python runtime dependency to the
  runner. Mitigation: env override gives clean test seam; runtime failure
  is non-fatal — runner records `loom_commit = "N/A: commit_meta unavailable"`
  and continues. (Same shape as commit_meta's own missing-repo strings.)
* **Real driver/observer/slave not wired.** Documented as out of scope in
  spec §1; future worktree replaces `agentStage` with the real
  fanout. Skeleton's "copy mock_workspace" is a placeholder; tests #1, #5,
  #12 still constitute a real end-to-end of the oracle/commit/CSV pipeline.
* **Windows.** `Setpgid` is Linux/Darwin only; on Windows the runner falls
  back to `Process.Kill()` of the leaf only. Tests #11 are tagged
  `//go:build !windows` since the assertion is process-group specific.
