package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
)

// maxBuildIterations bounds how many build_mcp_blocked → replan cycles the
// orchestrator will tolerate per spec name before failing the master task.
// Hardcoded for v1; documented as a future-extension in
// docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md.
const maxBuildIterations = 3

// parseOutputHandle attempts to interpret s as a phase-boundary handle JSON
// document. Unlike transport.ParseHandle, it does not require a non-empty URL
// field — build_mcp_blocked outputs have url:"" by convention.
func parseOutputHandle(s string) (typ string, meta map[string]string, ok bool) {
	var h struct {
		Type string            `json:"type"`
		Meta map[string]string `json:"meta,omitempty"`
	}
	if err := json.Unmarshal([]byte(s), &h); err != nil {
		return "", nil, false
	}
	if h.Type == "" {
		return "", nil, false
	}
	return h.Type, h.Meta, true
}

// appendSubTaskRows persists newly-appended nodes to the store.
func appendSubTaskRows(s *store.Store, parentID string, nodes []planner.Node) {
	rows := make([]store.SubTaskRow, len(nodes))
	for i, n := range nodes {
		rows[i] = store.SubTaskRow{
			ParentID: parentID, NodeID: n.ID, TargetID: n.TargetID,
			Prompt: n.Prompt, DependsOn: n.DependsOn, Status: "pending",
		}
	}
	_ = s.InsertSubTasks(parentID, rows)
}

