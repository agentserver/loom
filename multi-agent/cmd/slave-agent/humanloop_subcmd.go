package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/yourorg/multi-agent/internal/humanloop"
)

// runHumanloopMCP runs the in-binary humanloop MCP server.
// Usage: slave-agent humanloop-mcp <ipc-socket-path> <max-questions>
// Stdin/stdout = the backend's MCP transport; the IPC socket is how this
// subcommand reports user-question payloads back to the chat executor.
func runHumanloopMCP(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: slave-agent humanloop-mcp <socket-path> <max-questions>")
	}
	sock := args[0]
	max, err := strconv.Atoi(args[1])
	if err != nil || max <= 0 {
		return fmt.Errorf("max-questions must be a positive integer, got %q", args[1])
	}
	return humanloop.ServeStdio(os.Stdin, os.Stdout, sock, max)
}
