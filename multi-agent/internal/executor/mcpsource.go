package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
)

type Observer interface {
	Emit(observer.Event)
}

type buildSpec = buildspec.Spec

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
