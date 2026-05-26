package agentbackend

import (
	"context"

	"github.com/yourorg/multi-agent/internal/executor"
)

type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
)

// Re-exports so callers can depend on agentbackend only.
type (
	Task   = executor.Task
	Sink   = executor.Sink
	Result = executor.Result
)

type Backend interface {
	Kind() Kind
	Run(ctx context.Context, t Task, sink Sink) (Result, error)
	RunResume(ctx context.Context, sessionID, answer string, sink Sink) (Result, error)
	LLM() LLMRunner
	Permissions() PermissionsStore
	Detect(ctx context.Context) error
}

type LLMRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

type PermissionsStore interface {
	Get(ctx context.Context) (State, error)
	Patch(ctx context.Context, p Patch) (State, error)
}

type State struct {
	Backend Kind     `json:"backend"`
	Path    string   `json:"path"`
	Mode    string   `json:"mode,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	Deny    []string `json:"deny,omitempty"`
}

type Patch struct {
	Presets     []string `json:"presets,omitempty"`
	AllowAdd    []string `json:"allow_add,omitempty"`
	AllowRemove []string `json:"allow_remove,omitempty"`
	DenyAdd     []string `json:"deny_add,omitempty"`
	DenyRemove  []string `json:"deny_remove,omitempty"`
	Mode        string   `json:"mode,omitempty"`
}
