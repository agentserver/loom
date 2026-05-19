package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/progress"
)

type Observer interface {
	Emit(observer.Event)
}

// BuildMCPConfig wires BuildMCPExecutor to its slave-side dependencies.
type BuildMCPConfig struct {
	WorkDir   string                          // slave's cwd; generated_mcp/ + dynamic_mcp.yaml live here
	ClaudeBin string                          // path to claude CLI (or fake during tests)
	MCPExec   *MCPExecutor                    // for RegisterStdio after a successful build
	Republish func(ctx context.Context) error // tunnel.PublishCard hook; called after successful register
	Observer  Observer
}

type BuildMCPExecutor struct {
	cfg BuildMCPConfig

	// Exposed for tests.
	MCPExec *MCPExecutor
}

func NewBuildMCPExecutor(cfg BuildMCPConfig) *BuildMCPExecutor {
	return &BuildMCPExecutor{cfg: cfg, MCPExec: cfg.MCPExec}
}

func (e *BuildMCPExecutor) emit(ev observer.Event) {
	if e.cfg.Observer == nil {
		return
	}
	defer func() { _ = recover() }()
	e.cfg.Observer.Emit(ev)
}

func (e *BuildMCPExecutor) progress(t Task, phase, message string, extra map[string]interface{}) {
	payload := map[string]interface{}{
		"phase":    phase,
		"message":  message,
		"is_final": false,
	}
	for k, v := range extra {
		payload[k] = v
	}
	e.emit(observer.Event{
		Type:    observer.EventSlaveBuildMCPProgress,
		TaskID:  t.ID,
		Status:  "running",
		Payload: buildMCPObserverPayload(payload),
	})
}

func buildMCPObserverPayload(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

type buildSpec = buildspec.Spec
type buildToolSpec = buildspec.ToolSpec

type legacyBuildSpec struct {
	Name              string                `json:"name"`
	Description       string                `json:"description"`
	Tools             []legacyBuildToolSpec `json:"tools"`
	Hints             string                `json:"hints"`
	AllowedPackages   []string              `json:"allowed_packages"`
	ComposeServers    []string              `json:"compose_servers"`
	Version           int                   `json:"version"`
	PriorPath         string                `json:"prior_path"`
	PatchInstructions string                `json:"patch_instructions"`
	Iteration         int                   `json:"iteration"`
	MaxIterations     int                   `json:"max_iterations"`
}

type legacyBuildToolSpec struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ArgsSchema        json.RawMessage `json:"args_schema"`
	ResultDescription string          `json:"result_description"`
}

const (
	legacyListPermutationLimit     = 6
	buildMCPLegacyHashesContextKey = "build_mcp_legacy_spec_hashes"
)

