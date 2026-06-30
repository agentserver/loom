// Package ablation is the typed flag registry for paper-v3 ablation
// experiments.
//
// Downstream packages register a *bool via Default.Register(...) from their
// init() function; the CLI binder (Phase 2 WT-2-flag-integration) flips
// those targets via Default.SetByName(name, true) before workload start.
//
// The package is intentionally tiny but sits on the trust path of every
// published experimental result: a typo'd flag name that silently no-ops
// would invalidate an entire ablation sweep with no after-the-fact way to
// tell which run was real and which was wishful thinking. See
// docs/specs/wt1-ablation-registry.spec.md (the worktree spec) for the
// full security argument and the §7 (a)–(e) mitigations.
package ablation
