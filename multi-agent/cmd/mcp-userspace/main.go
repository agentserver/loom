package main

import (
	"fmt"
	"os"
)

const usage = `mcp-userspace — push/pull/install personal MCP & skill packages

Usage:
  mcp-userspace login --url URL --token TOK     Save config to ~/.mcp-userspace/config.yaml
  mcp-userspace push [--slug X] [--bump-patch|--bump-minor] <dir>
  mcp-userspace search [--kind mcp|skill|all] [--limit N] "query"
  mcp-userspace list [--workspace mine|all]
  mcp-userspace pull [--out <dir>] <slug>@<ver>
  mcp-userspace install [--as mcp|skill] [--scope user|project] [--workspace <id>] [--overwrite] <slug>@<ver>
  mcp-userspace yank <slug> <ver>

Note: Go flag parsing stops at the first non-flag arg, so put all flags
BEFORE the positional argument(s).

Configuration:
  Reads ~/.mcp-userspace/config.yaml; overridable per-invocation with
  --url and --token (not implemented yet — use 'login' to update config).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		runLogin(os.Args[2:])
	case "push":
		runPush(os.Args[2:])
	case "search":
		runSearch(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "pull":
		runPull(os.Args[2:])
	case "install":
		runInstall(os.Args[2:])
	case "yank":
		runYank(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