func (e *BuildMCPExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	spec, err := buildspec.ParseJSON(t.Prompt)
	if err != nil {
		return Result{}, fmt.Errorf("buildmcp: malformed spec: %w", err)
	}
	e.progress(t, "parse_spec", "parsed build_mcp spec", map[string]interface{}{"name": spec.Name})
	canonical, err := buildspec.MarshalCanonical(spec)
	if err != nil {
		return Result{}, fmt.Errorf("buildmcp: invalid spec: %w", err)
	}
	specHash := computeSpecHashFromCanonical(canonical)
	legacySpecHashes := computeLegacySpecHashes(t.Prompt, t.SystemContext, spec)

	// Idempotency: if dynamic_mcp.yaml has a matching entry that points at an
	// existing file, short-circuit.
	if existing, ok := LookupDynamicEntry(DynamicYAMLPath(e.cfg.WorkDir), spec.Name); ok && existing.Version == spec.Version && specHashMatches(existing.SpecHash, specHash, legacySpecHashes) {
		if _, err := os.Stat(filepath.Join(e.cfg.WorkDir, existing.Args[0])); err == nil {
			e.progress(t, "reuse", "reusing existing generated MCP server", map[string]interface{}{"name": spec.Name})
			return Result{Summary: e.successHandle(spec, existing.Args[0], existing.Tools, 0).Marshal()}, nil
		}
	}

	priorCode := ""
	if spec.PriorPath != "" {
		b, err := os.ReadFile(filepath.Join(e.cfg.WorkDir, spec.PriorPath))
		if err != nil {
			return Result{}, fmt.Errorf("buildmcp: read prior_path: %w", err)
		}
		priorCode = string(b)
	}

	e.progress(t, "generate", "generating MCP server source", map[string]interface{}{"name": spec.Name})
	src, err := e.invokeClaude(ctx, t, spec, priorCode)
	if err != nil {
		return Result{Summary: e.blockedHandle(t.ID, spec, "", "", "claude_invocation", err.Error())}, nil
	}
	src = stripFencesAndJunk(src)

	// Validate syntax.
	e.progress(t, "validate", "validating generated source", map[string]interface{}{"name": spec.Name})
	if err := validatePythonSyntax(src); err != nil {
		return Result{Summary: e.blockedHandle(t.ID, spec, "", "", "validate_syntax", err.Error())}, nil
	}
	// Validate imports.
	bad, _ := ValidateImports(src, spec.AllowedPackages)
	if len(bad) > 0 {
		return Result{Summary: e.blockedHandle(t.ID, spec, strings.Join(bad, ","),
			"claude used "+strings.Join(bad, ", ")+" not in allowed_packages",
			"validate_imports", "")}, nil
	}
	// Write file with header.
	relPath := filepath.Join("generated_mcp", spec.Name, fmt.Sprintf("v%d.py", spec.Version))
	absPath := filepath.Join(e.cfg.WorkDir, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return Result{}, err
	}
	header := fmt.Sprintf(`# -*- coding: utf-8 -*-
# AUTO-GENERATED by multi-agent build_mcp at %s
# spec.name=%s  version=%d  iteration=%d
# spec_hash=sha256:%s  prior_path=%s
# DO NOT HAND-EDIT. To evolve this file, send another build_mcp task
# with version=N+1, prior_path=this file path, and patch_instructions=...
#
# This file is reserved for framework-generated MCP servers.
# Hand-written MCP servers belong elsewhere (referenced via config.yaml).

`, time.Now().UTC().Format(time.RFC3339), spec.Name, spec.Version, spec.Iteration, specHash, spec.PriorPath)
	full := []byte(header + src)
	if err := os.WriteFile(absPath, full, 0o644); err != nil {
		return Result{}, err
	}
	// Smoke launch.
	e.progress(t, "smoke_launch", "smoke launching generated MCP server", map[string]interface{}{"name": spec.Name})
	tools, err := SmokeLaunchPython(ctx, absPath, 3*time.Second)
	if err != nil {
		_ = os.Remove(absPath)
		return Result{Summary: e.blockedHandle(t.ID, spec, "", err.Error(), "smoke_launch", "")}, nil
	}
	tools = mergeMCPToolDescriptors(spec, tools)
	toolNames := capability.FlatNames(tools)
	// Register.
	e.progress(t, "register", "registering generated MCP server", map[string]interface{}{"name": spec.Name})
	mcpCfg := MCPServerCfg{Transport: "stdio", Command: "python3", Args: []string{absPath}}
	if err := e.cfg.MCPExec.RegisterStdio(spec.Name, mcpCfg); err != nil {
		return Result{}, fmt.Errorf("buildmcp: register: %w", err)
	}
	// Persist to dynamic_mcp.yaml.
	if err := UpsertDynamicYAML(DynamicYAMLPath(e.cfg.WorkDir), DynamicEntry{
		Name: spec.Name, Transport: "stdio", Command: "python3",
		Args: []string{relPath}, Version: spec.Version,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SpecHash:  specHash, Tools: tools,
	}); err != nil {
		return Result{}, fmt.Errorf("buildmcp: persist yaml: %w", err)
	}
	// Re-publish card.
	if e.cfg.Republish != nil {
		e.progress(t, "republish", "republishing slave capability card", map[string]interface{}{"name": spec.Name})
		if err := e.cfg.Republish(ctx); err != nil {
			sink.Write("warn", fmt.Sprintf("republish card: %v", err))
		}
	}
	e.emit(observer.Event{
		Type:          observer.EventMCPServerCreated,
		TaskID:        t.ID,
		MCPServerName: spec.Name,
		MCPTools:      toolNames,
		Status:        "completed",
		Payload: buildMCPObserverPayload(map[string]interface{}{
			"mcp_tool_descriptors": tools,
		}),
	})
	return Result{Summary: e.successHandle(spec, relPath, tools, len(strings.Split(src, "\n"))).Marshal()}, nil
}

