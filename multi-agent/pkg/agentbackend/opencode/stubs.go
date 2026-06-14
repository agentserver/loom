package opencode

import (
	"context"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// stubs.go holds placeholder types so backend.go compiles before Tasks 3-4
// land the real implementations. Deleted at the end of Task 4.

type executor struct{}

func newExecutor(_ agentbackend.Config, _ []string) *executor { return &executor{} }
func (e *executor) Run(context.Context, agentbackend.Task, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.Run not implemented (Task 4)")
}
func (e *executor) RunResume(context.Context, string, string, agentbackend.Sink) (agentbackend.Result, error) {
	panic("opencode executor.RunResume not implemented (Task 5)")
}

type Store struct{}

func NewStore(_ string) *Store { return &Store{} }
func (s *Store) Get(context.Context) (agentbackend.State, error) {
	panic("opencode Store.Get not implemented (Task 3)")
}
func (s *Store) Patch(context.Context, agentbackend.Patch) (agentbackend.State, error) {
	panic("opencode Store.Patch not implemented (Task 3)")
}

func detect(_ context.Context, _ string) error {
	panic("opencode detect not implemented (Task 3)")
}
