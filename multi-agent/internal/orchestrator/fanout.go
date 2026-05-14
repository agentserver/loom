package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
)

// maxBuildIterations bounds how many build_mcp_blocked → replan cycles the
// orchestrator will tolerate per spec name before failing the master task.
// Hardcoded for v1; documented as a future-extension in
// docs/superpowers/specs/2026-05-09-dynamic-mcp-design.md.
const maxBuildIterations = 3

const fanoutFailureDrainTimeout = 5 * time.Second

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

// renamePlanIDs rewrites every node id in a freshly-planned slice to a
// unique form (parentNodeID + "_" + originalID) and updates intra-plan
// DependsOn references to match. Used by runFanout's phase-boundary
// handler so a re-called planner that emits "n1" again doesn't collide
// with already-running nodes in the scheduler. References to ids OUTSIDE
// this plan (e.g., the parent build node) are left unchanged.
func renamePlanIDs(nodes []planner.Node, parentNodeID string) []planner.Node {
	rename := make(map[string]string, len(nodes))
	for _, n := range nodes {
		rename[n.ID] = parentNodeID + "_" + n.ID
	}
	out := make([]planner.Node, len(nodes))
	for i, n := range nodes {
		n.ID = rename[n.ID]
		newDeps := make([]string, len(n.DependsOn))
		for j, d := range n.DependsOn {
			if newD, ok := rename[d]; ok {
				newDeps[j] = newD
			} else {
				newDeps[j] = d // refers to a pre-existing node
			}
		}
		n.DependsOn = newDeps
		// Also rewrite {{X.output}} template references in the prompt
		// for the same reason.
		for orig, renamed := range rename {
			n.Prompt = strings.ReplaceAll(n.Prompt,
				"{{"+orig+".output}}",
				"{{"+renamed+".output}}")
		}
		out[i] = n
	}
	return out
}

