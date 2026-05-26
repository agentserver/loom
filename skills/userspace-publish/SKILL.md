---
name: userspace-publish
description: Use when the user has finished iterating on an MCP server or skill inside this driver and wants to save it to their personal observer-backed space for use on other devices or workspaces.
---

# userspace-publish

Push a freshly-built MCP package or skill to the user's personal space hosted on observer-server.

## When to use

- User says "save this to my space", "I want to use this on my laptop too", "publish this skill/MCP", "push it to userspace"
- After a `register_slave_mcp` succeeds AND the user expresses intent to reuse

Do NOT auto-push without the user asking. This skill is opt-in.

## Preconditions

- `mcp-userspace` CLI is on PATH (built from `cmd/mcp-userspace/`)
- `~/.mcp-userspace/config.yaml` has the observer URL + this agent's token (run `mcp-userspace login --url ... --token ...` once)
- The package directory contains either `spec.json` + `src/server.py` (kind=mcp) or `skill/SKILL.md` (kind=skill)
- `capability_card.md` exists in the directory — write one if missing

## CLI usage reminder

Go's `flag.Parse` stops at the first non-flag arg. **Put all `--flags` BEFORE the positional argument**:

```
mcp-userspace push --slug wedding_almanac --bump-patch ./generated_mcp/wedding_almanac
mcp-userspace install --as mcp --workspace ws-work --overwrite wedding_almanac@1.0.0
```

Reversing the order silently fails with "usage: ..." + exit 2.

## Steps

1. Confirm with user which package they mean (path + slug).
2. If `capability_card.md` is missing, draft a 3-5 sentence description focused on what the package DOES (not how it's implemented) and offer it for review.
3. Run `mcp-userspace push --slug <slug> <dir>` — if first push, omit `--bump-*`; if updating, ask whether minor or patch bump (`--bump-minor` vs `--bump-patch`).
4. Read the response. If `dedup=true`, tell the user the exact same bytes were already there (someone pushed identical content before).
5. Tell the user how to install on another device:
   ```
   mcp-userspace login --url <observer-url> --token <that-device-token>
   mcp-userspace install --as mcp --workspace <ws-id-on-that-device> <slug>@<ver>
   ```
   For skill kind, add `--scope user` (to drop in `~/.claude/skills/`) or `--scope project --project-root <path>`.

## Failure modes

- **HTTP 409 version already exists**: user must bump version. Ask which axis (patch / minor) and re-run with `--bump-*`.
- **HTTP 400 kind mismatch**: a different slug already holds this name as the other kind (mcp vs skill). Suggest renaming.
- **HTTP 401**: token expired or wrong. Re-run `mcp-userspace login` with a fresh token from the observer.
- **HTTP 413**: package too large. Help user trim (limits: 10 MiB compressed, 5 MiB per file, 50 MiB uncompressed total, 1024 files).
- **HTTP 403 on install**: trying to record install for a workspace that doesn't match the calling token. Use a token bound to the target workspace.

## What NOT to do

- Do not push without explicit user request.
- Do not silently bump versions; always confirm bump axis.
- Do not write or modify files under `~/.mcp-userspace/` directly — always go through the CLI.
- Do not forget the `--workspace` flag on `install` — otherwise the install is local-only and `list --workspace mine` will not show it on the server.
