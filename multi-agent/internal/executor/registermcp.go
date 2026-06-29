package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

// RegisterMCPConfig wires RegisterMCPExecutor to its slave-side dependencies.
type RegisterMCPConfig struct {
	WorkDir   string                          // slave's cwd; generated_mcp/ + dynamic_mcp.yaml live here
	MCPExec   *MCPExecutor                    // for RegisterStdio after a successful register
	Republish func(ctx context.Context) error // tunnel.PublishCard hook; called after successful register
	Observer  Observer
}

// RegisterMCPExecutor validates and registers a pre-written MCP Python file
// without calling Claude.
type RegisterMCPExecutor struct {
	cfg RegisterMCPConfig

	// Exposed for tests.
	MCPExec *MCPExecutor
}

func (e *RegisterMCPExecutor) emit(ev observer.Event) {
	if e.cfg.Observer == nil {
		return
	}
	defer func() { _ = recover() }()
	e.cfg.Observer.Emit(ev)
}

// NewRegisterMCPExecutor creates a new RegisterMCPExecutor with the given config.
func NewRegisterMCPExecutor(cfg RegisterMCPConfig) *RegisterMCPExecutor {
	return &RegisterMCPExecutor{cfg: cfg, MCPExec: cfg.MCPExec}
}

type registerMCPPrompt struct {
	Spec       buildspec.Spec `json:"spec"`
	SourcePath string         `json:"source_path"`
}

// Run executes the register_mcp skill. The task prompt must be JSON with
// fields "spec" (buildspec.Spec) and "source_path" (relative to WorkDir).
func (e *RegisterMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()

	var p registerMCPPrompt
	if err := json.Unmarshal([]byte(t.Prompt), &p); err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp prompt must be JSON: %w", err), observerstore.FailContractViolation)
	}

	p.Spec = buildspec.Normalize(p.Spec)
	if err := buildspec.Validate(p.Spec); err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: invalid spec: %w", err), observerstore.FailContractViolation)
	}

	if p.SourcePath == "" {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: source_path is required"), observerstore.FailContractViolation)
	}

	relPath := p.SourcePath
	absPath := filepath.Join(e.cfg.WorkDir, relPath)
	if !strings.HasPrefix(filepath.Clean(absPath), filepath.Clean(e.cfg.WorkDir)+string(filepath.Separator)) {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: source_path escapes workdir: %s", relPath), observerstore.FailPolicyViolation)
	}

	src, err := os.ReadFile(absPath)
	if err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: read source: %w", err), observerstore.FailMissingFile)
	}

	if err := validatePythonSyntax(string(src)); err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: syntax: %w", err), observerstore.FailContractViolation)
	}

	if bad, _ := ValidateImports(string(src), p.Spec.AllowedPackages); len(bad) > 0 {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: disallowed imports: %s", strings.Join(bad, ",")), observerstore.FailPolicyViolation)
	}

	observed, err := SmokeLaunchPython(ctx, absPath, 3*time.Second)
	if err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: smoke: %w", err), observerstore.FailUnknown)
	}

	tools := mergeMCPToolDescriptors(p.Spec, observed)

	mcpCfg := MCPServerCfg{Transport: "stdio", Command: "python3", Args: []string{absPath}}
	if err := e.cfg.MCPExec.RegisterStdio(p.Spec.Name, mcpCfg); err != nil {
		// RegisterStdio's only failure path today is "transport must be
		// stdio" — a malformed cfg, not a runtime/idempotency fault. If
		// new failure modes appear, branch here before tagging.
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: register: %w", err), observerstore.FailContractViolation)
	}

	entry := DynamicEntry{
		Name:      p.Spec.Name,
		Transport: "stdio",
		Command:   "python3",
		Args:      []string{relPath},
		Version:   p.Spec.Version,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Tools:     tools,
	}
	if err := UpsertDynamicYAML(DynamicYAMLPath(e.cfg.WorkDir), entry); err != nil {
		return Result{}, observerstore.Categorize(fmt.Errorf("register_mcp: persist: %w", err), observerstore.FailUnknown)
	}

	if e.cfg.Republish != nil {
		if err := e.cfg.Republish(ctx); err != nil {
			sink.Write("warn", fmt.Sprintf("register_mcp: republish: %v", err))
		}
	}

	toolNames := capability.FlatNames(tools)
	e.emit(observer.Event{
		Type:          observer.EventMCPServerCreated,
		TaskID:        t.ID,
		MCPServerName: p.Spec.Name,
		MCPTools:      toolNames,
		Status:        "completed",
	})

	handle := handleJSON{
		Type: "mcp_tool_set",
		URL:  "file://" + absPath,
		Meta: map[string]string{
			"name":    p.Spec.Name,
			"version": strconv.Itoa(p.Spec.Version),
			"tools":   strings.Join(capability.FlatNames(tools), ","),
		},
	}
	return Result{Summary: handle.Marshal()}, nil
}
