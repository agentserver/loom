package humanloop

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// ServeStdio reads JSON-RPC 2.0 requests from r line-by-line and writes
// responses to w. The server implements the MCP subset we need:
// initialize / notifications/initialized / tools/list / tools/call.
// endpointArg is the executor's IPC endpoint; max is the per-process quota for
// tools/call invocations of ask_user|request_permission.
func ServeStdio(r io.Reader, w io.Writer, endpointArg string, max int) error {
	ep, err := ParseEndpointArg(endpointArg)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	used := 0
	for sc.Scan() {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeResult(w, req.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]interface{}{"name": "humanloop", "version": "1"},
			})
		case "notifications/initialized":
			// no reply for notifications
		case "tools/list":
			writeResult(w, req.ID, map[string]interface{}{"tools": toolList()})
		case "tools/call":
			text := handleCall(req.Params, ep, &used, max)
			writeResult(w, req.ID, map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": text},
				},
			})
		default:
			writeError(w, req.ID, -32601, "method not found: "+req.Method)
		}
	}
	return sc.Err()
}

func writeResult(w io.Writer, id json.RawMessage, result interface{}) {
	out := map[string]interface{}{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result}
	if len(id) == 0 {
		out["id"] = nil
	}
	b, _ := json.Marshal(out)
	fmt.Fprintln(w, string(b))
}

func writeError(w io.Writer, id json.RawMessage, code int, message string) {
	out := map[string]interface{}{"jsonrpc": "2.0", "id": json.RawMessage(id),
		"error": map[string]interface{}{"code": code, "message": message}}
	if len(id) == 0 {
		out["id"] = nil
	}
	b, _ := json.Marshal(out)
	fmt.Fprintln(w, string(b))
}
