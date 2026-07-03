package contract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/internal/capability"
)

func TestCapabilityRequirements_ForbiddenAliases_RoundTrip(t *testing.T) {
	orig := CapabilityRequirements{
		Skills:           []string{"chat"},
		Tools:            []string{"grep"},
		ForbiddenAliases: []string{"openai_for_glm", "gh_pat_dev"},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CapabilityRequirements
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig.ForbiddenAliases, got.ForbiddenAliases) {
		t.Errorf("ForbiddenAliases round-trip: got %v want %v", got.ForbiddenAliases, orig.ForbiddenAliases)
	}
}

func TestCapabilityRequirements_ToolRequirements_RoundTrip(t *testing.T) {
	orig := CapabilityRequirements{
		ToolRequirements: []ToolRequirement{
			{Name: "go", MinVersion: "1.22.0"},
			{Name: "sqlite3", MinVersion: ""}, // presence-only
		},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CapabilityRequirements
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig.ToolRequirements, got.ToolRequirements) {
		t.Errorf("ToolRequirements round-trip: got %v want %v", got.ToolRequirements, orig.ToolRequirements)
	}
}

func TestExecutionPolicy_RequiredReach_RoundTrip(t *testing.T) {
	orig := ExecutionPolicy{
		Routing:       RoutingMasterOnly,
		RequiredReach: capability.NetworkIntranet,
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ExecutionPolicy
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RequiredReach != capability.NetworkIntranet {
		t.Errorf("RequiredReach round-trip: got %q want %q", got.RequiredReach, capability.NetworkIntranet)
	}
}

// Zero-value emitting: `required_reach` and the two new
// capability_requirements fields all carry `omitempty` — a Marshal of a
// zero-value ExecutionPolicy / CapabilityRequirements MUST NOT include
// them (backward compat with existing task_contracts.body JSON blobs).
func TestNewFields_OmitEmpty(t *testing.T) {
	var cr CapabilityRequirements
	b, err := json.Marshal(cr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	bs := string(b)
	if strings.Contains(bs, "forbidden_aliases") {
		t.Errorf("empty CapabilityRequirements leaked forbidden_aliases: %s", bs)
	}
	if strings.Contains(bs, "tool_requirements") {
		t.Errorf("empty CapabilityRequirements leaked tool_requirements: %s", bs)
	}
	var ep ExecutionPolicy
	b2, err := json.Marshal(ep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b2), "required_reach") {
		t.Errorf("empty ExecutionPolicy leaked required_reach: %s", string(b2))
	}
}
