package agentbackend

import (
	"testing"
)

func TestSessionRef_IsZero(t *testing.T) {
	cases := []struct {
		name string
		ref  SessionRef
		want bool
	}{
		{"empty struct", SessionRef{}, true},
		{"only kind", SessionRef{Kind: "codex"}, true},
		{"only agentID", SessionRef{AgentID: "ag-1"}, true},
		{"backend set", SessionRef{Backend: "thr-1"}, false},
		{"bridge set", SessionRef{Bridge: "cse_1"}, false},
		{"both set", SessionRef{Backend: "thr-1", Bridge: "cse_1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.IsZero(); got != tc.want {
				t.Errorf("IsZero() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionRef_HasBackend(t *testing.T) {
	if (SessionRef{}).HasBackend() {
		t.Error("zero ref should not HasBackend")
	}
	if (SessionRef{Bridge: "cse_1"}).HasBackend() {
		t.Error("bridge-only ref should not HasBackend")
	}
	if !(SessionRef{Backend: "thr-1"}).HasBackend() {
		t.Error("backend ref should HasBackend")
	}
}

func TestSessionRef_String(t *testing.T) {
	cases := []struct {
		name string
		ref  SessionRef
		want string
	}{
		{"empty", SessionRef{}, "SessionRef{}"},
		{"backend only", SessionRef{Backend: "thr-1"}, "SessionRef{backend=thr-1}"},
		{"bridge only", SessionRef{Bridge: "cse_1"}, "SessionRef{bridge=cse_1}"},
		{"both", SessionRef{Backend: "thr-1", Bridge: "cse_1"}, "SessionRef{backend=thr-1 (bridge=cse_1)}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewBackend_Success(t *testing.T) {
	// Empty kind and empty agentID are both legitimate (driver-side construction).
	cases := []struct {
		name      string
		kind      Kind
		agentID   string
		backendID string
	}{
		{"all set", "codex", "ag-1", "thr-1"},
		{"empty kind", "", "ag-1", "thr-1"},
		{"empty agentID", "codex", "", "thr-1"},
		{"empty kind and agentID", "", "", "thr-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := NewBackend(tc.kind, tc.agentID, tc.backendID)
			if ref.Backend != tc.backendID {
				t.Errorf("Backend = %q, want %q", ref.Backend, tc.backendID)
			}
			if ref.Bridge != "" {
				t.Errorf("Bridge should be empty, got %q", ref.Bridge)
			}
			if ref.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", ref.Kind, tc.kind)
			}
			if ref.AgentID != tc.agentID {
				t.Errorf("AgentID = %q, want %q", ref.AgentID, tc.agentID)
			}
		})
	}
}

func TestNewBackend_EmptyBackendIDPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewBackend with empty backendID should panic")
		}
	}()
	NewBackend("codex", "ag-1", "")
}

func TestNewBridgeOnly_Success(t *testing.T) {
	ref := NewBridgeOnly("codex", "ag-1", "cse_1")
	if ref.Bridge != "cse_1" {
		t.Errorf("Bridge = %q, want cse_1", ref.Bridge)
	}
	if ref.Backend != "" {
		t.Errorf("Backend should be empty, got %q", ref.Backend)
	}
	// Empty kind is allowed.
	ref2 := NewBridgeOnly("", "", "cse_2")
	if ref2.Bridge != "cse_2" {
		t.Errorf("Bridge = %q, want cse_2", ref2.Bridge)
	}
}

func TestNewBridgeOnly_EmptyBridgeIDPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewBridgeOnly with empty bridgeID should panic")
		}
	}()
	NewBridgeOnly("codex", "ag-1", "")
}

func TestWithBackend_Success(t *testing.T) {
	base := NewBridgeOnly("codex", "ag-1", "cse_1")
	paired := base.WithBackend("thr-1")
	if paired.Backend != "thr-1" {
		t.Errorf("Backend = %q, want thr-1", paired.Backend)
	}
	if paired.Bridge != "cse_1" {
		t.Errorf("Bridge = %q, want cse_1", paired.Bridge)
	}
	if paired.Kind != "codex" || paired.AgentID != "ag-1" {
		t.Errorf("Kind/AgentID lost: %+v", paired)
	}
	// Original unchanged (value type).
	if base.Backend != "" {
		t.Errorf("base.Backend mutated: %+v", base)
	}
}

func TestWithBackend_PreconditionPanics(t *testing.T) {
	cases := []struct {
		name string
		base SessionRef
		arg  string
	}{
		{"empty backendID", NewBridgeOnly("codex", "ag-1", "cse_1"), ""},
		{"already paired", SessionRef{Backend: "thr-old", Bridge: "cse_1"}, "thr-new"},
		{"no bridge base", SessionRef{Kind: "codex"}, "thr-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("WithBackend should panic for %s", tc.name)
				}
			}()
			tc.base.WithBackend(tc.arg)
		})
	}
}