func (e *BuildMCPExecutor) successHandle(spec buildSpec, relPath string, tools []capability.MCPToolDescriptor, lineCount int) handleJSON {
	return handleJSON{
		Type: "mcp_tool_set",
		URL:  "file://" + filepath.Join(e.cfg.WorkDir, relPath),
		Meta: map[string]string{
			"name":      spec.Name,
			"version":   strconv.Itoa(spec.Version),
			"tools":     strings.Join(capability.FlatNames(tools), ","),
			"slave_id":  os.Getenv("SLAVE_SANDBOX_ID"),
			"lines":     strconv.Itoa(lineCount),
			"deps":      strings.Join(spec.AllowedPackages, ","),
			"iteration": strconv.Itoa(spec.Iteration),
		},
	}
}

func mergeMCPToolDescriptors(spec buildSpec, observed []capability.MCPToolDescriptor) []capability.MCPToolDescriptor {
	merged := make([]capability.MCPToolDescriptor, 0, len(spec.Tools)+len(observed))
	byName := make(map[string]int, len(spec.Tools)+len(observed))

	for _, tool := range spec.Tools {
		if tool.Name == "" {
			continue
		}
		descriptor := capability.MCPToolDescriptor{
			Server:            spec.Name,
			Name:              tool.Name,
			Description:       tool.Description,
			InputSchema:       tool.ArgsSchema,
			ResultDescription: tool.ResultDescription,
		}
		byName[descriptor.Name] = len(merged)
		merged = append(merged, descriptor)
	}

	for _, tool := range observed {
		if tool.Name == "" {
			continue
		}
		if tool.Server == "" {
			tool.Server = spec.Name
		}
		if idx, ok := byName[tool.Name]; ok {
			existing := merged[idx]
			if existing.Server == "" {
				existing.Server = tool.Server
			}
			if existing.Description == "" {
				existing.Description = tool.Description
			}
			if len(existing.InputSchema) == 0 {
				existing.InputSchema = tool.InputSchema
			}
			if existing.ResultDescription == "" {
				existing.ResultDescription = tool.ResultDescription
			}
			merged[idx] = existing
			continue
		}
		byName[tool.Name] = len(merged)
		merged = append(merged, tool)
	}

	for i := range merged {
		if merged[i].Server == "" {
			merged[i].Server = spec.Name
		}
	}
	return merged
}

