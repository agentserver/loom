package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
	"github.com/yourorg/multi-agent/internal/observerstore"
	"github.com/yourorg/multi-agent/internal/promotionaudit"
	"github.com/yourorg/multi-agent/internal/userspace"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	as := fs.String("as", "", "mcp|skill (auto-detect if empty)")
	scope := fs.String("scope", "user", "user|project (skill only)")
	projectRoot := fs.String("project-root", ".", "for --scope=project")
	overwrite := fs.Bool("overwrite", false, "overwrite existing install")
	workspace := fs.String("workspace", "", "workspace_id to record install against on server (required to track install server-side)")
	// B6 promotion-audit fields — spec §2.3. All four required.
	promotedBy := fs.String("promoted-by-user-id", "", "audit: promoting user id slug (^[A-Za-z0-9_-]{8,128}$)")
	driverThread := fs.String("driver-thread-id", "", "audit: driver thread id (^[A-Za-z0-9_-]{8,128}$)")
	reason := fs.String("promotion-reason", "", "audit: one of explicit_user_request|driver_agent_inferred|batch_import|ci_seed")
	candTask := fs.String("candidate-source-task-id", "", "audit: originating candidate task id or sentinel (^[A-Za-z0-9_-]{8,128}$)")
	// Observer store path — needed to write the audit row. Optional
	// only in the specific "offline install with all four fields set
	// and the operator asserting no audit sink" mode. Empty path with
	// --allow-no-audit is refused; the CLI errs on the side of NOT
	// silently skipping the audit (§7 (i) / spec §2.3).
	observerPath := fs.String("observer-db", "", "path to observer.db (required to write promotion_audit row)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace install [--observer-db PATH --promoted-by-user-id ... --driver-thread-id ... --promotion-reason ... --candidate-source-task-id ...] <slug>@<ver>")
		os.Exit(2)
	}
	// Validate audit fields BEFORE the tarball fetch so a malformed
	// input aborts without network side-effects (§2.3).
	auditFields, err := buildInstallAuditFields(*workspace, *promotedBy, *driverThread, *reason, *candTask)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	slug, ver := parseSlugVer(fs.Arg(0))
	if ver == "" {
		fmt.Fprintln(os.Stderr, "version required: <slug>@<ver>")
		os.Exit(2)
	}
	auditFields.MCPName = slug
	if err := promotionaudit.Validate(auditFields); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	cfg, err := loadConfig()
	failIf(err)
	client := newClient(cfg)
	tarball, err := client.PullTarball(slug, ver)
	failIf(err)
	_, files, err := pack.ReadTarball(tarball)
	failIf(err)

	// Detect kind.
	kind := *as
	if kind == "" {
		for _, f := range files {
			if f.Path == "skill/SKILL.md" {
				kind = "skill"
				break
			}
		}
		if kind == "" {
			kind = "mcp"
		}
	}

	switch kind {
	case "skill":
		skillFiles := make([]userspace.SkillFile, 0, len(files))
		for _, f := range files {
			skillFiles = append(skillFiles, userspace.SkillFile{Path: f.Path, Content: f.Content})
		}
		dir, err := userspace.ResolveSkillDir(userspace.InstallScope(*scope), *projectRoot)
		failIf(err)
		dest, err := userspace.InstallSkill(skillFiles, dir, *overwrite)
		failIf(err)
		fmt.Printf("skill installed to %s\n", dest)
	case "mcp":
		// v1: copy tarball contents into ./generated_mcp/<slug>/ so the user can
		// run scaffold-mcp-server + mcp-acceptance + register_slave_mcp manually.
		dest := "generated_mcp/" + slug
		failIf(os.MkdirAll(dest, 0o755))
		for _, f := range files {
			full := dest + "/" + f.Path
			_ = os.MkdirAll(parentDir(full), 0o755)
			failIf(os.WriteFile(full, f.Content, 0o644))
		}
		fmt.Printf("mcp package extracted to %s — next: scaffold-mcp-server / mcp-acceptance / register_slave_mcp\n", dest)
	default:
		fmt.Fprintln(os.Stderr, "unknown --as:", kind)
		os.Exit(2)
	}

	// Record install server-side so `list --workspace mine` reflects it and
	// other workspaces' search results show the right installed_version.
	if *workspace != "" {
		if err := client.Install(*workspace, slug, ver); err != nil {
			fmt.Fprintln(os.Stderr, "warn: failed to record install server-side:", err)
			os.Exit(1)
		}
		fmt.Printf("recorded installation in workspace %s\n", *workspace)
	} else {
		fmt.Fprintln(os.Stderr, "note: install only extracted locally; pass --workspace <id> to record on server")
	}

	// Write the promotion_audit row (§2.3). Failure is a HARD error
	// for the CLI (unlike the driver-tool path which degrades to a
	// log line, because there is no persistent user context to fall
	// back on here).
	if *observerPath == "" {
		fmt.Fprintln(os.Stderr, "error: --observer-db is required to write the promotion_audit row (spec §2.3); use the audit-writer path or set the observer db path")
		os.Exit(2)
	}
	if err := writeInstallAuditRow(*observerPath, auditFields); err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to write promotion_audit row:", err)
		os.Exit(1)
	}
	fmt.Printf("promotion_audit row written for %s\n", slug)
}

// buildInstallAuditFields validates the four required flags and builds
// the AuditFields for install. workspace_id may be "" for offline
// install per §2.3.
func buildInstallAuditFields(workspace, promotedBy, driverThread, reason, candTask string) (promotionaudit.AuditFields, error) {
	if promotedBy == "" {
		return promotionaudit.AuditFields{}, errors.New("--promoted-by-user-id is required")
	}
	if driverThread == "" {
		return promotionaudit.AuditFields{}, errors.New("--driver-thread-id is required")
	}
	if reason == "" {
		return promotionaudit.AuditFields{}, errors.New("--promotion-reason is required")
	}
	if candTask == "" {
		return promotionaudit.AuditFields{}, errors.New("--candidate-source-task-id is required")
	}
	parsedReason, err := promotionaudit.Parse(reason)
	if err != nil {
		return promotionaudit.AuditFields{}, err
	}
	return promotionaudit.AuditFields{
		WorkspaceID:           workspace, // may be "" for offline install
		Action:                promotionaudit.ActionInstall,
		PromotedByUserID:      promotedBy,
		DriverThreadID:        driverThread,
		PromotionReason:       parsedReason,
		CandidateSourceTaskID: candTask,
		// RegistryHashAfter="" is allowed for ActionInstall — install
		// has no registry-hash effect (spec §3.1).
	}, nil
}

// writeInstallAuditRow opens the observer DB and writes one row via
// promotionaudit.SQLiteWriter. Returns the wrapped error on any
// failure — caller decides exit code.
func writeInstallAuditRow(dbPath string, f promotionaudit.AuditFields) error {
	store, err := observerstore.OpenSQLite(dbPath)
	if err != nil {
		return fmt.Errorf("open observer.db: %w", err)
	}
	defer store.Close()
	w := promotionaudit.NewSQLiteWriter(store.DB())
	defer w.Close()
	return w.Write(context.Background(), f)
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
