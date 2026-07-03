package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/stretchr/testify/require"
)

// TestDraftTaskContract_CallsLookupAndAttachesHits — spec B4 §8. The
// draftTaskContractTool integrates Lookup: given a goal string, its
// response carries `registry_hits` populated by driver.Lookup.
func TestDraftTaskContract_CallsLookupAndAttachesHits(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	// Wire lookup deps: one registry entry + one userspace hit that both
	// match the goal keyword.
	sc := &sampleCapture{}
	resetRegistryForTest()
	resetLookupDepsForTest()
	resetNoRegistryLookupForTest()
	ResetLookupMetricsForTest()
	noteRegister("slave-a", "csv_profile", "s")
	SetLookupDeps(LookupDeps{
		UserspaceStore: &mockUserspace{rows: []PackageHit{{Slug: "csv_helper", Description: "helper"}}},
		WorkspaceID:    "ws-abc12345",
		SampleWrite:    sc.write,
		CurrentRunID:   CurrentRunID,
	})

	tool := toolByName(t, tools, "draft_task_contract")
	// Use a short goal that IS a substring of the registered MCP name
	// (case-insensitive). The registry-side matcher checks
	// "mcp_name contains query", so a broader goal like "profile csv"
	// won't match "csv_profile"; a shorter one like "csv_profile" does.
	out, err := tool.Call(context.Background(), json.RawMessage(`{"goal":"csv_profile"}`))
	require.NoError(t, err)
	var resp struct {
		RegistryHits []Hit `json:"registry_hits"`
	}
	require.NoError(t, json.Unmarshal(out, &resp))
	found := false
	for _, h := range resp.RegistryHits {
		if strings.Contains(h.MCPName, "csv_profile") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected csv_profile registry hit in response, got %+v", resp.RegistryHits)
	}
}

// TestDraftTaskContract_UnderNoRegistryLookup_NoHits — with the
// ablation on, registry_hits is nil/empty.
func TestDraftTaskContract_UnderNoRegistryLookup_NoHits(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return nil, nil
		},
	}
	tools := newTestTools(t, sdk)
	resetRegistryForTest()
	resetLookupDepsForTest()
	resetNoRegistryLookupForTest()
	ResetLookupMetricsForTest()
	noteRegister("slave-a", "csv_profile", "s")
	SetLookupDeps(LookupDeps{
		UserspaceStore: &mockUserspace{rows: []PackageHit{{Slug: "csv_helper"}}},
		WorkspaceID:    "ws-abc12345",
		SampleWrite:    (&sampleCapture{}).write,
	})
	noRegistryLookup = true
	defer resetNoRegistryLookupForTest()

	tool := toolByName(t, tools, "draft_task_contract")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"goal":"profile a csv file"}`))
	require.NoError(t, err)
	var resp struct {
		RegistryHits []Hit `json:"registry_hits"`
	}
	require.NoError(t, json.Unmarshal(out, &resp))
	if len(resp.RegistryHits) != 0 {
		t.Fatalf("registry_hits should be empty under NoRegistryLookup, got %+v", resp.RegistryHits)
	}
}
