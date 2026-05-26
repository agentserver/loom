package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
	"github.com/yourorg/multi-agent/internal/userspace"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	as := fs.String("as", "", "mcp|skill (auto-detect if empty)")
	scope := fs.String("scope", "user", "user|project (skill only)")
	projectRoot := fs.String("project-root", ".", "for --scope=project")
	overwrite := fs.Bool("overwrite", false, "overwrite existing install")
	workspace := fs.String("workspace", "", "workspace_id to record install against on server (required to track install server-side)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace install <slug>@<ver>")
		os.Exit(2)
	}
	slug, ver := parseSlugVer(fs.Arg(0))
	if ver == "" {
		fmt.Fprintln(os.Stderr, "version required: <slug>@<ver>")
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
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
