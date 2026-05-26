package main

import (
	"encoding/json"
	"flag"
	"fmt"
)

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	scope := fs.String("workspace", "mine", "mine|all")
	kind := fs.String("kind", "all", "mcp|skill|all")
	fs.Parse(args)
	cfg, err := loadConfig()
	failIf(err)
	resp, err := newClient(cfg).List(*scope, *kind)
	failIf(err)
	b, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Println(string(b))
}
