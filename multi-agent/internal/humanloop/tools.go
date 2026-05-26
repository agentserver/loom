package humanloop

import (
	"encoding/json"
	"fmt"
)

const submittedText = `{"status":"submitted","note":"Your question was dispatched to the user. The backend will now pause; the user's answer will arrive as your next user turn after resume."}`

const refusedText = `{"status":"refused","reason":"max questions reached for this task; decide yourself and explain in summary"}`

func toolList() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name":        "ask_user",
			"description": "Pause the conversation and ask the human (sitting at the driver) for a judgement or clarification. Use only when guessing wrong would be costly and the answer fits in one or two sentences.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question": map[string]interface{}{"type": "string"},
					"options":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"context":  map[string]interface{}{"type": "string"},
				},
				"required":             []string{"question"},
				"additionalProperties": false,
			},
		},
		{
			"name":        "request_permission",
			"description": "Pause the conversation and ask the human to approve a sensitive operation. Advisory only: an 'approve' answer does NOT grant new abilities; the user must use update_slave_claude_permissions / register_slave_mcp separately.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"intent": map[string]interface{}{"type": "string",
						"enum": []string{"run_bash", "write_path", "install_mcp", "other"}},
					"target": map[string]interface{}{"type": "string"},
					"reason": map[string]interface{}{"type": "string"},
				},
				"required":             []string{"intent", "target"},
				"additionalProperties": false,
			},
		},
	}
}

func handleCall(rawParams json.RawMessage, ipcSocket string, used *int, max int) string {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(rawParams, &p); err != nil {
		return fmt.Sprintf(`{"status":"error","reason":"bad params: %s"}`, err.Error())
	}
	if p.Name != "ask_user" && p.Name != "request_permission" {
		return fmt.Sprintf(`{"status":"error","reason":"unknown tool: %s"}`, p.Name)
	}
	if *used >= max {
		return refusedText
	}
	*used++

	var args struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
		Context  string   `json:"context"`
		Intent   string   `json:"intent"`
		Target   string   `json:"target"`
		Reason   string   `json:"reason"`
	}
	_ = json.Unmarshal(p.Arguments, &args)

	payload := Payload{
		Kind:     p.Name,
		Question: args.Question,
		Options:  args.Options,
		Context:  args.Context,
		Intent:   args.Intent,
		Target:   args.Target,
		Reason:   args.Reason,
	}

	client, err := DialIPC(ipcSocket)
	if err != nil {
		return fmt.Sprintf(`{"status":"error","reason":"ipc dial: %s"}`, err.Error())
	}
	defer client.Close()
	if err := client.Send(payload); err != nil {
		return fmt.Sprintf(`{"status":"error","reason":"ipc send: %s"}`, err.Error())
	}
	return submittedText
}
