package driver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// mcpEnvelope mirrors the result shape mcp_server.go writes for tools/call:
//
//	{"jsonrpc":"2.0","id":<id>,"result":{"content":[{"type":"text","text":"<json>"}]}}
//
// or on error:
//
//	{"jsonrpc":"2.0","id":<id>,"error":{"code":-32000,"message":"..."}}
type mcpEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Result *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// TestIntegration_DriverBindFlow drives a real MCP server over an in-memory
// stdio pair and asserts the bind / stamp / unbound-fails-cleanly contract
// at the wire level. Env-gated to keep routine CI fast; the wire is the
// reason this test exists (the Tool-wrapper layer is already covered by
// TestBindThreadTool_RegisteredInAll in Task 2).
func TestIntegration_DriverBindFlow(t *testing.T) {
	if os.Getenv("LOOM_BIND_THREAD_INTEGRATION") != "1" {
		t.Skip("set LOOM_BIND_THREAD_INTEGRATION=1 to run")
	}

	var (
		muCap    sync.Mutex
		captured agentsdk.DelegateTaskRequest
	)
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave", DisplayName: "slave", Status: "available",
					Card: json.RawMessage(`{"skills":["chat"]}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			muCap.Lock()
			captured = req
			muCap.Unlock()
			return &agentsdk.DelegateTaskResponse{TaskID: "task-i1"}, nil
		},
	}
	tools := newLoomTestTools(t, sdk, "" /*home*/, "drv-int", "int-driver")

	// Stdio pair. We write JSON-RPC requests to inW (server reads from inR);
	// the server writes responses to outW (we read from outR). Close inW
	// when done to let Serve exit cleanly.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := NewMCPServer(tools.All())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ctx, inR, outW)
		_ = outW.Close()
	}()
	scanner := bufio.NewScanner(outR)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	readEnvelope := func(t *testing.T) mcpEnvelope {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("scanner: no line (err=%v)", scanner.Err())
		}
		var env mcpEnvelope
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &env))
		return env
	}
	writeReq := func(t *testing.T, line string) {
		t.Helper()
		_, err := io.WriteString(inW, line+"\n")
		require.NoError(t, err)
	}

	// 1) tools/call submit_task BEFORE bind_thread → JSON-RPC -32000.
	writeReq(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_task","arguments":{"prompt":"hi","skill":"chat","target_display_name":"slave"}}}`)
	env := readEnvelope(t)
	require.NotNil(t, env.Error, "expected JSON-RPC error, got result=%v", env.Result)
	require.Equal(t, -32000, env.Error.Code)
	require.Contains(t, env.Error.Message, "driver not bound to a codex thread")
	require.Nil(t, env.Result)

	// 2) tools/call bind_thread → result.content[0].text is a BindResult JSON.
	writeReq(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bind_thread","arguments":{"thread_id":"019ef3bd-42c8-7731-85b7-7177ae747389"}}}`)
	env = readEnvelope(t)
	require.Nil(t, env.Error)
	require.NotNil(t, env.Result)
	require.Len(t, env.Result.Content, 1)
	require.Equal(t, "text", env.Result.Content[0].Type)
	var bres BindResult
	require.NoError(t, json.Unmarshal([]byte(env.Result.Content[0].Text), &bres))
	require.True(t, bres.Bound)
	require.Equal(t, "019ef3bd-42c8-7731-85b7-7177ae747389", bres.ThreadID)
	require.Equal(t, "drv-int", bres.AgentID)
	require.Equal(t, "int-driver", bres.DisplayName)

	// 3) tools/call submit_task → result.content[0].text decodes to the
	//    DelegateTask response; captured request carries a parseable
	//    loom_origin marker pointing at the bound thread.
	const boundID = "019ef3bd-42c8-7731-85b7-7177ae747389"
	writeReq(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"submit_task","arguments":{"prompt":"hi","skill":"chat","target_display_name":"slave"}}}`)
	env = readEnvelope(t)
	require.Nil(t, env.Error)
	require.NotNil(t, env.Result)
	require.Len(t, env.Result.Content, 1)
	require.Contains(t, env.Result.Content[0].Text, `"task_id":"task-i1"`)
	muCap.Lock()
	sc := captured.SystemContext
	muCap.Unlock()
	// Parse — substring match would still pass if the marker's JSON shape
	// drifted to a stale schema. ParseLoomOrigin enforces the wire-shape
	// contract: it returns ok=false if `{"loom_origin":{...}}` is missing
	// or malformed (pkg/agentbackend/loomorigin.go:57).
	parent, _, ok := agentbackend.ParseLoomOrigin(sc)
	require.True(t, ok, "SystemContext must contain a parseable loom_origin marker, got %q", sc)
	require.Equal(t, boundID, parent.SessionID,
		"parent_session_id MUST equal the bound thread id")
	require.Equal(t, "drv-int", parent.AgentID)
	require.Equal(t, "int-driver", parent.DisplayName)

	// Cleanly stop Serve: close stdin → reader goroutine exits with EOF.
	require.NoError(t, inW.Close())
	select {
	case err := <-serveErr:
		// Serve returns ctx.Err() (nil) on clean EOF; any other error fails.
		if err != nil && err != context.Canceled {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin EOF")
	}
}
