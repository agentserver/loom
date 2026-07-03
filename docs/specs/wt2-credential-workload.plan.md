# WT-2-credential-workload — Plan

> Companion to `docs/specs/wt2-credential-workload.spec.md`. Numbering here
> follows the sections there — e.g. §7.a below implements the safety item
> §7(a) of the spec.

---

## 1. Landing sequence

1. Land the spec (`docs/specs/wt2-credential-workload.spec.md`) — no code.
2. Add `codexconfig.go` + `codexconfig_test.go` with the full table
   matrix from §5 below. Land tests-first inside the same commit; the
   validation function has no external dependency, so unit tests fully
   exercise it.
3. Wire `--codex-config-path` and `--codex-config-mode` into `main.go`
   (only these two — the harness owns path resolution per spec §2.2).
   Add the call to `validateCodexConfig` into `runner.go` right after
   the existing `validateStubListen` / `validateObserverDB` pre-flight
   block. Extend `Opts` with the two new string fields — no new bool.
4. Add `runner_test.go` case that spins the runner via `Run()` with the
   new fields set and asserts pre-flight rejects a mismatched pair.
   Reuse the existing `commitMetaShim` / `gitEmailShim` helpers so no
   real codex or git is invoked.
5. Add `eval/eval-modelproxy-overhead.sh`, `eval/hops_loopback_check.sh`,
   and `eval/testdata/*.json` fixtures. Make the shell scripts pass
   `shellcheck` locally (documented in the header — no CI change here
   since the runner subtree currently has no shellcheck job).
6. Run the verification commands from §7. Commit as a single
   Conventional-Commit `feat(eval): WT-2 credential dual-path runner
   flags + overhead harness` with the Co-Authored-By trailer.

Nothing is pushed. The prompt is explicit.

---

## 2. Files created / edited

```
docs/specs/wt2-credential-workload.spec.md                                 NEW
docs/specs/wt2-credential-workload.plan.md                                 NEW  (this file)

multi-agent/tools/eval/runner/main.go                                      EDIT (~10 lines added)
multi-agent/tools/eval/runner/runner.go                                    EDIT (~8 lines added)
multi-agent/tools/eval/runner/codexconfig.go                               NEW
multi-agent/tools/eval/runner/codexconfig_test.go                          NEW
multi-agent/tools/eval/runner/runner_test.go                               EDIT (+1 test)

multi-agent/tests/eval/workloads/credential-bound-model/eval/README.md                          NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/eval-modelproxy-overhead.sh        NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/hops_loopback_check.sh             NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/hop_loopback_ok.json      NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/hop_no_proxy.json         NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/hop_fake_loopback.json    NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/hop_empty.json            NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/hop_leaks_loopback.json   NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/config_a.toml             NEW
multi-agent/tests/eval/workloads/credential-bound-model/eval/testdata/config_b.toml             NEW
```

Nothing else is touched — see spec §1's "Hard rules" for the file-scope
guardrails.

---

## 3. Data shapes

### 3.1 `codexConfigInspection` (Go)

```go
type codexConfigInspection struct {
    HasBearer bool
    HasEnvKey bool
}
```

Returned by `inspectCodexConfig(path string)`. Populated by
line-scanning the file — no TOML parser dependency. Comment handling:
lines are `strings.TrimSpace`-d and any suffix from the first `#`
onwards is dropped BEFORE the regex match (so `foo = "bar"  # comment`
still matches). Multi-line strings are not handled — the two fields
we care about are single-line by convention; a multi-line value that
starts with `experimental_bearer_token = """...` is flagged as
`HasBearer` on the OPENING line, matching operator expectation.

### 3.2 `hop` (shell)

Read as JSON but never with jq. The mode=a check is:

```
grep -oE '"proxy_addr"[[:space:]]*:[[:space:]]*"[^"]+"' route.json \
  | sed -E 's/.*"proxy_addr"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' \
  | while read -r addr; do
      host=$(strip_port "$addr")
      case "$host" in
        127.0.0.1|::1|localhost) echo LOOPBACK ;;
        *) echo OTHER ;;
      esac
    done | grep -qx LOOPBACK
```

`strip_port` handles both `host:port` (returns `host`) and bare `host`
(returns `host`). For IPv6 in brackets (`[::1]:8080`) it also strips
the brackets — the fixture uses the bracketed form because that is
what real IPv6 URLs use. Same-string comparison after stripping.

