package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSlaveAgentE2E_GeneratesPersistentCapabilityDocument(t *testing.T) {
	if os.Getenv("SLAVE_AGENT_E2E_HELPER") == "1" || os.Getenv("SLAVE_AGENT_E2E_MCP") == "1" {
		t.Skip("helper process")
	}

	var mu sync.Mutex
	var postedCard map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/discovery/cards":
			defer r.Body.Close()
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				mu.Lock()
				postedCard = body
				mu.Unlock()
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/api/agent/tasks/poll":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	work := t.TempDir()
	journalDir := filepath.Join(work, "journal")
	require.NoError(t, os.MkdirAll(journalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(journalDir, "CURRENT_STATE.md"), []byte("## Tools\n- persisted current state\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(work, "dynamic_mcp.yaml"), []byte(`
servers:
  generated_e2e:
    transport: stdio
    command: python3
    args: ["mcp/generated_e2e.py"]
    tools:
      - generated_tool
`), 0o644))
	cfg := fmt.Sprintf(`server:
  url: %s
  name: slave-capdoc-e2e
credentials:
  sandbox_id: sbx-e2e
  tunnel_token: tunnel-e2e
  proxy_token: proxy-e2e
  workspace_id: ws-e2e
  short_id: short-e2e
claude:
  bin: claude
mcp_servers:
  static_e2e:
    transport: stdio
    command: %s
    args: ["-test.run", "TestSlaveAgentE2EMCPHelper"]
    env:
      SLAVE_AGENT_E2E_MCP: "1"
discovery:
  display_name: slave-capdoc-e2e
  description: capability doc e2e
  skills: [chat, mcp, register_mcp]
resources:
  memory_gb: 12
  tags: [e2e, capability-doc]
`, server.URL, os.Args[0])
	cfgPath := filepath.Join(work, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o600))

	cmd := exec.Command(os.Args[0], "-test.run", "TestSlaveAgentE2EHelper")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "SLAVE_AGENT_E2E_HELPER=1", "SLAVE_AGENT_E2E_CONFIG="+cfgPath)
	var logs lockedBuffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	docPath := filepath.Join(journalDir, "CAPABILITIES.md")
	require.Eventually(t, func() bool {
		body, err := os.ReadFile(docPath)
		if err != nil {
			return false
		}
		text := string(body)
		return strings.Contains(text, "static_e2e/echo") &&
			strings.Contains(text, "generated_e2e/generated_tool") &&
			strings.Contains(text, "persisted current state") &&
			strings.Contains(text, "memory_gb: 12")
	}, 90*time.Second, 100*time.Millisecond, "logs:\n%s", logs.String())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if postedCard == nil {
			return false
		}
		card, _ := postedCard["card"].(map[string]interface{})
		if card == nil {
			return false
		}
		if card["capability_doc_path"] != "/capabilities" {
			return false
		}
		mcpTools, _ := card["mcp_tools"].([]interface{})
		for _, raw := range mcpTools {
			tool, _ := raw.(map[string]interface{})
			if tool["server"] == "static_e2e" && tool["name"] == "echo" {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "logs:\n%s", logs.String())
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSlaveAgentE2EHelper(t *testing.T) {
	if os.Getenv("SLAVE_AGENT_E2E_HELPER") != "1" {
		return
	}
	if err := run(os.Getenv("SLAVE_AGENT_E2E_CONFIG")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSlaveAgentE2EMCPHelper(t *testing.T) {
	if os.Getenv("SLAVE_AGENT_E2E_MCP") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID int `json:"id"`
		}
		_ = json.Unmarshal(line, &req)
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"tools": []map[string]interface{}{{
					"name":        "echo",
					"description": "Echoes the provided message.",
					"inputSchema": map[string]interface{}{"type": "object"},
				}},
			},
		}
		_ = json.NewEncoder(os.Stdout).Encode(resp)
	}
}