func (e *BuildMCPExecutor) blockedHandle(taskID string, spec buildSpec, neededPackages, reason, stage, claudeErr string) string {
	if reason == "" && claudeErr != "" {
		reason = claudeErr
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	e.emit(observer.Event{
		Type:          observer.EventMCPServerBlocked,
		TaskID:        taskID,
		MCPServerName: spec.Name,
		Status:        "blocked",
		Payload: buildMCPObserverPayload(map[string]interface{}{
			"stage":           stage,
			"reason":          reason,
			"needed_packages": neededPackages,
			"iteration":       spec.Iteration,
		}),
	})
	h := handleJSON{
		Type: "build_mcp_blocked",
		URL:  "",
		Meta: map[string]string{
			"spec_name":       spec.Name,
			"iteration":       strconv.Itoa(spec.Iteration),
			"needed_packages": neededPackages,
			"reason":          reason,
			"stage":           stage,
		},
	}
	return h.Marshal()
}

// handleJSON is a local mirror of pkg/transport.Handle to avoid an import cycle
// between internal/executor and pkg/transport. Field names match.
type handleJSON struct {
	Type  string            `json:"type"`
	URL   string            `json:"url"`
	Bytes int64             `json:"bytes,omitempty"`
	MIME  string            `json:"mime,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

func (h handleJSON) Marshal() string {
	b, _ := json.Marshal(h)
	return string(b)
}

func (e *BuildMCPExecutor) invokeClaude(ctx context.Context, t Task, spec buildSpec, priorCode string) (string, error) {
	var src string
	commandDone := make(chan struct{})
	err := progress.RunWithHeartbeat(ctx, progress.Config{
		Interval:    20 * time.Second,
		HardTimeout: 0,
		Emit: func(ctx context.Context, elapsed time.Duration) {
			e.progress(t, "generate", "generating MCP server source", map[string]interface{}{
				"name":       spec.Name,
				"elapsed_ms": elapsed.Milliseconds(),
			})
		},
	}, func(ctx context.Context) error {
		defer close(commandDone)
		out, err := e.invokeClaudeOnce(ctx, spec, priorCode)
		if err != nil {
			return err
		}
		src = out
		return nil
	})
	<-commandDone
	if err != nil {
		return "", err
	}
	return src, nil
}

func (e *BuildMCPExecutor) invokeClaudeOnce(ctx context.Context, spec buildSpec, priorCode string) (string, error) {
	args := []string{"--print", "--output-format=stream-json", "--verbose"}
	cmd := exec.CommandContext(ctx, e.cfg.ClaudeBin, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() {
		defer stdin.Close()
		io.WriteString(stdin, buildSystemPrompt+"\n\n")
		io.WriteString(stdin, "SPEC:\n")
		b, _ := json.MarshalIndent(spec, "", "  ")
		io.WriteString(stdin, string(b))
		if priorCode != "" {
			io.WriteString(stdin, "\n\nPRIOR CODE (revise according to patch_instructions):\n")
			io.WriteString(stdin, priorCode)
		}
		io.WriteString(stdin, "\n\nRespond with python source only. No markdown, no commentary.")
	}()
	var assembled strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Type != "assistant" {
			continue
		}
		for _, c := range msg.Message.Content {
			if c.Type == "text" {
				assembled.WriteString(c.Text)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("claude exit: %v: %s", err, stderrBuf.String())
	}
	return assembled.String(), nil
}

const buildSystemPrompt = `You are generating a Python MCP (Model Context Protocol) stdio server.
Wire format:
- Read JSON-RPC requests one per line on stdin: {"jsonrpc":"2.0","id":<int>,"method":"tools/list" | "tools/call","params":{...}}
- Write JSON responses one per line to stdout: {"jsonrpc":"2.0","id":<int>,"result":{...}} or {"jsonrpc":"2.0","id":<int>,"error":{"message":"..."}}
- For "tools/list" return full tool descriptors for every spec.tools entry: {"result":{"tools":[{"name":"X","description":"...","inputSchema":{...},"result_description":"..."}]}}. Each inputSchema must match the corresponding spec.tools[].args_schema exactly.
- For "tools/call" with params {"name":"X","arguments":{...}}, dispatch on name; return {"result":{"result":<your value>,"capability_changed":false}}
- Flush after every response.

Constraints:
- Output ONLY a complete Python file. No markdown fences. No commentary. No explanation.
- The file must be a runnable program (use 'if __name__ == "__main__":' guard).
- Limit imports to the packages declared in the spec's allowed_packages plus the Python standard library.
`

func stripFencesAndJunk(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i > 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSuffix(s, "```\n")
	}
	return strings.TrimSpace(s) + "\n"
}

func validatePythonSyntax(src string) error {
	cmd := exec.Command("python3", "-c", "import ast,sys; ast.parse(sys.stdin.read())")
	cmd.Stdin = strings.NewReader(src)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ast.parse failed: %v: %s", err, errBuf.String())
	}
	return nil
}

