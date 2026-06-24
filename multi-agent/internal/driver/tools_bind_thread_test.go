package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBindThread_SetsAndStores verifies a valid call mutates parentThread and
// returns the expected BindResult (carrying the configured agent_id + display_name).
func TestBindThread_SetsAndStores(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "" /*codexHome*/, "drv-1", "prod-driver")
	res, err := tools.BindThread(context.Background(), "019ef3bd-42c8-7731-85b7-7177ae747389")
	require.NoError(t, err)
	require.Equal(t, BindResult{
		Bound:       true,
		ThreadID:    "019ef3bd-42c8-7731-85b7-7177ae747389",
		AgentID:     "drv-1",
		DisplayName: "prod-driver",
	}, res)
	p := tools.parentThread.Load()
	require.NotNil(t, p)
	require.Equal(t, "019ef3bd-42c8-7731-85b7-7177ae747389", *p)
}

// TestBindThread_RejectsInvalidFormat covers every shape the validator must
// reject. None of these may mutate parentThread.
func TestBindThread_RejectsInvalidFormat(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"empty", ""},
		{"whitespace_only", "   "},
		{"contains_dollar", "thread$id"},
		{"contains_brace_open", "thread{id"},
		{"contains_brace_close", "thread}id"},
		{"contains_slash", "thread/id"},
		{"contains_newline", "thr\nid"},
		{"unexpanded_placeholder", "${CODEX_THREAD_ID}"},
		{"too_long", strings.Repeat("a", 129)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-X", "driver-X")
			res, err := tools.BindThread(context.Background(), tc.in)
			require.Error(t, err, "input %q must be rejected", tc.in)
			require.Contains(t, err.Error(), "invalid thread_id format")
			require.Equal(t, BindResult{}, res)
			require.Nil(t, tools.parentThread.Load(), "parentThread must not be set on rejection")
		})
	}
}

// TestBindThread_Idempotent_SameValue calls with the same id twice; the second
// call must succeed and not change observable state.
func TestBindThread_Idempotent_SameValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	_, err = tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	require.Equal(t, "thr-1", *tools.parentThread.Load())
}

// TestBindThread_Replaces_DifferentValue codifies the /resume semantic: a
// later bind_thread with a NEW value replaces the previously-bound id.
func TestBindThread_Replaces_DifferentValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	_, err = tools.BindThread(context.Background(), "thr-2")
	require.NoError(t, err)
	require.Equal(t, "thr-2", *tools.parentThread.Load())
}

// TestBindResult_JSONTags pins the wire-shape the MCP wrapper relies on.
func TestBindResult_JSONTags(t *testing.T) {
	b, err := json.Marshal(BindResult{Bound: true, ThreadID: "t", AgentID: "a", DisplayName: "d"})
	require.NoError(t, err)
	require.JSONEq(t, `{"bound":true,"thread_id":"t","agent_id":"a","display_name":"d"}`, string(b))
}

// TestBindThreadTool_RegisteredInAll is the non-gated regression for the
// "method exists but tool not wired into MCP" failure mode. If All() drops
// the registration, tools/list won't surface bind_thread and codex can't
// call it; tests that go through Tools.BindThread directly wouldn't notice.
func TestBindThreadTool_RegisteredInAll(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "" /*home*/, "drv-1", "prod-driver")

	var found Tool
	for _, tool := range tools.All() {
		if tool.Name() == "bind_thread" {
			found = tool
			break
		}
	}
	require.NotNil(t, found, "bind_thread missing from Tools.All() — wrapper not registered")

	// Schema sanity: must require thread_id.
	var schema struct {
		Required   []string                          `json:"required"`
		Properties map[string]map[string]interface{} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(found.InputSchema(), &schema))
	require.Contains(t, schema.Required, "thread_id")
	require.Contains(t, schema.Properties, "thread_id")

	// Round-trip Call: valid UUIDv7 must produce a BindResult-shaped response.
	raw, err := found.Call(context.Background(),
		json.RawMessage(`{"thread_id":"019ef3bd-42c8-7731-85b7-7177ae747389"}`))
	require.NoError(t, err)
	var got BindResult
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, BindResult{
		Bound:       true,
		ThreadID:    "019ef3bd-42c8-7731-85b7-7177ae747389",
		AgentID:     "drv-1",
		DisplayName: "prod-driver",
	}, got)
}

// TestBindThreadTool_Call_InvalidJSON exercises the json.Unmarshal error
// path: the wrapper must convert it into an MCPToolError so codex sees a
// clean JSON-RPC -32000 instead of a generic Go error string.
func TestBindThreadTool_Call_InvalidJSON(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	tool := toolByName(t, tools, "bind_thread")
	_, err := tool.Call(context.Background(), json.RawMessage(`{not json}`))
	require.Error(t, err)
	var mcpErr *MCPToolError
	require.ErrorAs(t, err, &mcpErr, "wrapper must return *MCPToolError")
	require.Contains(t, mcpErr.Message, "invalid args")
}

// TestBindThreadTool_Call_ValidatorRejection ensures validator errors from
// the underlying BindThread are also rewrapped as MCPToolError.
func TestBindThreadTool_Call_ValidatorRejection(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	tool := toolByName(t, tools, "bind_thread")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"thread_id":"${VAR}"}`))
	require.Error(t, err)
	var mcpErr *MCPToolError
	require.ErrorAs(t, err, &mcpErr)
	require.Contains(t, mcpErr.Message, "invalid thread_id format")
}

// TestRequireBoundThread_UnboundReturnsActionableError covers the path the
// model will see when the skill skipped Step 1 of Initialization.
func TestRequireBoundThread_UnboundReturnsActionableError(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	id, err := tools.requireBoundThread()
	require.Error(t, err)
	require.Empty(t, id)
	// The message MUST mention bind_thread AND the script — the model
	// uses both keywords to recover.
	require.Contains(t, err.Error(), "bind_thread")
	require.Contains(t, err.Error(), "discover-thread.sh")
}

// TestRequireBoundThread_ReturnsCapturedValue is the success path.
func TestRequireBoundThread_ReturnsCapturedValue(t *testing.T) {
	tools := newLoomTestTools(t, &fakeSDK{}, "", "drv-1", "d1")
	_, err := tools.BindThread(context.Background(), "thr-1")
	require.NoError(t, err)
	id, err := tools.requireBoundThread()
	require.NoError(t, err)
	require.Equal(t, "thr-1", id)
}
