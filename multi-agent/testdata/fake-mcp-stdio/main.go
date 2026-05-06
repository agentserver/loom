package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type resp struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

type toolResult struct {
	Result            interface{} `json:"result"`
	CapabilityChanged bool        `json:"capability_changed"`
	ChangeHint        string      `json:"change_hint,omitempty"`
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		var p toolCallParams
		_ = json.Unmarshal(r.Params, &p)
		out := resp{JSONRPC: "2.0", ID: r.ID}
		switch p.Name {
		case "echo":
			out.Result = toolResult{Result: p.Arguments, CapabilityChanged: false}
		case "raise":
			out.Result = toolResult{Result: "raised", CapabilityChanged: true, ChangeHint: "did the thing"}
		case "boom":
			out.Error = map[string]string{"message": "intentional failure"}
		default:
			out.Error = map[string]string{"message": "unknown tool"}
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
	}
}
