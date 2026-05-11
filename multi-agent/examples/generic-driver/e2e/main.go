package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type rpcClient struct {
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

func newRPC(stdin io.WriteCloser, stdout io.Reader) *rpcClient {
	return &rpcClient{stdin: stdin, stdout: bufio.NewReaderSize(stdout, 64*1024)}
}

func (c *rpcClient) call(method string, params interface{}) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	req := map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": method,
	}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// callTool unwraps the {content:[{type:text,text:JSON}]} envelope to return
// the inner JSON of a tools/call response.
func (c *rpcClient) callTool(name string, args interface{}) (json.RawMessage, error) {
	res, err := c.call("tools/call", map[string]interface{}{
		"name": name, "arguments": args,
	})
	if err != nil {
		return nil, err
	}
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &env); err != nil {
		return nil, err
	}
	if len(env.Content) == 0 {
		return nil, fmt.Errorf("empty content")
	}
	return json.RawMessage(env.Content[0].Text), nil
}

func main() {
	bin := flag.String("driver-bin", "./driver-agent", "path to built driver-agent binary")
	cfg := flag.String("driver-config", "", "path to driver yaml")
	mode := flag.String("mode", "full", "smoke|full")
	flag.Parse()

	if *cfg == "" {
		die("--driver-config required")
	}

	cmd := exec.Command(*bin, "serve-mcp", "--config", *cfg)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		die("start driver: " + err.Error())
	}
	defer func() { _ = cmd.Process.Kill() }()

	rpc := newRPC(stdin, stdout)

	if _, err := rpc.call("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "e2e", "version": "0"},
		"capabilities":    map[string]interface{}{},
	}); err != nil {
		die("initialize: " + err.Error())
	}

	listed, err := rpc.call("tools/list", nil)
	if err != nil {
		die("tools/list: " + err.Error())
	}
	wantTools := []string{"list_agents", "submit_task", "get_task", "wait_task", "tail_subtasks", "cancel_task"}
	for _, w := range wantTools {
		if !strings.Contains(string(listed), `"name":"`+w+`"`) {
			die("tools/list missing " + w + ": " + string(listed))
		}
	}
	fmt.Println("ok: tools/list returned all six tools")

	if *mode == "smoke" {
		fmt.Println("smoke: PASS")
		return
	}

	// Full e2e: requires master + agent-fileconcat to be running.
	tmp, _ := os.MkdirTemp("", "genericdriver-e2e-")
	defer os.RemoveAll(tmp)
	in1 := filepath.Join(tmp, "in1.txt")
	in2 := filepath.Join(tmp, "in2.txt")
	out := filepath.Join(tmp, "out.txt")
	os.WriteFile(in1, []byte("hello"), 0o644)
	os.WriteFile(in2, []byte("world"), 0o644)

	subRes, err := rpc.callTool("submit_task", map[string]interface{}{
		"prompt":      "concatenate inputs and write to out.txt",
		"read_paths":  []string{in1, in2},
		"write_paths": []map[string]interface{}{{"path": out, "overwrite": true}},
	})
	if err != nil {
		die("submit_task: " + err.Error())
	}
	var sub struct {
		TaskID   string `json:"task_id"`
		Manifest struct {
			Files  []map[string]interface{} `json:"files"`
			Writes []map[string]interface{} `json:"writes"`
		} `json:"manifest"`
	}
	json.Unmarshal(subRes, &sub)
	if sub.TaskID == "" {
		die("no task_id: " + string(subRes))
	}
	if len(sub.Manifest.Files) != 2 || len(sub.Manifest.Writes) != 1 {
		die(fmt.Sprintf("manifest shape: %d files / %d writes", len(sub.Manifest.Files), len(sub.Manifest.Writes)))
	}
	fmt.Println("ok: submit_task returned", sub.TaskID)

	waitRes, err := rpc.callTool("wait_task", map[string]interface{}{
		"task_id": sub.TaskID, "poll_interval_sec": 2, "timeout_sec": 60,
	})
	if err != nil {
		die("wait_task: " + err.Error())
	}
	var w struct {
		Status       string `json:"status"`
		WrittenFiles []struct {
			Path   string `json:"path"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"written_files"`
	}
	json.Unmarshal(waitRes, &w)
	if w.Status != "completed" {
		die("status: " + w.Status + " (full: " + string(waitRes) + ")")
	}
	if len(w.WrittenFiles) != 1 || w.WrittenFiles[0].Bytes != 11 {
		die(fmt.Sprintf("written_files: %+v", w.WrittenFiles))
	}
	got, _ := os.ReadFile(out)
	if string(got) != "hello world" {
		die("local file content: " + string(got))
	}
	fmt.Println("ok: out.txt =", strings.TrimSpace(string(got)))

	auditPath := filepath.Join(os.Getenv("HOME"), ".cache", "multi-agent",
		readShortIDFromConfig(*cfg), "audit.log")
	if alt := os.Getenv("DRIVER_AUDIT_PATH"); alt != "" {
		auditPath = alt
	}
	auditBytes, err := os.ReadFile(auditPath)
	if err != nil {
		die("read audit: " + err.Error())
	}
	for _, want := range []string{"register_read", "register_write", "fetch_blob", "put_blob"} {
		if !bytes.Contains(auditBytes, []byte(want)) {
			die("audit log missing " + want)
		}
	}
	fmt.Println("ok: audit log records all four event types")

	fmt.Println("E2E PASS")
}

func readShortIDFromConfig(path string) string {
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "short_id:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "short_id:"))
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "e2e FAIL:", msg)
	os.Exit(1)
}
