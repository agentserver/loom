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

const (
	runtimeRoot = "/tmp/multi-agent-driver-first-e2e"
	slaveAID    = "a4811483-6f8e-4494-a132-2da139469221"
	slaveBID    = "ab268a1b-6482-4d71-9de8-04ee6e1e3610"
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
	timeoutSec := flag.Int("timeout-sec", 1200, "overall helper timeout")
	flag.Parse()

	cmd := exec.Command("docker", "run", "--rm", "-i", "--network", "host",
		"-v", runtimeRoot+":/e2e",
		"-v", "/root/.zshrc:/root/.zshrc:ro",
		"multi-agent-e2e-runtime:latest",
		"zsh", "-lc", "cd /e2e/driver && /e2e/bin/driver-agent serve-mcp --config config.yaml")
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
		"clientInfo":      map[string]string{"name": "online-driver-first-e2e", "version": "0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		die("initialize: " + err.Error())
	}

	listed, err := rpc.call("tools/list", nil)
	if err != nil {
		die("tools/list: " + err.Error())
	}
	for _, want := range []string{"list_agents", "inspect_capabilities", "dry_run_contract", "submit_contract_task"} {
		if !strings.Contains(string(listed), `"name":"`+want+`"`) {
			die("tools/list missing " + want)
		}
	}
	fmt.Println("TOOLS_OK=list_agents,inspect_capabilities,dry_run_contract,submit_contract_task")

	agentsRaw, err := rpc.callTool("list_agents", map[string]any{})
	if err != nil {
		die("list_agents: " + err.Error())
	}
	for _, want := range []string{slaveAID, slaveBID, "slave-a-online-dag-160628", "slave-b-online-dag-160628"} {
		if !strings.Contains(string(agentsRaw), want) {
			die("list_agents missing " + want + ": " + string(agentsRaw))
		}
	}
	fmt.Println("AGENTS_OK=slave-a-online-dag-160628,slave-b-online-dag-160628")

	contract := map[string]any{
		"version":         1,
		"conversation_id": fmt.Sprintf("online-driver-first-e2e-%d", time.Now().Unix()),
		"intent": map[string]any{
			"goal": "Run a real online driver-first DAG test. The driver must use Claude Code to create exactly two independent chat DAG nodes, one for each allowed slave, then reduce their outputs.",
			"success_criteria": []string{
				"Route is driver_fanout",
				"Both slave-a-online-dag-160628 and slave-b-online-dag-160628 are delegated one chat task",
				"Final summary includes distinct outputs from both slaves",
			},
		},
		"data_contract": map[string]any{
			"read_artifacts": []any{},
			"write_targets": []map[string]any{{
				"type": "artifact",
				"kind": "report",
				"name": "driver-first-online-e2e-report.md",
			}},
		},
		"execution_policy": map[string]any{
			"routing":                               "direct_first",
			"allow_master":                          false,
			"allow_build_mcp":                       false,
			"allow_code_artifacts":                  true,
			"code_persistence":                      "observer_artifact_store",
			"expose_code_to_user":                   "on_request",
			"write_mode":                            "artifact_only",
			"max_dag_nodes":                         2,
			"max_depth":                             2,
			"max_concurrency":                       2,
			"require_user_approval_for_repo_writes": false,
			"allowed_targets": []string{
				slaveAID,
				slaveBID,
			},
		},
		"capability_requirements": map[string]any{
			"skills": []string{},
			"tools":  []string{},
		},
	}

	dryRunRaw, err := rpc.callTool("dry_run_contract", map[string]any{"contract": contract})
	if err != nil {
		die("dry_run_contract: " + err.Error())
	}
	var dryRun struct {
		Runnable         bool   `json:"runnable"`
		RecommendedRoute string `json:"recommended_route"`
	}
	if err := json.Unmarshal(dryRunRaw, &dryRun); err != nil {
		die("decode dry_run_contract: " + err.Error() + ": " + string(dryRunRaw))
	}
	fmt.Printf("DRY_RUN_ROUTE=%s RUNNABLE=%v\n", dryRun.RecommendedRoute, dryRun.Runnable)
	if !dryRun.Runnable || dryRun.RecommendedRoute != "driver_fanout" {
		die("dry_run_contract did not recommend driver_fanout: " + string(dryRunRaw))
	}

	prompt := strings.Join([]string{
		"ONLINE E2E TEST: The driver must generate the DAG itself using Claude Code.",
		"Create exactly two root chat nodes and no master node.",
		"Node A must target agent_id " + slaveAID + " (display_name slave-a-online-dag-160628). Ask it to answer with the sentence: slave-a-online-dag-160628 completed its direct driver-first DAG task.",
		"Node B must target agent_id " + slaveBID + " (display_name slave-b-online-dag-160628). Ask it to answer with the sentence: slave-b-online-dag-160628 completed its direct driver-first DAG task.",
		"Do not use build_mcp. Do not use skill mcp. Ordinary chat nodes only.",
		"After both nodes complete, reduce the two outputs into one concise final summary that names both display names.",
	}, "\n")

	start := time.Now()
	submitRaw, err := rpc.callTool("submit_contract_task", map[string]any{
		"contract":    contract,
		"prompt":      prompt,
		"timeout_sec": *timeoutSec,
	})
	if err != nil {
		die("submit_contract_task: " + err.Error())
	}
	elapsed := time.Since(start).Round(time.Second)
	var submit struct {
		Route   string `json:"route"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(submitRaw, &submit); err != nil {
		die("decode submit_contract_task: " + err.Error() + ": " + string(submitRaw))
	}
	fmt.Printf("SUBMIT_ROUTE=%s ELAPSED=%s\n", submit.Route, elapsed)
	fmt.Println("SUMMARY_BEGIN")
	fmt.Println(submit.Summary)
	fmt.Println("SUMMARY_END")
	if submit.Route != "driver_fanout" {
		die("submit route is not driver_fanout: " + string(submitRaw))
	}
	for _, want := range []string{"slave-a-online-dag-160628", "slave-b-online-dag-160628"} {
		if !strings.Contains(submit.Summary, want) {
			die("summary missing " + want)
		}
	}
	fmt.Println("ONLINE_DRIVER_FIRST_E2E=PASS")
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "online driver-first e2e FAIL:", msg)
	os.Exit(1)
}
