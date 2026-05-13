package buildspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

var validName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

type Spec struct {
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	Tools             []ToolSpec `json:"tools"`
	Hints             string     `json:"hints"`
	AllowedPackages   []string   `json:"allowed_packages"`
	ComposeServers    []string   `json:"compose_servers"`
	Version           int        `json:"version"`
	Iteration         int        `json:"iteration"`
	MaxIterations     int        `json:"max_iterations"`
	PriorPath         string     `json:"prior_path,omitempty"`
	PatchInstructions string     `json:"patch_instructions,omitempty"`
}

type ToolSpec struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ArgsSchema        json.RawMessage `json:"args_schema"`
	ResultDescription string          `json:"result_description"`
}

type legacySpec struct {
	Name              string       `json:"name"`
	Description       string       `json:"description"`
	Tools             []legacyTool `json:"tools"`
	Hints             string       `json:"hints"`
	AllowedPackages   []string     `json:"allowed_packages"`
	ComposeServers    []string     `json:"compose_servers"`
	Version           int          `json:"version"`
	PriorPath         string       `json:"prior_path"`
	PatchInstructions string       `json:"patch_instructions"`
	Iteration         int          `json:"iteration"`
	MaxIterations     int          `json:"max_iterations"`
}

type legacyTool struct {
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	ArgsSchema        json.RawMessage `json:"args_schema"`
	ResultDescription string          `json:"result_description"`
}

func ParseJSON(raw string) (Spec, error) {
	var spec Spec
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("malformed build_mcp spec: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Spec{}, fmt.Errorf("malformed build_mcp spec: trailing content")
		}
		return Spec{}, fmt.Errorf("malformed build_mcp spec: %w", err)
	}
	spec = Normalize(spec)
	if err := Validate(spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func Normalize(spec Spec) Spec {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Description = strings.TrimSpace(spec.Description)
	spec.Hints = strings.TrimSpace(spec.Hints)
	spec.PriorPath = strings.TrimSpace(spec.PriorPath)
	spec.PatchInstructions = strings.TrimSpace(spec.PatchInstructions)
	if spec.Version == 0 {
		spec.Version = 1
	}
	if spec.Iteration == 0 {
		spec.Iteration = 1
	}
	if spec.MaxIterations == 0 {
		spec.MaxIterations = 3
	}
	spec.AllowedPackages = cleanList(spec.AllowedPackages)
	spec.ComposeServers = cleanList(spec.ComposeServers)
	for i := range spec.Tools {
		spec.Tools[i].Name = strings.TrimSpace(spec.Tools[i].Name)
		spec.Tools[i].Description = strings.TrimSpace(spec.Tools[i].Description)
		spec.Tools[i].ResultDescription = strings.TrimSpace(spec.Tools[i].ResultDescription)
	}
	return spec
}

func Validate(spec Spec) error {
	if !validName.MatchString(spec.Name) {
		return fmt.Errorf("invalid name %q (must match [a-z][a-z0-9_]{0,31})", spec.Name)
	}
	if spec.Version < 1 {
		return fmt.Errorf("version must be >=1")
	}
	if spec.Iteration < 1 {
		return fmt.Errorf("iteration must be >=1")
	}
	if spec.MaxIterations < 1 {
		return fmt.Errorf("max_iterations must be >=1")
	}
	if spec.Version >= 2 && spec.PriorPath == "" {
		return fmt.Errorf("prior_path required for version>=2")
	}
	if len(spec.Tools) == 0 {
		return fmt.Errorf("tools must have at least 1 entry")
	}
	for i, tool := range spec.Tools {
		if tool.Name == "" {
			return fmt.Errorf("tools[%d].name required", i)
		}
		if tool.Description == "" {
			return fmt.Errorf("tools[%d].description required", i)
		}
		if len(bytes.TrimSpace(tool.ArgsSchema)) == 0 {
			return fmt.Errorf("tools[%d].args_schema required", i)
		}
		if !json.Valid(tool.ArgsSchema) {
			return fmt.Errorf("tools[%d].args_schema must be valid JSON", i)
		}
		if tool.ResultDescription == "" {
			return fmt.Errorf("tools[%d].result_description required", i)
		}
	}
	return nil
}

func MarshalCanonical(spec Spec) (string, error) {
	spec = Normalize(spec)
	if err := Validate(spec); err != nil {
		return "", err
	}
	out, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func LegacyHashFromJSON(raw string) string {
	var spec legacySpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return ""
	}
	out, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:])
}

func cleanList(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
