package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/yourorg/multi-agent/internal/claudeperm"
)

type ClaudePermissionsConfig struct {
	WorkDir string
	Refresh func(context.Context, string) error
}

type ClaudePermissionsExecutor struct {
	cfg ClaudePermissionsConfig
}

type claudePermissionsRequest struct {
	Op string `json:"op"`
	claudeperm.Patch
}

func NewClaudePermissionsExecutor(cfg ClaudePermissionsConfig) *ClaudePermissionsExecutor {
	return &ClaudePermissionsExecutor{cfg: cfg}
}

func (e *ClaudePermissionsExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req claudePermissionsRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("claude_permissions prompt must be JSON: %w", err)
	}
	workdir := e.cfg.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	store := claudeperm.NewStore(workdir)
	var state claudeperm.State
	var err error
	switch req.Op {
	case "get":
		state, err = store.Read()
	case "patch":
		state, err = store.Patch(req.Patch)
		if err == nil && e.cfg.Refresh != nil {
			err = e.cfg.Refresh(ctx, "claude permission update")
		}
	default:
		return Result{}, fmt.Errorf("unsupported claude_permissions op %q", req.Op)
	}
	if err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return Result{}, err
	}
	sink.Write("chunk", string(body))
	return Result{Summary: string(body)}, nil
}