func computeSpecHashFromCanonical(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func computeLegacySpecHashFromPrompt(raw string) string {
	return buildspec.LegacyHashFromJSON(raw)
}

func computeLegacySpecHashes(raw, systemContext string, spec buildSpec) []string {
	seen := map[string]bool{}
	hashes := []string{}
	add := func(hash string) {
		if hash == "" || seen[hash] {
			return
		}
		seen[hash] = true
		hashes = append(hashes, hash)
	}
	for _, hash := range legacyHashesFromSystemContext(systemContext) {
		add(hash)
	}
	add(computeLegacySpecHashFromPrompt(raw))

	base := legacySpecFromBuildSpec(spec)
	add(computeLegacySpecHash(base))

	versions := []int{base.Version}
	if base.Version == 1 {
		versions = append(versions, 0)
	}
	iterations := []int{base.Iteration}
	if base.Iteration == 1 {
		iterations = append(iterations, 0)
	}
	maxIterations := []int{base.MaxIterations}
	if base.MaxIterations == 3 {
		maxIterations = append(maxIterations, 0)
	}
	allowedPackages := legacyListOrderCandidates(base.AllowedPackages)
	composeServers := legacyListOrderCandidates(base.ComposeServers)

	for _, version := range versions {
		for _, iteration := range iterations {
			for _, maxIteration := range maxIterations {
				for _, allowed := range allowedPackages {
					for _, compose := range composeServers {
						candidate := base
						candidate.Version = version
						candidate.Iteration = iteration
						candidate.MaxIterations = maxIteration
						candidate.AllowedPackages = allowed
						candidate.ComposeServers = compose
						add(computeLegacySpecHash(candidate))
					}
				}
			}
		}
	}
	return hashes
}

func legacyHashesFromSystemContext(systemContext string) []string {
	if systemContext == "" {
		return nil
	}
	var payload map[string][]string
	if err := json.Unmarshal([]byte(systemContext), &payload); err != nil {
		return nil
	}
	return payload[buildMCPLegacyHashesContextKey]
}

func legacyListOrderCandidates(values []string) [][]string {
	if len(values) == 0 {
		candidates := [][]string{values}
		if values != nil {
			candidates = append(candidates, nil)
		}
		return candidates
	}
	if len(values) > legacyListPermutationLimit {
		return [][]string{values}
	}
	return permuteLegacyList(values)
}

func permuteLegacyList(values []string) [][]string {
	current := append([]string(nil), values...)
	out := [][]string{}
	var walk func(int)
	walk = func(i int) {
		if i == len(current) {
			out = append(out, append([]string(nil), current...))
			return
		}
		for j := i; j < len(current); j++ {
			current[i], current[j] = current[j], current[i]
			walk(i + 1)
			current[i], current[j] = current[j], current[i]
		}
	}
	walk(0)
	return out
}

func legacySpecFromBuildSpec(spec buildSpec) legacyBuildSpec {
	tools := make([]legacyBuildToolSpec, 0, len(spec.Tools))
	for _, tool := range spec.Tools {
		tools = append(tools, legacyBuildToolSpec{
			Name:              tool.Name,
			Description:       tool.Description,
			ArgsSchema:        tool.ArgsSchema,
			ResultDescription: tool.ResultDescription,
		})
	}
	return legacyBuildSpec{
		Name:              spec.Name,
		Description:       spec.Description,
		Tools:             tools,
		Hints:             spec.Hints,
		AllowedPackages:   spec.AllowedPackages,
		ComposeServers:    spec.ComposeServers,
		Version:           spec.Version,
		PriorPath:         spec.PriorPath,
		PatchInstructions: spec.PatchInstructions,
		Iteration:         spec.Iteration,
		MaxIterations:     spec.MaxIterations,
	}
}

func computeLegacySpecHash(spec legacyBuildSpec) string {
	b, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	return computeSpecHashFromCanonical(string(b))
}

func specHashMatches(existing, canonical string, legacy []string) bool {
	if existing == canonical {
		return true
	}
	for _, hash := range legacy {
		if existing == hash {
			return true
		}
	}
	return false
}
