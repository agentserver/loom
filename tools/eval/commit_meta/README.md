# commit_meta — eval-run metadata collector

Phase 0 deliverable **WT-0-commit-meta** (§D6b in
`docs/intermediate/12_loom_development_tasks_for_v3.md`). Collects git commit
SHAs for the four loom-adjacent repos plus host OS facts, emits them as JSON
or YAML, and exposes a Pydantic schema that **Phase 1 WT-1-run-schema imports
directly**.

## Example

```bash
$ python -m commit_meta.collect | jq
{
  "loom_commit": "820430d (paper/v3/p0-commit-meta clean)",
  "agentserver_commit": "7155c97 (main clean)",
  "modelserver_commit": "N/A: not present at /root/modelserver",
  "app_commit": "b29fefa (feat/loom-v0.0.8-multimodel-catalog dirty)",
  "os": {
    "kernel": "Linux 7.0.9-arch1-1",
    "distro": "Arch Linux",
    "arch": "x86_64"
  },
  "collected_at_unix": 1730000000,
  "machine_hostname": "loom-dev"
}
```

## CLI

```bash
# Use defaults (cwd git root + env vars + /root/<repo> fallbacks)
python -m commit_meta.collect

# Explicit paths
python -m commit_meta.collect \
  --loom /root/multi-agent \
  --agentserver /path/to/agentserver \
  --modelserver /path/to/modelserver \
  --app /path/to/app

# YAML instead of JSON
python -m commit_meta.collect --format=yaml
```

Exit code is `0` on success, non-zero only on argparse failure — a missing
repo is **not** a failure, it becomes the string `"N/A: not present at <path>"`
in the corresponding field.

## Default search paths

For each field the collector tries the sources below in order and uses the
first one that resolves; an unresolvable path falls through to `"N/A: ..."`.

| Field                 | CLI flag         | Env var              | Hard fallback        |
| --------------------- | ---------------- | -------------------- | -------------------- |
| `loom_commit`         | `--loom`         | *(cwd git root)*     | `/root/multi-agent`  |
| `agentserver_commit`  | `--agentserver`  | `AGENTSERVER_ROOT`   | `/root/agentserver`  |
| `modelserver_commit`  | `--modelserver`  | `MODELSERVER_ROOT`   | `/root/modelserver`  |
| `app_commit`          | `--app`          | `APP_ROOT`           | `/root/app`          |

The commit string for each repo has the shape `"<short-sha> (<branch> <state>)"`
where state is `clean` or `dirty` (or `unknown` if `git status` itself fails).

## Schema re-use from Phase 1 WT-1-run-schema

The Pydantic model is the contract between this worktree and
`paper/v3/p1-run-schema`. Phase 1 should import it verbatim:

```python
from commit_meta.schema import CommitMetaSchema, OSInfo

# Parse the collector's JSON output:
meta = CommitMetaSchema.from_json(open("commit_meta.json").read())

# Or build one in-memory:
meta = CommitMetaSchema(
    loom_commit="820430d (master clean)",
    agentserver_commit="N/A: not present at /root/agentserver",
    modelserver_commit="N/A: not present at /root/modelserver",
    app_commit="N/A: not present at /root/app",
    os=OSInfo(kernel="Linux 7.0.9-arch1-1", distro="Arch Linux", arch="x86_64"),
    collected_at_unix=1730000000,
    machine_hostname="loom-dev",
)
print(meta.to_json())
```

The model uses `extra="forbid"`, so adding a field requires a coordinated edit
in both worktrees. Field names are locked by `test_field_names_match_wt1_contract`.

## Install / test

```bash
# From this directory:
pip install -e .[dev]
pytest
```

Runtime deps: `pydantic>=2.0`, `PyYAML>=6.0`. Stdlib only otherwise.

## Implementation notes

- We shell out to `git` rather than parsing `.git/HEAD` directly — linked
  worktrees, packed-refs, and detached HEADs are all handled by git's own
  `rev-parse`.
- OS info prefers `/etc/os-release` for the distro (freedesktop standard) and
  falls back to `platform.system()` on macOS or in minimal containers.
- `collected_at_unix` and `machine_hostname` are stamped at emit time so the
  same payload can be diffed against later runs from the same host.
