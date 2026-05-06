//go:build contract

package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestContract_DelegateTaskShape(t *testing.T) {
	var mu sync.Mutex
	var seen []map[string]interface{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Catch-all DiscoverAgents
		if strings.Contains(r.URL.Path, "agents") && !strings.Contains(r.URL.Path, "/tasks") {
			json.NewEncoder(w).Encode([]agentsdk.AgentCard{
				{AgentID: "agent-a", Status: "available"},
			})
			return
		}
		// DelegateTask: POST to /api/agent/tasks
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/tasks") && !strings.Contains(r.URL.Path, "/poll") {
			body, _ := io.ReadAll(r.Body)
			var m map[string]interface{}
			_ = json.Unmarshal(body, &m)
			mu.Lock()
			seen = append(seen, m)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"task_id": "ct1", "status": "assigned",
			})
			return
		}
		// WaitForTask: GET /api/agent/tasks/ct1
		if r.Method == "GET" && strings.Contains(r.URL.Path, "tasks/ct1") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"task_id": "ct1", "status": "completed", "output": "ok",
			})
			return
		}
		t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}))
	defer srv.Close()

	cli := agentsdk.NewClient(agentsdk.Config{ServerURL: srv.URL, Name: "master"})
	cli.SetRegistration(&agentsdk.Registration{
		SandboxID: "self", TunnelToken: "t", ProxyToken: "p",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cards, err := cli.DiscoverAgents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cards, "discover should return at least one agent")

	resp, err := cli.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID: "agent-a", Prompt: "hello", Skill: "chat", TimeoutSeconds: 60,
	})
	require.NoError(t, err)
	require.Equal(t, "ct1", resp.TaskID)

	info, err := cli.WaitForTask(ctx, "ct1", time.Second)
	require.NoError(t, err)
	require.Equal(t, "completed", info.Status)
	require.Equal(t, "ok", info.Output)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 1)
	require.Equal(t, "agent-a", seen[0]["target_id"])
	require.Equal(t, "hello", seen[0]["prompt"])
	require.EqualValues(t, 60, seen[0]["timeout_seconds"])
}
