package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/yourorg/multi-agent/internal/humanloop"
)

func runHumanloopMCP(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: driver-agent humanloop-mcp ENDPOINT_JSON_OR_SOCKET_PATH MAX_QUESTIONS")
	}
	endpointArg := args[0]
	max, err := strconv.Atoi(args[1])
	if err != nil || max <= 0 {
		return fmt.Errorf("max-questions must be a positive integer, got %q", args[1])
	}
	return humanloop.ServeStdio(os.Stdin, os.Stdout, endpointArg, max)
}
