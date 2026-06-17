package agentbackend

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestInterfacesCompile(t *testing.T) {
	var _ Backend = (*nilBackend)(nil)
	var _ LLMRunner = (*nilLLM)(nil)
	var _ PermissionsStore = (*nilPerm)(nil)
}

func TestSessionMessageJSONUsesSnakeCase(t *testing.T) {
	msg := SessionMessage{
		Role: "assistant",
		Text: "hello",
		Ts:   time.Date(2026, 6, 17, 1, 2, 3, 0, time.UTC),
	}

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"role":"assistant","text":"hello","ts":"2026-06-17T01:02:03Z"}`
	if string(body) != want {
		t.Fatalf("json=%s want %s", body, want)
	}
}

type nilBackend struct{}

func (nilBackend) Kind() Kind                                            { return KindClaude }
func (nilBackend) Run(_ context.Context, _ Task, _ Sink) (Result, error) { return Result{}, nil }
func (nilBackend) RunResume(_ context.Context, _, _ string, _ Sink) (Result, error) {
	return Result{}, nil
}
func (nilBackend) LLM() LLMRunner                                    { return nilLLM{} }
func (nilBackend) Permissions() PermissionsStore                     { return nilPerm{} }
func (nilBackend) Detect(_ context.Context) error                    { return nil }
func (nilBackend) ListSessions(_ context.Context) ([]Session, error) { return nil, nil }
func (nilBackend) GetSession(_ context.Context, _ string) (Session, []SessionMessage, error) {
	return Session{}, nil, ErrSessionNotFound
}

type nilLLM struct{}

func (nilLLM) Run(_ context.Context, _ string) (string, error) { return "", nil }

type nilPerm struct{}

func (nilPerm) Get(_ context.Context) (State, error)            { return State{}, nil }
func (nilPerm) Patch(_ context.Context, _ Patch) (State, error) { return State{}, nil }
