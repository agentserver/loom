//go:build smoke

package smoke

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
)

func TestSmoke_RealMCPStdio(t *testing.T) {
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not on PATH")
	}
	e := executor.NewMCPExecutor(map[string]executor.MCPServerCfg{
		"everything": {Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-everything"}},
	})
	defer e.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"server": "everything",
		"tool":   "echo",
		"args":   map[string]string{"message": "hello-from-smoke"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := e.Run(ctx, executor.Task{ID: "s", Prompt: string(body)}, &captureSink{})
	require.NoError(t, err)
	require.True(t, strings.Contains(res.Summary, "hello-from-smoke"))
}
