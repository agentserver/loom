package main

import (
	"flag"
	"fmt"
	"os"
)

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	urlFlag := fs.String("url", "", "observer URL, e.g. http://localhost:18091")
	tok := fs.String("token", "", "observer agent token (Bearer)")
	fs.Parse(args)
	if *urlFlag == "" || *tok == "" {
		fmt.Fprintln(os.Stderr, "--url and --token required")
		os.Exit(2)
	}
	if err := saveConfig(Config{URL: *urlFlag, Token: *tok}); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		os.Exit(1)
	}
	fmt.Println("saved to ~/.mcp-userspace/config.yaml")
}
