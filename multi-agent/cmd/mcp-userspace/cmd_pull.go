package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/internal/mcpmarket/pack"
)

func runPull(args []string) {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	out := fs.String("out", "", "extract to this directory (default: ./<slug>)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace pull <slug>@<ver>")
		os.Exit(2)
	}
	slug, ver := parseSlugVer(fs.Arg(0))
	cfg, err := loadConfig()
	failIf(err)
	client := newClient(cfg)
	if ver == "" {
		// Look up latest via list endpoint
		fmt.Fprintln(os.Stderr, "version required for v1; try `mcp-userspace search <slug>` to see available")
		os.Exit(2)
	}
	tarball, err := client.PullTarball(slug, ver)
	failIf(err)
	dest := *out
	if dest == "" {
		dest = slug
	}
	prefix, files, err := pack.ReadTarball(tarball)
	failIf(err)
	_ = prefix
	failIf(os.MkdirAll(dest, 0o755))
	for _, f := range files {
		full := filepath.Join(dest, f.Path)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		failIf(os.WriteFile(full, f.Content, 0o644))
	}
	fmt.Printf("extracted %s@%s to %s\n", slug, ver, dest)
}

func parseSlugVer(s string) (slug, ver string) {
	i := strings.Index(s, "@")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}
