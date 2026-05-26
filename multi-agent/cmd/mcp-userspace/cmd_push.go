package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/mcpmarket/manifest"
	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

func runPush(args []string) {
	fs := flag.NewFlagSet("push", flag.ExitOnError)
	slugFlag := fs.String("slug", "", "override slug (default = directory basename)")
	bumpMinor := fs.Bool("bump-minor", false, "auto-bump minor before push")
	bumpPatch := fs.Bool("bump-patch", false, "auto-bump patch before push")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace push <dir>")
		os.Exit(2)
	}
	dir := fs.Arg(0)

	cfg, err := loadConfig()
	failIf(err)

	// Detect kind from filesystem.
	kind := manifest.KindMCP
	if _, statErr := os.Stat(filepath.Join(dir, "skill", "SKILL.md")); statErr == nil {
		kind = manifest.KindSkill
	}

	// Read manifest.json if present; else synthesize a default.
	mfPath := filepath.Join(dir, "manifest.json")
	var m *manifest.Manifest
	if b, readErr := os.ReadFile(mfPath); readErr == nil {
		var parseErr error
		m, parseErr = manifest.Parse(b)
		failIf(parseErr)
	} else {
		slug := *slugFlag
		if slug == "" {
			slug = strings.ToLower(filepath.Base(dir))
		}
		m = &manifest.Manifest{
			SchemaVersion: manifest.SchemaVersion, Kind: kind,
			Slug: slug, Version: "0.1.0",
			CardRef:  "capability_card.md",
			Software: manifest.Software{Packages: []string{}},
			Hardware: manifest.Hardware{NetworkEgress: []string{}},
			Tags:     []string{}, License: "MIT",
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if kind == manifest.KindMCP {
			m.SpecRef = "spec.json"
			m.CasesRef = "tests/cases.json"
		}
	}
	if *bumpMinor || *bumpPatch {
		m.Version = bumpVersion(m.Version, *bumpMinor)
	}
	if *slugFlag != "" {
		m.Slug = *slugFlag
	}

	// Pack directory; strip any pre-existing manifest.json (we write the
	// authoritative one). No hashing needed — server computes sha of bytes.
	files, err := pack.FilesFromDir(dir)
	failIf(err)
	filtered := files[:0]
	for _, f := range files {
		if f.Path == "manifest.json" {
			continue
		}
		filtered = append(filtered, f)
	}
	mfBytes, _ := json.Marshal(m)
	withMF := append([]pack.File{{Path: "manifest.json", Content: mfBytes}}, filtered...)
	finalTar, _, err := pack.WriteTarball("mcp-package-"+m.Slug+"-"+m.Version, withMF)
	failIf(err)

	client := newClient(cfg)
	resp, err := client.Push(finalTar, mfBytes)
	failIf(err)
	fmt.Printf("pushed %s@%s (dedup=%v, blob=%s)\n",
		resp["slug"], resp["version"], resp["dedup"], resp["blob_sha256"])
}

func bumpVersion(v string, minor bool) string {
	var maj, min, pat int
	_, err := fmt.Sscanf(v, "%d.%d.%d", &maj, &min, &pat)
	if err != nil {
		return "0.1.0"
	}
	if minor {
		min++
		pat = 0
	} else {
		pat++
	}
	return fmt.Sprintf("%d.%d.%d", maj, min, pat)
}

func failIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
