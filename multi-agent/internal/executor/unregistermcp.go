package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// UnregisterMCPConfig wires UnregisterMCPExecutor to its slave-side
// dependencies. Symmetric to RegisterMCPConfig.
type UnregisterMCPConfig struct {
	WorkDir   string
	MCPExec   *MCPExecutor
	Republish func(ctx context.Context) error
	Observer  Observer
}

// UnregisterMCPExecutor removes a dynamic MCP server: drops it from
// dynamic_mcp.yaml, kills its stdio subprocess, refreshes capabilities,
// and emits an observer event. Source files under generated_mcp/ are NOT
// deleted (callers wanting that should use bash explicitly).
type UnregisterMCPExecutor struct {
	cfg UnregisterMCPConfig
}

func NewUnregisterMCPExecutor(cfg UnregisterMCPConfig) *UnregisterMCPExecutor {
	return &UnregisterMCPExecutor{cfg: cfg}
}

func (e *UnregisterMCPExecutor) emit(ev observer.Event) {
	if e.cfg.Observer == nil {
		return
	}
	defer func() { _ = recover() }()
	e.cfg.Observer.Emit(ev)
}

type unregisterMCPPrompt struct {
	Name      string `json:"name"`
	IfPresent bool   `json:"if_present"`
}

// Run executes the unregister_mcp skill. The task prompt must be JSON
// with field "name" (required) and optional "if_present" (default false).
// When if_present is false (strict, the default), the call errors if no
// matching entry exists in dynamic_mcp.yaml.
func (e *UnregisterMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()

	var p unregisterMCPPrompt
	if err := json.Unmarshal([]byte(t.Prompt), &p); err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("unregister_mcp prompt must be JSON: %w", err), observerstore.FailContractViolation)
	}
	if p.Name == "" {
		return Result{}, observerstore.Categorize(fmt.Errorf("unregister_mcp: name is required"), observerstore.FailContractViolation)
	}

	yamlPath := DynamicYAMLPath(e.cfg.WorkDir)
	_, present := LookupDynamicEntry(yamlPath, p.Name)
	if !present {
		if !p.IfPresent {
			return Result{}, observerstore.Categorize(fmt.Errorf("unregister_mcp: not registered: %s", p.Name), observerstore.FailStaleCapability)
		}
		handle := handleJSON{
			Type: "mcp_unregistered",
			Meta: map[string]string{"name": p.Name, "removed": "false"},
		}
		return Result{Summary: handle.Marshal()}, nil
	}

	if err := e.cfg.MCPExec.UnregisterStdio(p.Name); err != nil && !errors.Is(err, ErrMCPNotRegistered) {
		return Result{}, observerstore.Categorize(fmt.Errorf("unregister_mcp: kill stdio: %w", err), observerstore.FailUnknown)
	}

	removed, err := RemoveDynamicYAML(yamlPath, p.Name)
	if err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("unregister_mcp: persist: %w", err), observerstore.FailUnknown)
	}
	if !removed {
		sink.Write("warn", fmt.Sprintf("unregister_mcp: yaml entry %q vanished between lookup and remove", p.Name))
	}

	if e.cfg.Republish != nil {
		if err := e.cfg.Republish(ctx); err != nil {
			sink.Write("warn", fmt.Sprintf("unregister_mcp: republish: %v", err))
		}
	}

	e.emit(observer.Event{
		Type:          observer.EventMCPServerRemoved,
		TaskID:        t.ID,
		MCPServerName: p.Name,
		Status:        "completed",
	})

	handle := handleJSON{
		Type: "mcp_unregistered",
		Meta: map[string]string{"name": p.Name, "removed": "true"},
	}
	return Result{Summary: handle.Marshal()}, nil
}