func validateMCPNode(n planner.Node, agents []agentsdk.AgentCard, prompt string) error {
	if n.Skill != "mcp" {
		return nil
	}

	var call struct {
		Server string                 `json:"server"`
		Tool   string                 `json:"tool"`
		Args   map[string]interface{} `json:"args"`
	}
	if err := json.Unmarshal([]byte(prompt), &call); err != nil {
		return fmt.Errorf("mcp node %s prompt must be JSON with server/tool/args: %w", n.ID, err)
	}
	if call.Server == "" {
		return fmt.Errorf("mcp node %s missing server", n.ID)
	}
	if call.Tool == "" {
		return fmt.Errorf("mcp node %s missing tool", n.ID)
	}
	if call.Args == nil {
		call.Args = map[string]interface{}{}
	}

	var target *agentsdk.AgentCard
	for i := range agents {
		if agents[i].AgentID == n.TargetID {
			target = &agents[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("mcp node %s target agent %q not found", n.ID, n.TargetID)
	}

	mcpTools, flatTools := capability.ExtractFromAgentCard(target.Card)
	if desc, ok := capability.FindTool(mcpTools, call.Server, call.Tool); ok {
		if err := capability.ValidateArgs(desc.InputSchema, call.Args); err != nil {
			return fmt.Errorf("mcp node %s invalid args for %s/%s: %w", n.ID, call.Server, call.Tool, err)
		}
		return nil
	}
	for _, tool := range flatTools {
		if tool == call.Tool {
			return nil
		}
	}

	return fmt.Errorf("mcp node %s target agent %q does not advertise tool %s/%s", n.ID, n.TargetID, call.Server, call.Tool)
}

func mcpValidationReplanContext(n planner.Node, agents []agentsdk.AgentCard, prompt string, validationErr error) string {
	var call struct {
		Server string                 `json:"server"`
		Tool   string                 `json:"tool"`
		Args   map[string]interface{} `json:"args"`
	}
	_ = json.Unmarshal([]byte(prompt), &call)

	schema := ""
	for i := range agents {
		if agents[i].AgentID != n.TargetID {
			continue
		}
		tools, _ := capability.ExtractFromAgentCard(agents[i].Card)
		if desc, ok := capability.FindTool(tools, call.Server, call.Tool); ok && len(desc.InputSchema) > 0 {
			schema = string(desc.InputSchema)
		}
		break
	}
	if schema == "" {
		schema = "unavailable"
	}
	argsJSON, _ := json.Marshal(call.Args)

	return fmt.Sprintf(
		"MCP_CALL_VALIDATION_FAILED:\nnode_id=%s\nserver=%s\ntool=%s\nargs=%s\nerror=%s\ninput_schema=%s\n"+
			"Replan with schema-conformant arguments, or add/evolve a build_mcp node if the needed argument is not in the schema.",
		n.ID, call.Server, call.Tool, string(argsJSON), validationErr.Error(), schema)
}

func (o *Orchestrator) runFanout(ctx context.Context, t executor.Task) (executor.Result, error) {
	tc, prompt, err := contractPolicyFromPrompt(t.Prompt)
	if err != nil {
		return executor.Result{}, err
	}
	if tc.Version != 0 {
		t.Prompt = prompt
	}

	agents, err := o.discoverFiltered(ctx)
	if err != nil {
		return executor.Result{}, err
	}
	planningProgress := func(ctx context.Context, phase, message string, elapsed time.Duration) {
		o.emit(observer.Event{
			Type:   observer.EventMasterPlanningProgress,
			TaskID: t.ID,
			Status: "running",
			Payload: observerPayload(map[string]interface{}{
				"phase":      phase,
				"message":    message,
				"elapsed_ms": elapsed.Milliseconds(),
				"is_final":   false,
			}),
		})
	}
	planWithProgress := func(ctx context.Context, prompt string, agents []agentsdk.AgentCard) ([]planner.Node, error) {
		plan, err := o.planner.WithProgress(planningProgress).Plan(ctx, prompt, agents)
		if err != nil {
			return nil, err
		}
		return plan, nil
	}
	emitPlanningCompleted := func(plan []planner.Node) {
		o.emit(observer.Event{
			Type:   observer.EventMasterPlanningCompleted,
			TaskID: t.ID,
			Status: "completed",
			Payload: observerPayload(map[string]interface{}{
				"phase":      "planning",
				"message":    "planning completed",
				"is_final":   false,
				"node_count": len(plan),
			}),
		})
	}
	plan, err := planWithProgress(ctx, t.Prompt, agents)
	if err != nil {
		return executor.Result{}, fmt.Errorf("planner.Plan: %w", err)
	}
	if err := validatePlanForContract(plan, tc, agents); err != nil {
		return executor.Result{}, fmt.Errorf("invalid plan: %w", err)
	}
	emitPlanningCompleted(plan)
	nodeIDs := make([]string, len(plan))
	for i, n := range plan {
		nodeIDs[i] = n.ID
	}
	o.emit(observer.Event{
		Type:    observer.EventMasterPlanCreated,
		TaskID:  t.ID,
		Summary: observer.SummarizePrompt(t.Prompt, 80),
		Status:  "created",
		Payload: observerPayload(map[string][]string{"node_ids": nodeIDs}),
	})

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
	sched := NewScheduler(plan, effectiveFanoutConcurrency(o.cfg.MaxConcurrency, tc.ExecutionPolicy, tc.Version != 0))
	outputs := map[string]string{}
	var outputsMu sync.Mutex
	iterCount := map[string]int{}
	validationReplans := 0
	// allNodes tracks every node ever scheduled, including ones added via Append.
	allNodes := make([]planner.Node, len(plan))
	copy(allNodes, plan)
	optionalByID := make(map[string]bool, len(plan))
	for _, n := range plan {
		optionalByID[n.ID] = n.Optional
	}

	type done struct {
		FinishedNode
		ChildTaskID string
	}
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
	childTaskIDs := map[string]string{}
	recordedSkipped := map[string]bool{}
	nodeByID := func(id string) planner.Node {
		for _, n := range allNodes {
			if n.ID == id {
				return n
			}
		}
		return planner.Node{ID: id}
	}
	emitRequiredNodeFailed := func(fn FinishedNode, parentFailureReason string) {
		n := nodeByID(fn.NodeID)
		payload := map[string]interface{}{
			"required": true,
			"node_id":  fn.NodeID,
			"status":   fn.Status,
		}
		if fn.Error != "" {
			payload["error"] = fn.Error
		}
		if parentFailureReason != "" {
			payload["parent_failure_reason"] = parentFailureReason
		}
		o.emit(observer.Event{
			Type:          observer.EventMasterRequiredNodeFailed,
			TaskID:        t.ID,
			SubtaskID:     fn.NodeID,
			ChildTaskID:   childTaskIDs[fn.NodeID],
			Status:        fn.Status,
			TargetAgentID: n.TargetID,
			TargetRole:    observer.RoleSlave,
			Payload:       observerPayload(payload),
		})
	}
	recordTerminalDone := func(d done) {
		if d.ChildTaskID != "" {
			childTaskIDs[d.NodeID] = d.ChildTaskID
		}
		if d.Status == "completed" {
			outputsMu.Lock()
			outputs[d.NodeID] = d.Output
			outputsMu.Unlock()
			_ = o.store.UpdateSubTask(t.ID, d.NodeID, map[string]interface{}{
				"status": "completed", "output": d.Output,
				"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
			})
			sseSink.Write("subtask_done", fmt.Sprintf(`{"node_id":%q,"status":"completed","output_len":%d}`, d.NodeID, len(d.Output)))
			o.emit(observer.Event{
				Type:        observer.EventMasterSubtaskDone,
				TaskID:      t.ID,
				SubtaskID:   d.NodeID,
				ChildTaskID: d.ChildTaskID,
				Status:      "completed",
				Payload:     observerPayload(map[string]string{"output": d.Output}),
			})
			return
		}
		_ = o.store.UpdateSubTask(t.ID, d.NodeID, map[string]interface{}{
			"status": d.Status, "error": d.Error,
			"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
		sseSink.Write("subtask_done", fmt.Sprintf(`{"node_id":%q,"status":%q}`, d.NodeID, d.Status))
		o.emit(observer.Event{
			Type:        observer.EventMasterSubtaskDone,
			TaskID:      t.ID,
			SubtaskID:   d.NodeID,
			ChildTaskID: d.ChildTaskID,
			Status:      d.Status,
			Payload:     observerPayload(map[string]string{"error": d.Error}),
		})
	}
	recordSkippedDone := func(fn FinishedNode) {
		if recordedSkipped[fn.NodeID] {
			return
		}
		recordedSkipped[fn.NodeID] = true
		_ = o.store.UpdateSubTask(t.ID, fn.NodeID, map[string]interface{}{
			"status": "skipped", "error": fn.Error,
			"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
		sseSink.Write("subtask_skipped", fmt.Sprintf(`{"node_id":%q,"reason":%q}`, fn.NodeID, fn.Error))
		o.emit(observer.Event{
			Type:        observer.EventMasterSubtaskDone,
			TaskID:      t.ID,
			SubtaskID:   fn.NodeID,
			ChildTaskID: childTaskIDs[fn.NodeID],
			Status:      "skipped",
			Payload:     observerPayload(map[string]string{"error": fn.Error}),
		})
	}
	recordSkippedFinished := func() *FinishedNode {
		var requiredSkipped *FinishedNode
		for _, fn := range sched.AllFinished() {
			if fn.Status != "skipped" {
				continue
			}
			recordSkippedDone(fn)
			if !optionalByID[fn.NodeID] && requiredSkipped == nil {
				fnCopy := fn
				requiredSkipped = &fnCopy
			}
		}
		return requiredSkipped
	}
	drainInFlight := func() {
		if inFlight == 0 {
			return
		}
		drainDeadline := time.After(fanoutFailureDrainTimeout)
		for inFlight > 0 {
			select {
			case drained := <-doneCh:
				inFlight--
				sched.Report(drained.NodeID, drained.Status, drained.Output, drained.Error)
				recordTerminalDone(drained)
				recordSkippedFinished()
			case <-ctx.Done():
				return
			case <-drainDeadline:
				return
			}
		}
	}
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
				SystemContext:  n.SystemContext,
				TimeoutSeconds: o.cfg.SubTaskDefaults.TimeoutSec,
			})
			if err != nil {
				doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: err.Error()}}
				return
			}
			_ = o.store.UpdateSubTask(t.ID, n.ID, map[string]interface{}{"child_task_id": resp.TaskID})
			o.emit(observer.Event{
				Type:           observer.EventMasterSubtaskDispatched,
				TaskID:         t.ID,
				Summary:        observer.SummarizePrompt(t.Prompt, 80),
				SubtaskID:      n.ID,
				ChildTaskID:    resp.TaskID,
				SubtaskSummary: observer.SummarizePrompt(prompt, 80),
				Status:         "assigned",
				TargetAgentID:  n.TargetID,
				TargetRole:     observer.RoleSlave,
			})
			info, err := o.sdk.WaitForTask(fanoutCtx, resp.TaskID, 5*time.Second)
			if err != nil {
				doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: err.Error()}, ChildTaskID: resp.TaskID}
				return
			}
			f := FinishedNode{NodeID: n.ID, Status: info.Status, Output: info.Output}
			if info.Status != "completed" {
				f.Error = info.FailureReason
				if f.Error == "" {
					f.Error = info.Status
				}
			}
			doneCh <- done{FinishedNode: f, ChildTaskID: resp.TaskID}
		}(n, prompt)
	}

	for !sched.Done() {
		for _, n := range sched.Ready() {
			if n.Kind == "build_mcp" || n.Skill == "build_mcp" {
				prepared, err := prepareBuildMCPNode(n)
				if err != nil {
					o.emit(observer.Event{
						Type:      observer.EventMasterBuildMCPValidationFailed,
						TaskID:    t.ID,
						SubtaskID: n.ID,
						Status:    "failed",
						Payload: observerPayload(map[string]interface{}{
							"error":    err.Error(),
							"required": !n.Optional,
							"prompt":   n.Prompt,
						}),
					})
					validationReplans++
					if validationReplans >= maxBuildIterations {
						sched.MarkDispatched(n.ID)
						inFlight++
						go func(n planner.Node, err error) {
							doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: "build_mcp spec preflight: " + err.Error()}}
						}(n, err)
						continue
					}
					ctx2 := t.Prompt + "\n\n" + buildMCPSpecInvalidReplanContext(n, err)
					newPlan, perr := planWithProgress(fanoutCtx, ctx2, agents)
					if perr != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("replan after build_mcp spec validation failure: %w", perr)
					}
					newPlan = renamePlanIDs(newPlan, n.ID)
					if err := validateAppendPlanForContract(allNodes, newPlan, tc); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("invalid build_mcp spec validation replan: %w", err)
					}
					emitPlanningCompleted(newPlan)
					if err := sched.Append(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("append build_mcp spec validation replan: %w", err)
					}
					allNodes = append(allNodes, newPlan...)
					for _, appended := range newPlan {
						optionalByID[appended.ID] = appended.Optional
					}
					appendSubTaskRows(o.store, t.ID, newPlan)
					for _, skipped := range sched.MarkSuperseded(n.ID, "superseded by build_mcp spec validation replan") {
						optionalByID[skipped.NodeID] = true
						recordSkippedDone(skipped)
					}
					continue
				}
				n = prepared
			}
			outputsMu.Lock()
			prompt, rerr := Render(n.Prompt, outputs)
			outputsMu.Unlock()
			if rerr != nil {
				sched.MarkDispatched(n.ID)
				inFlight++
				go func(n planner.Node, e error) {
					doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: e.Error()}}
				}(n, rerr)
				continue
			}
			if verr := validateMCPNode(n, agents, prompt); verr != nil {
				o.emit(observer.Event{
					Type:          observer.EventMasterMCPCallValidationFailed,
					TaskID:        t.ID,
					SubtaskID:     n.ID,
					Status:        "failed",
					TargetAgentID: n.TargetID,
					TargetRole:    observer.RoleSlave,
					Payload: observerPayload(map[string]interface{}{
						"validation_error": verr.Error(),
						"required":         !n.Optional,
						"prompt":           prompt,
					}),
				})
				validationReplans++
				if validationReplans >= maxBuildIterations {
					sched.MarkDispatched(n.ID)
					inFlight++
					go func(n planner.Node, e error) {
						doneCh <- done{FinishedNode: FinishedNode{NodeID: n.ID, Status: "failed", Error: e.Error()}}
					}(n, verr)
					continue
				}

				ctx2 := t.Prompt + "\n\n" + mcpValidationReplanContext(n, agents, prompt, verr)
				newPlan, perr := planWithProgress(fanoutCtx, ctx2, agents)
				if perr != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("replan after mcp validation failure: %w", perr)
				}
				newPlan = renamePlanIDs(newPlan, n.ID)
				if err := validateAppendPlanForContract(allNodes, newPlan, tc); err != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("invalid mcp validation replan: %w", err)
				}
				emitPlanningCompleted(newPlan)
				if err := sched.Append(newPlan); err != nil {
					cancelAll()
					return executor.Result{}, fmt.Errorf("append mcp validation replan: %w", err)
				}
				allNodes = append(allNodes, newPlan...)
				for _, appended := range newPlan {
					optionalByID[appended.ID] = appended.Optional
				}
				appendSubTaskRows(o.store, t.ID, newPlan)
				for _, skipped := range sched.MarkSuperseded(n.ID, "superseded by mcp validation replan") {
					optionalByID[skipped.NodeID] = true
					recordSkippedDone(skipped)
				}
				continue
			}
			dispatched(n, prompt)
		}
		if inFlight == 0 {
			if !sched.Done() {
				continue
			}
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
		recordTerminalDone(d)
		if d.Status == "completed" {
			if hType, hMeta, hOk := parseOutputHandle(d.Output); hOk {
				switch hType {
				case "build_mcp_blocked":
					o.emit(observer.Event{
						Type:          observer.EventMasterMCPReplan,
						TaskID:        t.ID,
						SubtaskID:     d.NodeID,
						ChildTaskID:   d.ChildTaskID,
						Status:        hType,
						MCPServerName: hMeta["spec_name"],
						Payload:       observerPayload(map[string]interface{}{"type": hType, "meta": hMeta}),
					})
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
					newPlan, perr := planWithProgress(fanoutCtx, ctx2, agents)
					if perr != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("replan after blocked: %w", perr)
					}
					newPlan = renamePlanIDs(newPlan, d.NodeID)
					if err := validateAppendPlanForContract(allNodes, newPlan, tc, agents); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("invalid replan: %w", err)
					}
					emitPlanningCompleted(newPlan)
					if err := sched.Append(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("append replan: %w", err)
					}
					allNodes = append(allNodes, newPlan...)
					for _, n := range newPlan {
						optionalByID[n.ID] = n.Optional
					}
					appendSubTaskRows(o.store, t.ID, newPlan)

				case "mcp_tool_set":
					o.emit(observer.Event{
						Type:          observer.EventMasterMCPReplan,
						TaskID:        t.ID,
						SubtaskID:     d.NodeID,
						ChildTaskID:   d.ChildTaskID,
						Status:        hType,
						MCPServerName: hMeta["name"],
						Payload:       observerPayload(map[string]interface{}{"type": hType, "meta": hMeta}),
					})
					freshAgents, derr := o.discoverFiltered(fanoutCtx)
					if derr == nil {
						agents = freshAgents
					}
					// Spell out the server/tool relationship explicitly so
					// the planner doesn't have to infer it from the raw
					// handle JSON. mcpExec routes by server name; the tools
					// list is what's INSIDE that server.
					builtMsg := fmt.Sprintf(
						"BUILT a new MCP server. Now use it via skill='mcp' with "+
							"prompt JSON {\"server\":%q,\"tool\":\"<one of: %s>\",\"args\":{...}}.",
						hMeta["name"], hMeta["tools"])
					ctx2 := t.Prompt + "\n\n" + builtMsg
					newPlan, perr := planWithProgress(fanoutCtx, ctx2, agents)
					if perr != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("replan after build: %w", perr)
					}
					if len(newPlan) == 0 {
						continue
					}
					newPlan = renamePlanIDs(newPlan, d.NodeID)
					if err := validateAppendPlanForContract(allNodes, newPlan, tc, agents); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("invalid post-build plan: %w", err)
					}
					emitPlanningCompleted(newPlan)
					if err := sched.Append(newPlan); err != nil {
						cancelAll()
						return executor.Result{}, fmt.Errorf("append post-build plan: %w", err)
					}
					allNodes = append(allNodes, newPlan...)
					for _, n := range newPlan {
						optionalByID[n.ID] = n.Optional
					}
					appendSubTaskRows(o.store, t.ID, newPlan)
				}
			}
		} else {
			if !optionalByID[d.NodeID] || policy == "all_or_nothing" {
				cancelAll()
				sched.MarkDownstreamSkipped(d.NodeID)
				requiredSkipped := recordSkippedFinished()
				drainInFlight()
				if !optionalByID[d.NodeID] {
					emitRequiredNodeFailed(d.FinishedNode, "")
					if requiredSkipped != nil {
						emitRequiredNodeFailed(*requiredSkipped, d.Error)
					}
					return executor.Result{}, fmt.Errorf("required node %s %s: %s", d.NodeID, d.Status, d.Error)
				}
				if requiredSkipped != nil {
					emitRequiredNodeFailed(*requiredSkipped, d.Error)
					return executor.Result{}, fmt.Errorf("required node %s %s: %s", requiredSkipped.NodeID, requiredSkipped.Status, requiredSkipped.Error)
				}
				return executor.Result{}, fmt.Errorf("node %s %s: %s", d.NodeID, d.Status, d.Error)
			}
			sched.MarkDownstreamSkipped(d.NodeID)
			if requiredSkipped := recordSkippedFinished(); requiredSkipped != nil {
				cancelAll()
				drainInFlight()
				emitRequiredNodeFailed(*requiredSkipped, d.Error)
				return executor.Result{}, fmt.Errorf("required node %s %s: %s", requiredSkipped.NodeID, requiredSkipped.Status, requiredSkipped.Error)
			}
		}
	}

	fins := sched.AllFinished()
	resultNodeByID := map[string]planner.Node{}
	for _, n := range allNodes {
		resultNodeByID[n.ID] = n
	}
	results := make([]planner.SubResult, len(fins))
	for i, f := range fins {
		n := resultNodeByID[f.NodeID]
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
