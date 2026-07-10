# Public Benchmark Task 1 Baselines Design

## Goal

Promote the existing Terminal-Bench `heterogeneous-dates` adaptation from a smoke-only scaffold into the first public-task-derived benchmark workload that can be run through the baseline harness.

## Scope

This design covers one workload only:

- `public-terminal-heterogeneous-dates`

It does not claim the full 15-task benchmark is complete. It creates a repeatable baseline slice so the paper can inspect whether the first public benchmark task produces useful baseline rows and reports.

## Baselines

The task will support three baseline paths:

- `manual_ssh`: real local bash script that produces `avg_temp.txt` from the two CSV inputs.
- `single_machine_codex`: real local `codex exec` baseline using the existing Codex baseline harness.
- `cloud_sandbox_e2b`: for this task, a container-local Codex substitute is acceptable for the "cloud sandbox" effect. The implementation will add a `--container-codex` mode that runs Codex inside an isolated Docker container mounted on the per-run workspace, without requiring E2B.

Dry-run remains unchanged: the harness projects `fixtures/mock_workspace` and does not contact external services.

## Data Flow

The harness copies `fixtures/` into a fresh 0700 workspace. Real baseline implementations then write `avg_temp.txt` at the workspace root. The unchanged workload oracle grades `avg_temp.txt` against the public task answer.

`manual_ssh` computes the answer directly with Python or POSIX shell from `fixtures/task-deps/daily_temp_sf_high.csv` and `fixtures/task-deps/daily_temp_sf_low.csv`.

`single_machine_codex` prompts Codex to read those same two CSV files and write only `avg_temp.txt`.

`cloud_sandbox_e2b --container-codex` runs Codex in a container with the workspace mounted as `/workspace`; the container must write `/workspace/avg_temp.txt`, and the local oracle grades the result afterward.

## Error Handling

Unknown workload IDs keep their current sentinel errors. Container Codex mode fails as a baseline runtime error if Docker is missing, the image cannot run, Codex exits non-zero, or `avg_temp.txt` is not created.

The cloud baseline's existing secret scan remains in `Prepare`; container mode does not upload skipped or unsafe files. Stderr from Codex is included only as a sanitized baseline error.

## Testing

Tests are added before implementation:

- Unit tests assert all three baseline workload maps include `public-terminal-heterogeneous-dates`.
- `manual_ssh` real-mode harness test verifies the oracle passes for this workload.
- `single_machine_codex` fake-Codex argv/output test verifies the new prompt path and expected output.
- `cloud_sandbox` container-mode tests use a fake Docker binary to assert container arguments and output validation without requiring Docker.

End-to-end verification will run the baseline unit tests and then run real baseline commands for `manual_ssh`, `single_machine_codex`, and container Codex when Docker is available.
