package humanloop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// drive feeds a sequence of JSON-RPC requests through ServeStdio and returns
// the response lines. ServeStdio reads stdin until EOF and writes responses
// to stdout, one JSON object per line.
func drive(t *testing.T, sock string, max int, lines ...string) []string {
	t.Helper()
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := ServeStdio(in, &out, sock, max); err != nil && err != io.EOF {
		t.Fatalf("ServeStdio: %v", err)
	}
	var got []string
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	return got
}

func TestServerInitializeAndToolsList(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	resps := drive(t, EndpointArg(ep), 5,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	)
	if len(resps) < 2 {
		t.Fatalf("expected ≥2 responses, got %d: %v", len(resps), resps)
	}
	// tools/list response must mention both tool names
	if !strings.Contains(resps[1], `"ask_user"`) || !strings.Contains(resps[1], `"request_permission"`) {
		t.Errorf("tools/list missing tools: %s", resps[1])
	}
}

func TestServerAskUserForwardsAndReturnsSubmitted(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	got := make(chan Payload, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			t.Errorf("Receive: %v", err)
			return
		}
		got <- p
	}()

	resps := drive(t, EndpointArg(ep), 5,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"q?","options":["a","b"]}}}`,
	)
	if len(resps) < 2 {
		t.Fatalf("got %d responses", len(resps))
	}
	last := resps[len(resps)-1]
	if !strings.Contains(last, "submitted") {
		t.Errorf("expected submitted in tool_result, got: %s", last)
	}
	p := <-got
	if p.Kind != "ask_user" || p.Question != "q?" || len(p.Options) != 2 {
		t.Errorf("unexpected payload: %+v", p)
	}
}

// Helper used by quota_test.
func mustGetText(t *testing.T, line string) string {
	t.Helper()
	var msg struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if len(msg.Result.Content) == 0 {
		return ""
	}
	return msg.Result.Content[0].Text
}
