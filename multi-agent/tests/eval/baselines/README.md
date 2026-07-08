# tests/eval/baselines — non-Loom baselines for C5 comparison

This tree ships three baselines for §E1–§E3 of the paper's evaluation
plan (12 号 §E1 / §E2 / §E3). Each baseline is a standalone Go binary
that:

1. Copies a workload's `fixtures/` into a fresh 0700 tempdir.
2. Runs a per-baseline "agent" that produces the workload's
   `outputs.write_targets` inside that tempdir.
3. Invokes the workload's `success_oracle` (same oracle Loom uses;
   contract in 13 号 §1.3) with the tempdir as `$1`.
4. Emits a single-row CSV whose `baseline_or_ablation` column
   distinguishes this run in the D1 `runs` table.

## Baselines

| Directory | `baseline_or_ablation` | Corresponds to |
|---|---|---|
| `manual_ssh/`           | `manual_ssh`                  | §E1 — user manually ssh + hand-run |
| `single_machine/`       | `single_machine_claude_code`  | §E2 — Claude Code single-machine agent (reference; not in the matrix after wt4-codex-only) |
| `single_machine_codex/` | `single_machine_codex`        | §E2 — OpenAI Codex CLI single-machine agent (active baseline for wt4-codex-only) |
| `cloud_sandbox/`        | `cloud_sandbox_e2b`           | §E3 — E2B cloud sandbox |

## Running

```bash
# From multi-agent/ (module root):
go test ./tests/eval/baselines/... -count=1 -race

# 15-run smoke (dry-run for all baselines):
go test -tags matrix ./tests/eval/baselines/ -run TestMatrix15Runs

# Or via run.sh wrappers:
for wl in cross-device-code-mod remote-data-processing windows-only-artifact \
          missing-parser-converter credential-bound-model; do
  for bl in manual_ssh single_machine_codex cloud_sandbox; do
    bash tests/eval/baselines/$bl/run.sh --workload $wl --dry-run \
      --out /tmp/smoke-$wl-$bl.csv
  done
done
```

Every dry-run invocation writes a 2-line CSV; a real (non-dry-run)
`cloud_sandbox` invocation would dial E2B and requires
`--forward-e2b-api-key` + `E2B_API_KEY` set. See
`docs/specs/wt2-baselines.spec.md` §7 for the full security posture.

## Adding a fourth baseline

1. `mkdir tests/eval/baselines/<name>/` — copy `manual_ssh/` as a template.
2. Implement `harness.BaselineImpl` in `impl.go` (four methods: `Name`,
   `AgentForwards`, `Prepare`, `ExecuteAgent`).
3. Add a `main.go` shim that mirrors `manual_ssh/main.go` but registers
   any baseline-specific flags (`--forward-*-api-key`).
4. Add per-workload data in `workloads.go` (script or prompt table).
5. Add impl tests + build test.
6. Update `matrix_test.go`'s `baselines` slice with the new dir + name.
7. Pick a `baseline_or_ablation` string matching
   `^[a-z][a-z0-9_-]{2,63}$` (spec §7(d)); write it as the impl's
   `Name()` return value.

## Design references

- Spec:  `docs/specs/wt2-baselines.spec.md`
- Plan:  `docs/specs/wt2-baselines.plan.md`
- Oracle contract: `paper_writing/docs/intermediate/13_workload_spec.md` §1.3
- Env whitelist parent contract: `multi-agent/tools/eval/runner/subprocess.go`

## Security posture (one-line each; details in spec §7)

- (a) Env split: agent vs oracle; oracle env identical to WT-1 runner.
- (b) Cloud upload MUST scan with `internal/secretscrub` first — no upload on match.
- (c) `manual_ssh` runs local `/bin/bash` only — never `ssh/scp/rsync/sftp`.
- (d) `baseline_or_ablation` value passes `^[a-z][a-z0-9_-]{2,63}$`.
- (e) Fixtures are copied to tempdir; source tree never mutated.
- (f) `--dry-run` short-circuits every external side effect.
- (g) Every row carries `wall_time_ms` / `api_calls` / `upload_bytes`.
- (h) `CI=true` refuses to run cloud baseline without `--dry-run`.
