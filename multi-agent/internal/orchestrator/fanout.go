package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
)

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

	type done struct{ FinishedNode }
	doneCh := make(chan done, len(plan))
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
	for _, n := range plan {
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
