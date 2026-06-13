package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// validatePermissionsPatch enforces value-level sanitation on
// agentbackend.Patch list fields before reaching store.Patch. Rejects
// '*' wildcards (would broaden allow or narrow deny silently) and
// empty/whitespace entries (ambiguous). Unknown JSON top-level fields
// are silently ignored at unmarshal time (typed struct), so no need to
// validate them here.
// Fixes §1.4 #16 of docs/review-2026-06-13.md.
func validatePermissionsPatch(p agentbackend.Patch) error {
	lists := []struct {
		name string
		list []string
	}{
		{"presets", p.Presets},
		{"allow_add", p.AllowAdd},
		{"allow_remove", p.AllowRemove},
		{"deny_add", p.DenyAdd},
		{"deny_remove", p.DenyRemove},
	}
	for _, l := range lists {
		for _, item := range l.list {
			if item == "*" {
				return fmt.Errorf("permissions patch %s contains '*' wildcard; reject", l.name)
			}
			if strings.TrimSpace(item) == "" {
				return fmt.Errorf("permissions patch %s contains empty entry; reject", l.name)
			}
		}
	}
	return nil
}

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
		if vErr := validatePermissionsPatch(req.Patch); vErr != nil {
			return executor.Result{}, vErr
		}
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
