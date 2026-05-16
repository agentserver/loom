package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/planner"
	"github.com/yourorg/multi-agent/internal/store"
)

func (o *Orchestrator) runRoute(ctx context.Context, t executor.Task) (executor.Result, error) {
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
	target := contractRouteTarget(tc, agents, o.selfID)
	if target == "" {
		var err error
		target, err = o.planner.Route(ctx, t.Prompt, agents)
		if err != nil {
			return executor.Result{}, fmt.Errorf("planner.Route: %w", err)
		}
	}
	if target == "" {
		return executor.Result{}, fmt.Errorf("no candidate")
	}
	if err := validatePlanForContract([]planner.Node{{ID: "root", TargetID: target}}, tc, agents); err != nil {
		return executor.Result{}, fmt.Errorf("invalid route target: %w", err)
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
	output := taskOutput(info)
	_ = o.store.UpdateSubTask(t.ID, "root", map[string]interface{}{
		"status":      info.Status,
		"output":      output,
		"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if info.Status != "completed" {
		return executor.Result{}, fmt.Errorf("child %s: status=%s", resp.TaskID, info.Status)
	}
	return executor.Result{Summary: output}, nil
}

func contractRouteTarget(tc contract.TaskContract, agents []agentsdk.AgentCard, selfID string) string {
	if tc.Version == 0 {
		return ""
	}
	var matches []string
	for _, agent := range agents {
		if agent.AgentID == selfID || agent.Status != "available" {
			continue
		}
		if !contractTargetAllowed(agent.AgentID, tc.ExecutionPolicy.AllowedTargets) {
			continue
		}
		if !agentHasRequiredSkills(agent, tc.CapabilityRequirements.Skills) {
			continue
		}
		matches = append(matches, agent.AgentID)
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func contractTargetAllowed(agentID string, allowedTargets []string) bool {
	if len(allowedTargets) == 0 {
		return true
	}
	for _, allowed := range allowedTargets {
		if allowed == agentID {
			return true
		}
	}
	return false
}

func agentHasRequiredSkills(agent agentsdk.AgentCard, required []string) bool {
	if len(required) == 0 {
		return true
	}
	var card struct {
		Skills []string `json:"skills"`
	}
	_ = json.Unmarshal(agent.Card, &card)
	have := make(map[string]bool, len(card.Skills))
	for _, skill := range card.Skills {
		have[skill] = true
	}
	for _, skill := range required {
		if !have[skill] {
			return false
		}
	}
	return true
}

func taskOutput(info *agentsdk.TaskInfo) string {
	if info == nil {
		return ""
	}
	if info.Output != "" {
		return info.Output
	}
	if len(info.Result) == 0 {
		return ""
	}
	var obj struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(info.Result, &obj); err == nil && obj.Output != "" {
		return obj.Output
	}
	var raw string
	if err := json.Unmarshal(info.Result, &raw); err == nil {
		return raw
	}
	return ""
}
