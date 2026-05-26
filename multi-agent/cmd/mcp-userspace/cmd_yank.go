package main

import (
	"flag"
	"fmt"
	"os"
)

func runYank(args []string) {
	fs := flag.NewFlagSet("yank", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: mcp-userspace yank <slug> <ver>")
		os.Exit(2)
	}
	cfg, err := loadConfig()
	failIf(err)
	if err := newClient(cfg).Yank(fs.Arg(0), fs.Arg(1)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("yanked")
}
