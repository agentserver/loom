# Agent Config Migration (June 2026)

`internal/driver/config.go` and `internal/config/config.go` (slave)
collapse the per-backend `claude:` / `codex:` top-level YAML blocks
into a single `agent:` block. Linked: [issue #15](https://github.com/agentserver/loom/issues/15).

## Before

```yaml
agent:
  kind: claude

claude:
  bin: claude
  workdir: /loom/project
  extra_args: []

# (codex slaves used the codex: block instead)
```

## After

```yaml
agent:
  kind: claude        # required (no implicit default)
  bin: claude         # optional; backend factory defaults to the kind name
  workdir: /loom/proj # required (no cwd fallback)
  extra_args: []
```

For codex: `agent.kind: codex`, `agent.bin: codex`. Same for any
future backend (e.g. opencode) — only the strings change.

## Migration

Edit your driver and slave config YAML(s):

1. Delete the top-level `claude:` (or `codex:`) block entirely.
2. Move its `bin`, `workdir`, and `extra_args` keys into the `agent:` block.
3. Make sure `agent.kind` is set (no default any more).

The loader emits a friendly error if it sees a legacy `claude:` or
`codex:` top-level key:

```
config /path/to/config.yaml: legacy top-level key(s) [claude] are no
longer supported; consolidate into agent: { kind, bin, workdir,
extra_args }. See docs/migration/2026-06-agent-config.md
```

## Why

`pkg/agentbackend` had per-backend sub-structs and the CLI mains had
`switch cfg.Agent.Kind { case "claude": ...; case "codex": ... }`
peppered around. Adding a new backend required ~12 file edits. After
this PR a new backend lives in `pkg/agentbackend/<name>/` and two
`_ "..."` imports — no schema changes.

## Master config

Master's `cmd/master-agent/config.go` keeps the old shape for now; it
is on the [frozen list](https://github.com/agentserver/loom/issues/15)
and will be unified in a follow-up PR.

## Deploy templates

`deploy/{linux,windows}/{driver,slave}/install.{sh,ps1}` and
`deploy/linux/{driver,slave}/bootstrap.sh` already render the new
schema — operators using these scripts next time will get the new
YAML automatically.
