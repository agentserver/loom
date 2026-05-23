package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type permissionsExecutor struct {
	store   agentbackend.PermissionsStore
	refresh func(context.Context, string) error
}

func newPermissionsExecutor(s agentbackend.PermissionsStore, refresh func(context.Context, string) error) *permissionsExecutor {
	return &permissionsExecutor{store: s, refresh: refresh}
}

type permRequest struct {
	Op string `json:"op"`
	agentbackend.Patch
}

func (e *permissionsExecutor) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	defer sink.Close()
	var req permRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return executor.Result{}, fmt.Errorf("permissions prompt must be JSON: %w", err)
	}
	var (
		state agentbackend.State
		err   error
	)
	switch req.Op {
	case "get":
		state, err = e.store.Get(ctx)
	case "patch":
		state, err = e.store.Patch(ctx, req.Patch)
		if err == nil && e.refresh != nil {
			err = e.refresh(ctx, "permission update")
		}
	default:
		return executor.Result{}, fmt.Errorf("unsupported permissions op %q", req.Op)
	}
	if err != nil {
		return executor.Result{}, err
	}
	body, _ := json.Marshal(state)
	sink.Write("chunk", string(body))
	return executor.Result{Summary: string(body)}, nil
}
