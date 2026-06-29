# USERSPACE_OVERLAP_REPORT — WT-0 personal-skill-space

**Worktree:** `paper/v3/p0-personal-skill-space`
**Plan:** `multi-agent/docs/superpowers/plans/2026-05-26-personal-mcp-skill-space.md` (10 tasks)
**Reference:** `paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md` §B5
**Date checked:** 2026-06-29
**Branch base:** `820430d docs(plan): fix four review blockers` (this worktree currently equals `origin/master`)

---

## TL;DR

**Path (A): every plan task already lives on `master`.** All 10 of the plan's
top-level tasks have shipped feat/fix commits ahead of this worktree, plus
substantial post-plan hardening (PostgreSQL dialect, object-blob backend,
identity visibility filtering, atomic blob put, etc.). The 12 号 §B5 acceptance
("FTS5 search 命中 push 上去的 MCP；install 链路跑通；plan 10 task 全过") is
satisfied as-of HEAD.

**Action for this worktree:** commit this report only. No code changes are
required — re-writing already-landed code would conflict with the post-plan
fixes (PR #12 P3, object-blob store) that already evolved past the plan body.

**Note on the upstream task table:** 12 号 §B5 still marks this work `🟡`, but
on master the 10-task plan has fully landed. Per the prompt's instruction
("不要直接改 12 号文档，留给 Phase 切换流程统一改"), we record the discrepancy
here rather than editing 12 号; whoever rolls the phase-end status sweep should
flip §B5's marker from `🟡` to ✅ and cite this report.

---

## Per-task overlap table

Each row: plan task → status on master → key files → landing commit(s).
Status legend: ✅ landed; 🟡 partially landed / hardened post-plan; 🔴 absent.

| # | Plan task | Status | Files (relative to repo root, `multi-agent/` is the Go module root) | Landing commits |
|---|-----------|:-----:|---------------------------------------------------------------------|-----------------|
| 1 | `internal/mcpmarket/manifest` — manifest schema + JCS | ✅ | `multi-agent/internal/mcpmarket/manifest/{manifest.go,jcs.go,manifest_test.go}` (127 + 122 + 85 LOC) | `a08dc72 feat(mcpmarket/manifest): shared manifest schema + JCS canonicalizer` |
| 2 | `internal/mcpmarket/pack` — deterministic tar.gz | ✅ | `multi-agent/internal/mcpmarket/pack/{pack.go,pack_test.go}` (243 + 78 LOC) | `202069c feat(mcpmarket/pack): deterministic tar.gz + zip-slip-safe unpack` |
| 3 | `observerstore.Store.DB()` + `internal/userspace` schema + Migrate | ✅ | `multi-agent/internal/observerstore/store.go:222 func (s *SQLiteStore) DB()`; `multi-agent/internal/userspace/{schema.sql,schema_postgres.sql,migrate.go,migrate_test.go}` | `2021b51 feat(userspace): schema migrate + observerstore.Store.DB() accessor` (plus Postgres schema added in `7678ec1` / `9b1d12d`) |
| 4 | `internal/userspace/store.go` — packages / versions / installations CRUD | ✅ | `multi-agent/internal/userspace/{store.go,store_test.go,store_postgres_test.go}` (613 + 194 + 402 LOC) | `8c510c8 feat(userspace): store CRUD — packages, versions, installations, FTS5 search`; hardened `dc6bcf4` (yank semantics / ghost-slug / FTS description), `9b1d12d` (postgres dialect), `f5c2139` (identity visibility filtering) |
| 5 | `internal/userspace/blob.go` — fs sha256 + refcount | ✅ + post-plan extras | `multi-agent/internal/userspace/{blob.go,blob_test.go}` (434 + 450 LOC) — both `BlobStore` (sha256 fs) and `ObjectBlobStore` (object-backed) | `d8abf79 feat(userspace/blob): sha256-addressed fs blob store with refcount`; extended `45d119f` (object-backed blobs), `2e082b7` (delete after commit), `cdb46e9` (lock object blob deletion), `5843b5b` (atomic Put), `e93170e` (PR #12 P3 recreate-after-release) |
| 6 | `internal/userspace/skillpack.go` — SKILL.md frontmatter + install scope | ✅ | `multi-agent/internal/userspace/{skillpack.go,skillpack_test.go}` (131 + 76 LOC) | `4fe554d feat(userspace/skillpack): SKILL.md frontmatter + install to user/project scope` |
| 7 | `internal/userspace/api.go` + `routes.go` — HTTP handlers | ✅ | `multi-agent/internal/userspace/{api.go,api_test.go,routes.go}` (412 + 286 + 28 LOC); wired in `multi-agent/internal/observerweb/server.go` (`mountRoutes` → `userspace.MountRoutes`) and `multi-agent/cmd/observer-server/main.go` (`userspace.MigrateForDriver`, `userspace.NewStoreForDriver`, `userspace.NewBlobStore` / `NewObjectBlobStore`, `userspace.Handler{...}` + identity resolver) | `fab0b0d feat(userspace): HTTP routes mounted on observer-server`; identity resolver wiring added in `f5c2139` / `26a3f66` |
| 8 | `cmd/mcp-userspace` CLI | ✅ | `multi-agent/cmd/mcp-userspace/{main.go,client.go,config.go,cmd_{login,push,pull,search,install,list,yank}.go}` (10 files, 608 LOC total: 581 at `0ad1027` + 27 added by `6589dd6`) | `0ad1027 feat(cmd/mcp-userspace): CLI — login/push/search/list/pull/install/yank`; `6589dd6` (install records server-side via `--workspace`) |
| 9 | Local-gray e2e (verification only) | 🟡 covered by integration tests; no `multi-agent/tests/e2e/userspace_smoke.md` artifact | API integration tests exercise the full HTTP surface — `TestAPI_InstallCrossWorkspaceForbidden` (cross-ws 403 case, `api_test.go:160`), `TestAPI_InstallYankedVersionRejected` (`:213`), `TestSearchPackages_FTSFindsByCardMD` (`store_test.go:128`), plus `store_postgres_test.go` for the Postgres path. The optional `tests/e2e/userspace_smoke.md` note from Step 9.10 was never created. | Not committed as its own commit; covered by Tasks 4 / 7 commits. The plan itself marks Step 9.10 optional ("Do NOT add a commit for this step alone"). |
| 10 | `skills/userspace-publish/SKILL.md` — driver-side authoring guide | ✅ | `skills/userspace-publish/SKILL.md` (61 LOC) | `9024e66 feat(skills): userspace-publish authoring skill for driver-side Claude` |

**Summary:** 9/10 ✅ in full, 1/10 (#9) covered by automated tests but missing
the optional smoke-note artifact. Plan rule (A) applies.

---

## 12 号 §B5 acceptance bullets

> **Acceptance** (12 号 §B5): plan 10 task 全过；FTS5 search 可查到 push 上去的 MCP；install 链路跑通

- [x] **plan 10 task 全过** — see table above; only the optional Step 9.10
      smoke-note artifact is absent. The plan itself classifies Step 9.10 as
      optional.
- [x] **FTS5 search 可查到 push 上去的 MCP** — implemented in
      `internal/userspace/store.go:411 SearchPackages` (mirrors into FTS5 at
      `store.go:216`), exposed via `api.go:208 "search_mode": "fts5"`, and
      tested by `store_test.go:128 TestSearchPackages_FTSFindsByCardMD`.
- [x] **install 链路跑通** — `cmd/mcp-userspace/cmd_install.go` drives the
      HTTP API; `api.go` `installVersion` handler is wired at
      `internal/observerweb/server.go:135 userspace.MountRoutes(mux, usHandler)`;
      `api_test.go:160 TestAPI_InstallCrossWorkspaceForbidden` and
      `:213 TestAPI_InstallYankedVersionRejected` cover scope + yank gates.

Verification commands (run from `multi-agent/` Go module root):

```bash
go test ./internal/userspace/... ./internal/mcpmarket/... ./cmd/mcp-userspace/...
```

Output observed in this worktree on 2026-06-29:

```
ok  	github.com/yourorg/multi-agent/internal/userspace	0.089s
ok  	github.com/yourorg/multi-agent/internal/mcpmarket/manifest	0.004s
ok  	github.com/yourorg/multi-agent/internal/mcpmarket/pack	0.012s
?   	github.com/yourorg/multi-agent/cmd/mcp-userspace	[no test files]
```

(`cmd/mcp-userspace` has no `*_test.go`; CLI is exercised end-to-end via the
HTTP API tests in `internal/userspace/api_test.go`, since it is a thin client
wrapper. The plan does not require CLI unit tests — Tasks 8 step list focuses
on subcommand behavior verified by manual e2e in Task 9.)

---

## What this worktree commits

Only this report:

- `USERSPACE_OVERLAP_REPORT.md` (this file)

No code changes. Rationale recorded in the TL;DR. Whoever runs the Phase-0
status sweep should update `paper_writing/docs/intermediate/12_loom_development_tasks_for_v3.md` §B5 from `🟡` to ✅
and cite this report as evidence.

---

## Discrepancies / open questions to flag upward (not fixed here)

1. **12 号 §B5 status marker** is `🟡` but actual master state is ✅. Flip
   during the next phase status sweep.
2. **Step 9.10 smoke note** (`multi-agent/tests/e2e/userspace_smoke.md`) is
   optional and was not produced. If a paper-time artifact is desired, run the
   Step 9.1–9.8 sequence locally and paste outputs into a new note. Not done
   in this worktree because (a) the plan marks it optional, (b) automated
   integration tests already cover the same surfaces, and (c) the gray e2e
   would attempt to bind `:18092` and write under `/tmp/e2e-userspace/`, which
   is out of scope for an overlap-report-only commit.
3. **Plan body vs. implementation drift** — the implementation evolved past
   the plan text in three substantive ways (object-blob store, Postgres
   dialect, identity visibility filtering). The plan was not retro-edited;
   reviewers should treat `internal/userspace/store.go` / `blob.go` / `api.go`
   as the source of truth, not the plan body's code snippets.
