package driver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yourorg/multi-agent/internal/observerstore"
)

// bindThreadTool exposes Tools.BindThread over MCP. The Tools-level method
// owns validation and state; this wrapper only handles JSON unmarshalling
// and MCPToolError translation so wire errors are JSON-RPC -32000 instead
// of generic Go strings.
type bindThreadTool struct{ t *Tools }

func (b *bindThreadTool) Name() string { return "bind_thread" }

func (b *bindThreadTool) Description() string {
	return "Bind the driver MCP to its parent Codex thread_id. Must be " +
		"called once per session before any parent-link submission " +
		"(submit_task / submit_contract_task with skill chat / chat_resume / " +
		"fanout / fanout_strict / route, or resume_task). The multiagent " +
		"skill runs the discover-thread.sh script and passes its output here. " +
		"Calling with a new thread_id replaces the bound value (supports " +
		"/resume mid-session)."
}

func (b *bindThreadTool) InputSchema() json.RawMessage {
	// Schema string interpolates validThreadIDPattern (Task 1) so the wire
	// pattern and the validator stay in lockstep — no copy-paste drift.
	return json.RawMessage(fmt.Sprintf(`{
        "type":"object",
        "properties":{
            "thread_id":{
                "type":"string",
                "description":"Backend-native thread/session id of the parent agent. Codex's value is a UUIDv7 like 019ef3bd-42c8-7731-85b7-7177ae747389; the validator accepts any opaque id matching %s, so other backends or tests can pass non-UUID strings (e.g. 'thr-parent') as well.",
                "pattern":%q
            }
        },
        "required":["thread_id"]
    }`, validThreadIDPattern, validThreadIDPattern))
}

func (b *bindThreadTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &MCPToolError{Message: "invalid args: " + err.Error(), Category: observerstore.FailContractViolation}
	}
	result, err := b.t.BindThread(ctx, args.ThreadID)
	if err != nil {
		// BindThread can fail for several reasons (validation, downstream
		// state checks). Leave untagged until BindThread itself returns
		// typed errors a tag can be inferred from.
		return nil, &MCPToolError{Message: err.Error(), Category: observerstore.FailUnknown}
	}
	return json.Marshal(result)
}