### 3.3 CSV row shape

Unchanged from PR #53. The `codex_config_path` column carries the
value the caller passed to `--codex-config-path` (or the pre-existing
`--codex-config`, which stays as the fallback). Per spec §2.2 the
runner NEVER resolves a mode into a path on its own — if the caller
passes only `--codex-config-mode` and no path, the column stays empty
(matching what PR #53 already does for callers that skip both flags).
Path resolution is exclusively the harness's job.

### 3.4 Harness-side per-run assertions (spec §7(e) guards 2 + 3)

After each runner invocation the harness reads the just-written CSV
file, extracts the `codex_config_path` column via `awk -F, 'NR==2 {print $20}'`
(header line first, then the sole data row — column 20 per
`multi-agent/tools/eval/runner/writer.go:80`), and asserts:

```
expected=$( [[ "$mode" == "a" ]] && echo "$CONFIG_A_PATH" || echo "$CONFIG_B_PATH" )
[[ "$got" == "$expected" ]] || die "csv codex_config_path drift: got=$got want=$expected mode=$mode"
```

Any drift aborts the WHOLE sample set (not just the current
iteration) — a mixed set would poison `ModelProxyOverhead`.

The 30-second inter-run delay is implemented as the LITERAL shell
statement `sleep 30`, hard-coded, not parameterised by a flag or
env var. A `grep -q "^\s*sleep 30\s*$" eval-modelproxy-overhead.sh`
is added to the harness's own `--self-test` mode so a drive-by edit
that removes / weakens the sleep is caught in CI (`--self-test` is
run alongside `--dry-run` in the acceptance checklist §7).

---

## 4. Interfaces to other worktrees

- **PR #53 (WT-1-eval-runner-skeleton)**: consumed as-is. We add flags
  and one pre-flight step; we do NOT rearrange the pipeline or the
  `Opts` fields we don't own.
- **WT-1-run-schema** (`multi-agent/internal/evalrun/`): consumed as-is
  — this worktree does not persist rows, it only extends the documented
  enum values for `baseline_or_ablation`.
- **WT-1-capability-snapshot** (already merged into base): unrelated.
- **Phase 0 credential-bound-model workload**: consumed as-is —
  spec.yaml, oracle.sh, README.md, fixtures/ all untouched.

---

## 5. Test matrix (`codexconfig_test.go`)

Eight table rows. Each row is a Go struct literal in the
`TestValidateCodexConfig` table; the top-level test iterates and calls
`validateCodexConfig(opts)` with the fields set.

| # | Name                        | mode | path setup                                | env `OPENAI_API_KEY` | expected `errors.Is` sentinel |
|---|-----------------------------|------|-------------------------------------------|----------------------|-------------------------------|
| 1 | `mode_a_bearer_ok`          | `a`  | tempfile with `experimental_bearer_token = "sk-real123456"` | unset | nil |
| 2 | `mode_a_envkey_reject`      | `a`  | tempfile with `env_key = "OPENAI_API_KEY"`                  | (irrelevant, mode a doesn't need it) | `ErrCodexConfigMismatch` |
| 3 | `mode_b_envkey_ok`          | `b`  | tempfile with `env_key = "OPENAI_API_KEY"`                  | `sk-real123456`      | nil |
| 4 | `mode_b_bearer_reject`      | `b`  | tempfile with `experimental_bearer_token = "..."`           | `sk-real123456`      | `ErrCodexConfigMismatch` |
| 5 | `mode_b_env_sentinel`       | `b`  | tempfile with `env_key = "OPENAI_API_KEY"`                  | `test-key`           | `ErrOpenAIKeyMissing` |
| 6 | `mode_b_env_empty`          | `b`  | tempfile with `env_key = "OPENAI_API_KEY"`                  | unset                | `ErrOpenAIKeyMissing` |
| 7 | `path_traversal_reject`     | `a`  | absolute path `/etc/shadow`                                  | unset                | `ErrCodexConfigPathForbidden` |
| 8 | `mode_invalid`              | `c`  | tempfile with either field                                   | unset                | `ErrCodexConfigModeInvalid` |

Additional row (not in the table above because it is a separate test):

- `TestValidateCodexConfig_NoFlagsIsPassThrough`: neither
  `--codex-config-path` nor `--codex-config-mode` supplied → no
  validation runs, no error. Guarantees PR #53 callers are not broken.

- `TestValidateCodexConfig_AmbiguousBothFields`: config lists BOTH
  `experimental_bearer_token` AND `env_key` → rejected under BOTH
  modes as `ErrCodexConfigMismatch` (per spec §7.a table row).

- `TestValidateCodexConfig_ConfigContentsNeverLogged`: run the
  validator with a bearer-shaped token in the config; capture the
  error's `.Error()` string; assert the token substring
  `sk-real123456` does NOT appear in the message. Guards §7.c.

- `TestValidateCodexConfig_InlineCommentsIgnored`: a `#` comment on a
  line that would otherwise match an auth field key does not count.
  Guards the comment-strip step of `inspectCodexConfig`.

- `TestValidateCodexConfig_SentinelPlaceholderKey`: parameterised over
  `{sk-XXXXXX, sk-XXXXXXXXXX, changeme, x}` → all reject with
  `ErrOpenAIKeyMissing`. Guards spec §7.d beyond the two-row default
  matrix.

- `TestValidateCodexConfig_ModeAOnlyDoesNotAssertEnv`: mode=a with
  an empty `OPENAI_API_KEY` env value → nil. Guards against a
  regression where the env precondition accidentally fires for the
  local-proxy path.

Repo-root discovery for the path-traversal check reuses
`findRepoModuleRoot`-style logic; unit tests use `filepath.Join(t.TempDir(), "cfg.toml")`
which lives under `/tmp/` (on Linux CI runners) — that satisfies the
`/tmp/` allowlist arm of §7.b, so the tests don't need repo-relative
paths to succeed.

### 5.1 Runner-level integration test (in `runner_test.go`)

One added case `TestRunPreflightRejectsConfigMismatch`:

- Set up `Opts{WorkloadID: "credential-bound-model", ...}` with
  `CodexConfigMode = "a"` and a tempfile config that has
  `env_key = "OPENAI_API_KEY"`.
- Call `Run(ctx, opts)`.
- Expect `Result.ExitCode == 2` and no CSV written (the preflight helper
  in `runner.go` returns before `WriteCSVRow`).

This confirms the wiring from `Opts` through `runner.go` into the
validator; the unit table above already covers the validator's own
semantics.

### 5.2 Shell test surface

`hops_loopback_check.sh` has a `--self-test` mode that runs against
every `testdata/hop_*.json` fixture (five files, enumerated in the
table below) and prints `OK` / `FAIL` per case;
the eval `README.md` documents `bash hops_loopback_check.sh --self-test`
as the smoke command. This is invoked manually — no CI job asserts on
it — but it satisfies §7.f's "engineered for the substring attack"
requirement by making the counter-example fixture explicit and
easy to run.

Fixture roster (all under `eval/testdata/`, all rooted in spec §7.f):

| Fixture                    | Mode | Expected verdict | Purpose                                                       |
|----------------------------|------|------------------|---------------------------------------------------------------|
| `hop_loopback_ok.json`     | a    | pass             | at least one hop.proxy_addr = 127.0.0.1:53452                  |
| `hop_no_proxy.json`        | b    | pass             | no hop carries a loopback proxy_addr                          |
| `hop_fake_loopback.json`   | a    | FAIL             | proxy_addr = 127.0.0.1.evil.com — the substring attack (§7.f) |
| `hop_empty.json`           | a    | FAIL             | `hops: []` — mode=a requires positive evidence                |
| `hop_leaks_loopback.json`  | b    | FAIL             | a mode=b run whose route.json records a loopback hop (proxy leak) — must reject |

`eval-modelproxy-overhead.sh --dry-run` is invoked from the acceptance
checklist (§7) and prints:

```
eval-modelproxy-overhead: dry-run
  mode=a  resolved_config=/repo/.../driver-codex-local/.codex/config.toml
  mode=b  resolved_config=/repo/.../driver-codex-local/codex-config.toml
  runner: /path/to/eval-runner run --workload credential-bound-model \
    --codex-config-mode a --codex-config-path /repo/.../.codex/config.toml \
    --keep-tempdir \
    --out /tmp/wt2-modelproxy-a-1.csv --stub-listen 127.0.0.1:18080
  (…)
  sleep 30 between adjacent runs (3 samples × 2 modes = 6 runs, 5 sleeps)
```

The `--keep-tempdir` flag is mandatory per spec §2.3: the harness
greps the workspace path out of the runner's stderr line
`eval-runner: --keep-tempdir set; workspace at <path>` (fixtures.go:85),
then invokes `hops_loopback_check.sh <workspace>/route.json <mode>`.
Without `--keep-tempdir`, the runner's default cleanup would delete
`route.json` before the check can read it. The harness itself removes
the workspace after the check completes (both pass and fail paths).

In `--dry-run` the harness does NOT invoke the runner binary, but it
DOES validate every input the runner would consume (spec §2.3):

- the `--mode` flag is `a`, `b`, or `both` (default);
- the sample count `N` is ≥ 3;
- the workload's prompt fixture
  (`multi-agent/tests/eval/workloads/credential-bound-model/fixtures/prompt.txt`)
  exists AND is a non-empty regular file (spec §7(e) guard 1 —
  same-prompt invariant depends on this file being present and
  readable; a missing prompt file aborts the sample before any
  runner is invoked);
- the two resolved config paths (mode=a and mode=b) exist AND are
  regular files;
- the runner binary is executable (via `[[ -x "$runner_path" ]]`);
- the oracle script is executable (same test);
- for mode=b, `OPENAI_API_KEY` is set and not a sentinel value
  (spec §7.d — the shell mirrors the Go sentinel list).

Any failed validation prints a specific diagnostic and exits 2 (the
same code the runner uses for pre-flight failures). `--dry-run` is
what CI exercises; CI environments that do not have the operator's
`prod_test/driver-codex-local/` populated must either populate it
(with a stub two-file layout containing dummy `experimental_bearer_token`
= "sk-fake-for-dry-run-only" and dummy `env_key = "OPENAI_API_KEY"` —
never a real token) OR skip the harness. There is deliberately no
`--dry-run-tolerate-missing-configs` escape hatch: silent CI success
with missing configs is the exact class of drift we are engineering
against.

CI never asserts on `ModelProxyOverhead` values themselves (spec §7.h)
— the harness has no `--assert-max-delta-ms` flag, and none is
planned. The measurement is inherently network- and upstream-load-
dependent; making CI red on a bad-network minute would be measuring
the internet, not the paper's claim. Real-value assertion is a
manual operator step documented in the harness's `--help` text.

---

## 6. Rollback plan

Every changed file is either new (delete the file) or additive in a
well-fenced block (revert the block).

- `main.go`: three added `flag.String` calls + three added fields in
  the `Opts` literal. Revert the diff.
- `runner.go`: one added pre-flight call. Revert the diff.
- `runner_test.go`: one added test function. Delete it.

`git revert` on the single feature commit is the operator instruction.

---

## 7. Verification (matches spec §4)

Run from the worktree root (`/root/multi-agent/.worktrees/p2-credential-workload`):

```
cd multi-agent
go test ./tools/eval/runner/... -count=1 -shuffle=on -race
go vet ./...
cd ..
bash multi-agent/tests/eval/workloads/credential-bound-model/eval/eval-modelproxy-overhead.sh --mode a --dry-run
bash multi-agent/tests/eval/workloads/credential-bound-model/eval/eval-modelproxy-overhead.sh --mode b --dry-run
bash multi-agent/tests/eval/workloads/credential-bound-model/eval/eval-modelproxy-overhead.sh --self-test
bash multi-agent/tests/eval/workloads/credential-bound-model/eval/hops_loopback_check.sh --self-test
bash multi-agent/tests/eval/workloads/credential-bound-model/oracle.sh multi-agent/tests/eval/workloads/credential-bound-model/fixtures/mock_workspace
```

Every command must exit 0. Anything else fails the acceptance gate.

---

## 8. Open questions (deliberately left open)

- Should `credential-a` / `credential-b` be recorded in
  `wt1-run-schema.spec.md`'s enum list right now, in this PR? Spec
  §2.5 argues no — the runner does not persist that field, so the
  enum extension is documentation only, and the touch would drag
  `wt1-run-schema.spec.md` into this PR's review surface. Decision:
  land it as a two-line note in **this** spec (§2.5) and file a
  follow-up to WT-1-run-schema's owner (the spec's §2.5 documents
  the enum extension; §9 already lists "persisting baseline_or_ablation
  to the runs table from the runner itself" as out of scope).
- Should the harness be a Go binary instead of a shell script? The
  arg surface is tiny (mode, sample count, dry-run) and the operator
  audience is comfortable with shell; a Go rewrite would inflate the
  worktree scope without adding safety (the safety-critical decisions
  are already in `codexconfig.go`). Decision: shell.