func (o *Orchestrator) runFanout(ctx context.Context, t executor.Task) (executor.Result, error) {
	agents, err := o.discoverFiltered(ctx)
	if err != nil {
		return executor.Result{}, err
	}
	plan, err := o.planner.Plan(ctx, t.Prompt, agents)
	if err != nil {
		return executor.Result{}, fmt.Errorf("planner.Plan: %w", err)
	}
	if err := Validate(plan); err != nil {
		return executor.Result{}, fmt.Errorf("invalid plan: %w", err)
	}

	rows := make([]store.SubTaskRow, len(plan))
	for i, n := range plan {
		rows[i] = store.SubTaskRow{
			ParentID: t.ID, NodeID: n.ID, TargetID: n.TargetID,
			Prompt: n.Prompt, DependsOn: n.DependsOn, Status: "pending",
		}
	}
	if err := o.store.InsertSubTasks(t.ID, rows); err != nil {
		return executor.Result{}, err
	}

	policy := o.policyForSkill(t.Skill)
	sched := NewScheduler(plan, o.cfg.MaxConcurrency)
	outputs := map[string]string{}
	var outputsMu sync.Mutex
	iterCount := map[string]int{}
	// allNodes tracks every node ever scheduled, including ones added via Append.
	allNodes := make([]planner.Node, len(plan))
	copy(allNodes, plan)

	type done struct{ FinishedNode }
	// Sized for: initial plan (≤ MaxNodes) + up to maxBuildIterations replans
	// after build_mcp_blocked + one phase-2 replan after mcp_tool_set, each up
	// to MaxNodes. Bounded so the goroutine sends never block the main loop
	// even under deep negotiation; otherwise a slow drain could spuriously
	// trip the 60s no-progress guard.
	doneCh := make(chan done, MaxNodes*(maxBuildIterations+2))
	fanoutCtx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()

	sseSink := o.store.ChunkSink(t.ID)
	defer sseSink.Close()

	var inFlight int
	dispatched := func(n planner.Node, prompt string) {
		sched.MarkDispatched(n.ID)
		inFlight++
		_ = o.store.UpdateSubTask(t.ID, n.ID, map[string]interface{}{
			"status":     "assigned",
			"prompt":     prompt,
			"started_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
		sseSink.Write("subtask_dispatched", fmt.Sprintf(`{"node_id":%q,"target_id":%q}`, n.ID, n.TargetID))
		go func(n planner.Node, prompt string) {
			resp, err := o.sdk.DelegateTask(fanoutCtx, agentsdk.DelegateTaskRequest{
				TargetID:       n.TargetID,
				Skill:          n.Skill,
				Prompt:         prompt,
				TimeoutSeconds: o.cfg.SubTaskDefaults.TimeoutSec,
			})
			if err != nil {
				doneCh <- done{FinishedNode{NodeID: n.ID, Status: "failed", Error: err.Error()}}
				return
			}
			_ = o.store.UpdateSubTask(t.ID, n.ID, map[string]interface{}{"child_task_id": resp.TaskID})
			info, err := o.sdk.WaitForTask(fanoutCtx, resp.TaskID, 5*time.Second)
			if err != nil {
				doneCh <- done{FinishedNode{NodeID: n.ID, Status: "failed", Error: err.Error()}}
				return
			}
			f := FinishedNode{NodeID: n.ID, Status: info.Status, Output: info.Output}
			if info.Status != "completed" {
				f.Error = info.FailureReason
				if f.Error == "" {
					f.Error = info.Status
				}
			}
			doneCh <- done{f}
		}(n, prompt)
	}

	for !sched.Done() {
		for _, n := range sched.Ready() {
			outputsMu.Lock()
			prompt, rerr := Render(n.Prompt, outputs)
			outputsMu.Unlock()
			if rerr != nil {
				sched.MarkDispatched(n.ID)
				inFlight++
				go func(n planner.Node, e error) {
					doneCh <- done{FinishedNode{NodeID: n.ID, Status: "failed", Error: e.Error()}}
				}(n, rerr)
				continue
			}
			dispatched(n, prompt)
		}
		if inFlight == 0 {
			break
		}
		var d done
		select {
		case d = <-doneCh:
		case <-time.After(60 * time.Second):
			cancelAll()
			// best-effort drain (give goroutines a chance to write after ctx cancel)
			drainDeadline := time.After(5 * time.Second)
			for inFlight > 0 {
				select {
				case <-doneCh:
					inFlight--
				case <-drainDeadline:
					inFlight = 0 // give up draining
				}
			}
			return executor.Result{}, fmt.Errorf("scheduler stuck: no progress in 60s")
		}
		inFlight--
		sched.Report(d.NodeID, d.Status, d.Output, d.Error)
		if d.Status == "completed" {
			outputsMu.Lock()
			outputs[d.NodeID] = d.Output
			outputsMu.Unlock()
			_ = o.store.UpdateSubTask(t.ID, d.NodeID, map[string]interface{}{
				"status": "completed", "output": d.Output,
				"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
			})
			sseSink.Write("subtask_done", fmt.Sprintf(`{"node_id":%q,"status":"completed","output_len":%d}`, d.NodeID, len(d.Output)))

			if hType, hMeta, hOk := parseOutputHandle(d.Output); hOk {
				switch hType {
				case "build_mcp_blocked":
					specName := hMeta["spec_name"]
					iterCount[specName]++
					if iterCount[specName] >= maxBuildIterations {
						cancelAll()
						return executor.Result{}, fmt.Errorf(
							"build_mcp '%s' exhausted %d iterations; last need=%q reason=%q",
							specName, maxBuildIterations,
							hMeta["needed_packages"], hMeta["reason"])
					}
					ctx2 := t.Prompt + "\n\nBUILD_MCP_BLOCKED: " + d.Output
					newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
					if perr != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("replan after blocked: %w", perr)
					}
					if err := Validate(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("invalid replan: %w", err)
					}
					if err := sched.Append(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("append replan: %w", err)
					}
					allNodes = append(allNodes, newPlan...)
					appendSubTaskRows(o.store, t.ID, newPlan)

				case "mcp_tool_set":
					freshAgents, derr := o.discoverFiltered(fanoutCtx)
					if derr == nil {
						agents = freshAgents
					}
					ctx2 := t.Prompt + "\n\nBUILT: " + d.Output
					newPlan, perr := o.planner.Plan(fanoutCtx, ctx2, agents)
					if perr != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("replan after build: %w", perr)
					}
					if err := Validate(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("invalid post-build plan: %w", err)
					}
					if err := sched.Append(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("append post-build plan: %w", err)
					}
					allNodes = append(allNodes, newPlan...)
					appendSubTaskRows(o.store, t.ID, newPlan)
				}
			}
		} else {
			_ = o.store.UpdateSubTask(t.ID, d.NodeID, map[string]interface{}{
				"status": d.Status, "error": d.Error,
				"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
			})
			sseSink.Write("subtask_done", fmt.Sprintf(`{"node_id":%q,"status":%q}`, d.NodeID, d.Status))
			if policy == "all_or_nothing" {
				cancelAll()
				for inFlight > 0 {
					<-doneCh
					inFlight--
				}
				return executor.Result{}, fmt.Errorf("node %s %s: %s", d.NodeID, d.Status, d.Error)
			}
			sched.MarkDownstreamSkipped(d.NodeID)
			for _, fn := range sched.AllFinished() {
				if fn.Status == "skipped" {
					_ = o.store.UpdateSubTask(t.ID, fn.NodeID, map[string]interface{}{
						"status": "skipped", "error": fn.Error,
						"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
					})
					sseSink.Write("subtask_skipped", fmt.Sprintf(`{"node_id":%q,"reason":%q}`, fn.NodeID, fn.Error))
				}
			}
		}
	}

	fins := sched.AllFinished()
	nodeByID := map[string]planner.Node{}
	for _, n := range allNodes {
		nodeByID[n.ID] = n
	}
	results := make([]planner.SubResult, len(fins))
	for i, f := range fins {
		n := nodeByID[f.NodeID]
		results[i] = planner.SubResult{
			NodeID: f.NodeID, TargetID: n.TargetID, Prompt: n.Prompt,
			Status: f.Status, Output: f.Output, Error: f.Error,
		}
	}
	summary, rerr := o.planner.Reduce(ctx, t.Prompt, results)
	if rerr != nil {
		return executor.Result{}, fmt.Errorf("reduce: %w", rerr)
	}
	return executor.Result{Summary: summary}, nil
}
