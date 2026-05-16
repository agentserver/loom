package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type rpcClient struct {
	stdin  io.WriteCloser
	stdout *bufio.Reader
	nextID int
}

func newRPC(stdin io.WriteCloser, stdout io.Reader) *rpcClient {
	return &rpcClient{stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1024*1024)}
}

func (c *rpcClient) call(method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
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
			return nil, fmt.Errorf("read response: %w", err)
		}
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *rpcClient) callTool(name string, args any) (json.RawMessage, error) {
	res, err := c.call("tools/call", map[string]any{"name": name, "arguments": args})
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
		return nil, fmt.Errorf("empty tool response")
	}
	return json.RawMessage(env.Content[0].Text), nil
}

func main() {
	timeoutSec := flag.Int("timeout-sec", 900, "wait_task timeout")
	promptMode := flag.String("prompt-mode", "build", "build|reuse")
	flag.Parse()

	cmd := exec.Command("docker", "run", "--rm", "-i", "--network", "host",
		"-v", "/tmp/multi-agent-online-e2e:/e2e",
		"-e", "ANTHROPIC_BASE_URL", "-e", "ANTHROPIC_API_KEY",
		"multi-agent-e2e-runtime:latest",
		"/e2e/driver-agent", "serve-mcp", "--config", "/e2e/driver.yaml")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		die("start driver container: " + err.Error())
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	rpc := newRPC(stdin, stdout)
	if _, err := rpc.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "online-dynamic-mcp-e2e", "version": "0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		die("initialize: " + err.Error())
	}

	promptLines := []string{
		"Analyze orders.csv against refund-policy.md and write outputs/refund-risk-report.md.",
		"",
	}
	switch *promptMode {
	case "reuse":
		promptLines = append(promptLines,
			"Prefer already-advertised MCP tools such as csv_profiler and refund_policy_checker.",
			"Do not build a new MCP service unless no existing advertised MCP tool can read the CSV or policy.",
		)
	default:
		promptLines = append(promptLines,
			"No available agent currently advertises a CSV profiling MCP tool or a refund policy checking MCP tool.",
			"Build missing MCP services in parallel when independent, then use the built tools to produce a concise markdown report with:",
		)
	}
	promptLines = append(promptLines,
		"- high-risk refund requests,",
		"- policy rule references,",
		"- recommended review queue,",
		"- a short note that generated MCP code is persisted but hidden unless requested.",
	)
	prompt := strings.Join(promptLines, "\n")

	subRes, err := rpc.callTool("submit_task", map[string]any{
		"prompt":              prompt,
		"read_paths":          []string{"/e2e/driver-files/orders.csv", "/e2e/driver-files/refund-policy.md"},
		"write_paths":         []map[string]any{{"path": "/e2e/outputs/refund-risk-report.md", "overwrite": true}},
		"timeout_sec":         *timeoutSec,
		"target_display_name": "master-online-e2e",
	})
	if err != nil {
		die("submit_task: " + err.Error())
	}
	var sub struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(subRes, &sub); err != nil || sub.TaskID == "" {
		die("bad submit response: " + string(subRes))
	}
	fmt.Println("TASK_ID=" + sub.TaskID)

	var waitRes json.RawMessage
	deadline := time.Now().Add(time.Duration(*timeoutSec) * time.Second)
	for {
		waitRes, err = rpc.callTool("wait_task", map[string]any{
			"task_id":           sub.TaskID,
			"poll_interval_sec": 5,
			"timeout_sec":       *timeoutSec,
		})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			die("wait_task: " + err.Error())
		}
		fmt.Fprintln(os.Stderr, "wait_task retry after transient error:", err)
		time.Sleep(5 * time.Second)
	}
	var wait struct {
		Status        string `json:"status"`
		FailureReason string `json:"failure_reason"`
		FinalOutput   string `json:"final_output"`
		WrittenFiles  []struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		} `json:"written_files"`
	}
	if err := json.Unmarshal(waitRes, &wait); err != nil {
		die("bad wait response: " + err.Error() + ": " + string(waitRes))
	}
	fmt.Println("STATUS=" + wait.Status)
	if wait.FailureReason != "" {
		fmt.Println("FAILURE_REASON=" + wait.FailureReason)
	}
	fmt.Printf("WRITTEN_FILES=%+v\n", wait.WrittenFiles)
	if wait.FinalOutput != "" {
		fmt.Println("FINAL_OUTPUT=" + wait.FinalOutput)
	}
	if wait.Status != "completed" {
		os.Exit(2)
	}
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "online dynamic MCP e2e FAIL:", msg)
	os.Exit(1)
}
