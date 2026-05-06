package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/store"
)

func (o *Orchestrator) runRoute(ctx context.Context, t executor.Task) (executor.Result, error) {
	agents, err := o.discoverFiltered(ctx)
	if err != nil {
		return executor.Result{}, err
	}
	target, err := o.planner.Route(ctx, t.Prompt, agents)
	if err != nil {
		return executor.Result{}, fmt.Errorf("planner.Route: %w", err)
	}
	if target == "" {
		return executor.Result{}, fmt.Errorf("no candidate")
	}

	if err := o.store.InsertSubTasks(t.ID, []store.SubTaskRow{
		{ParentID: t.ID, NodeID: "root", TargetID: target, Prompt: t.Prompt, Status: "assigned"},
	}); err != nil {
		return executor.Result{}, err
	}

	resp, err := o.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       target,
		Prompt:         t.Prompt,
		TimeoutSeconds: t.TimeoutSec,
	})
	if err != nil {
		_ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
			"status": "failed", "error": err.Error(),
		})
		return executor.Result{}, fmt.Errorf("delegate: %w", err)
	}
	_ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
		"child_task_id": resp.TaskID,
		"started_at":    time.Now().UTC().Format(time.RFC3339Nano),
	})

	info, err := o.sdk.WaitForTask(ctx, resp.TaskID, 5*time.Second)
	if err != nil {
		_ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
			"status": "failed", "error": err.Error(),
		})
		return executor.Result{}, fmt.Errorf("wait: %w", err)
	}
	_ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
		"status":      info.Status,
		"output":      info.Output,
		"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if info.Status != "completed" {
		return executor.Result{}, fmt.Errorf("child %s: status=%s", resp.TaskID, info.Status)
	}
	return executor.Result{Summary: info.Output}, nil
}
