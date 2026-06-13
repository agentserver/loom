package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// fakePermsStore captures the last Patch call so tests can verify the
// validator gates BEFORE the persistence layer.
type fakePermsStore struct {
	state     agentbackend.State
	getErr    error
	patchErr  error
	patchSeen *agentbackend.Patch // nil if Patch never called
}

func (f *fakePermsStore) Get(_ context.Context) (agentbackend.State, error) {
	return f.state, f.getErr
}
func (f *fakePermsStore) Patch(_ context.Context, p agentbackend.Patch) (agentbackend.State, error) {
	pcopy := p
	f.patchSeen = &pcopy
	return f.state, f.patchErr
}

type noopPermsSink struct{}

func (noopPermsSink) Write(_, _ string) {}
func (noopPermsSink) Close()             {}

// TestPermissionsPatch_RejectsStarWildcard pins §1.4 #16: '*' broadens
// allow / narrows deny and must be rejected before store.Patch.
func TestPermissionsPatch_RejectsStarWildcard(t *testing.T) {
	store := &fakePermsStore{}
	e := newPermissionsExecutor(store, nil)

	cases := []struct {
		name  string
		patch agentbackend.Patch
		field string
	}{
		{"allow_add star", agentbackend.Patch{AllowAdd: []string{"*"}}, "allow_add"},
		{"deny_remove star", agentbackend.Patch{DenyRemove: []string{"*"}}, "deny_remove"},
		{"deny_add star", agentbackend.Patch{DenyAdd: []string{"foo", "*"}}, "deny_add"},
		{"presets star", agentbackend.Patch{Presets: []string{"*"}}, "presets"},
		{"allow_remove star", agentbackend.Patch{AllowRemove: []string{"*"}}, "allow_remove"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store.patchSeen = nil // reset
			req := permRequest{Op: "patch", Patch: tc.patch}
			raw, _ := json.Marshal(req)
			_, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, noopPermsSink{})
			require.Error(t, err, "must reject %s '*'", tc.field)
			require.Contains(t, strings.ToLower(err.Error()), "wildcard",
				"err should mention wildcard, got %v", err)
			require.Nil(t, store.patchSeen, "store.Patch must NOT be called on validation reject")
		})
	}
}

// TestPermissionsPatch_RejectsEmptyEntry pins §1.4 #16: empty/whitespace
// entries are ambiguous and rejected.
func TestPermissionsPatch_RejectsEmptyEntry(t *testing.T) {
	store := &fakePermsStore{}
	e := newPermissionsExecutor(store, nil)

	req := permRequest{Op: "patch", Patch: agentbackend.Patch{
		AllowAdd: []string{"Read(./*)", "  "},
	}}
	raw, _ := json.Marshal(req)
	_, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, noopPermsSink{})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "empty",
		"err should mention empty, got %v", err)
	require.Nil(t, store.patchSeen)
}

// TestPermissionsPatch_AcceptsValidPatch (positive case)
func TestPermissionsPatch_AcceptsValidPatch(t *testing.T) {
	store := &fakePermsStore{state: agentbackend.State{}}
	refreshCalled := false
	refresh := func(_ context.Context, _ string) error {
		refreshCalled = true
		return nil
	}
	e := newPermissionsExecutor(store, refresh)

	req := permRequest{Op: "patch", Patch: agentbackend.Patch{
		AllowAdd: []string{"Read(./data/*)"},
		DenyAdd:  []string{"Write(/etc/**)"},
	}}
	raw, _ := json.Marshal(req)
	_, err := e.Run(context.Background(), executor.Task{Prompt: string(raw)}, noopPermsSink{})
	require.NoError(t, err)
	require.NotNil(t, store.patchSeen, "store.Patch should have been called")
	require.Equal(t, []string{"Read(./data/*)"}, store.patchSeen.AllowAdd)
	require.True(t, refreshCalled, "refresh should fire on successful patch")
}
