package agentbackend

import (
	"context"
	"testing"
)

func TestInterfacesCompile(t *testing.T) {
	var _ Backend = (*nilBackend)(nil)
	var _ LLMRunner = (*nilLLM)(nil)
	var _ PermissionsStore = (*nilPerm)(nil)
}

type nilBackend struct{}

func (nilBackend) Kind() Kind                                                      { return KindClaude }
func (nilBackend) Run(_ context.Context, _ Task, _ Sink) (Result, error)          { return Result{}, nil }
func (nilBackend) LLM() LLMRunner                                                  { return nilLLM{} }
func (nilBackend) Permissions() PermissionsStore                                   { return nilPerm{} }
func (nilBackend) Detect(_ context.Context) error                                  { return nil }

type nilLLM struct{}

func (nilLLM) Run(_ context.Context, _ string) (string, error) { return "", nil }

type nilPerm struct{}

func (nilPerm) Get(_ context.Context) (State, error)            { return State{}, nil }
func (nilPerm) Patch(_ context.Context, _ Patch) (State, error) { return State{}, nil }
