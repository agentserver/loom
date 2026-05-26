package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func runSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	kind := fs.String("kind", "all", "mcp|skill|all")
	limit := fs.Int("limit", 20, "max results")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace search \"query\"")
		os.Exit(2)
	}
	cfg, err := loadConfig()
	failIf(err)
	resp, err := newClient(cfg).Search(fs.Arg(0), *kind, *limit)
	failIf(err)
	b, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(b))
}
