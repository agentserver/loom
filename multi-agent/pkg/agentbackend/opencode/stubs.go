package opencode

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// stubs.go: temporary scaffold for executor. Deleted by Task 4.

type executor struct{}

func newExecutor(_ agentbackend.Config, _ []string) *executor { return &executor{} }
func (e *executor) Run(context.Context, agentbackend.Task, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.Run not implemented (Task 4)")
}
func (e *executor) RunResume(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.RunResume not implemented (Task 5)")
}
