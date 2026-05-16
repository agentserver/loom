package dynamicmcp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/buildspec"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/driver"
	"github.com/yourorg/multi-agent/internal/orchestrator"
	"github.com/yourorg/multi-agent/internal/planner"
)

func TestDriverFilesMultiMCPScenarioFixtures(t *testing.T) {
	root := filepath.Join("scenarios", "driver-files-multi-mcp")
	require.FileExists(t, filepath.Join(root, "README.md"))

	var manifest driver.Manifest
	readJSON(t, filepath.Join(root, "request", "manifest.json"), &manifest)
	require.Len(t, manifest.Files, 2)
	require.Len(t, manifest.Writes, 1)
	require.Equal(t, "outputs/refund-risk-report.md", manifest.Writes[0].Path)
	require.Contains(t, manifest.Writes[0].PutURL, "/api/writes/")

	for _, f := range manifest.Files {
		require.Equal(t, "file", f.Kind)
		require.NotEmpty(t, f.URL)
		require.Contains(t, f.URL, "/api/files/")
		body, err := os.ReadFile(filepath.Join(root, f.Path))
		require.NoError(t, err)
		require.Equal(t, int64(len(body)), f.Bytes, f.Path)
		sum := sha256.Sum256(body)
		require.Equal(t, hex.EncodeToString(sum[:]), f.SHA256, f.Path)
	}

	var tc contract.TaskContract
	readJSON(t, filepath.Join(root, "request", "contract.json"), &tc)
	tc.ApplyDefaults()
	require.NoError(t, tc.Validate())
	require.Equal(t, contract.RoutingMasterOnly, tc.ExecutionPolicy.Routing)
	require.True(t, tc.ExecutionPolicy.AllowBuildMCP)
	require.GreaterOrEqual(t, tc.ExecutionPolicy.ConcurrencyLimit(), 2)
	require.Contains(t, tc.CapabilityRequirements.Skills, "build_mcp")

	promptBytes, err := os.ReadFile(filepath.Join(root, "request", "prompt.txt"))
	require.NoError(t, err)
	prompt := string(promptBytes)
	require.Contains(t, prompt, "<USER_FILES_MANIFEST version=1>")
	require.Contains(t, prompt, "orders.csv")
	require.Contains(t, prompt, "refund-policy.md")
	require.Contains(t, prompt, "Build missing MCP services in parallel when independent")
}

func TestDriverFilesMultiMCPExpectedPlan(t *testing.T) {
	root := filepath.Join("scenarios", "driver-files-multi-mcp")
	var nodes []planner.Node
	readJSON(t, filepath.Join(root, "expected", "first_phase_plan.json"), &nodes)

	require.NoError(t, orchestrator.Validate(nodes))
	require.Len(t, nodes, 2)
	seenNames := map[string]bool{}
	for _, n := range nodes {
		require.Empty(t, n.DependsOn, "first phase build_mcp nodes should be independently dispatchable")
		require.Equal(t, "builder-slave", n.TargetID)
		require.Equal(t, "build_mcp", n.Kind)
		require.Equal(t, "build_mcp", n.Skill)
		require.NotEmpty(t, n.BuildSpec)
		spec, err := buildspec.ParseJSON(string(n.BuildSpec))
		require.NoError(t, err)
		require.Len(t, spec.Tools, 1)
		require.False(t, seenNames[spec.Name], "duplicate build spec name %s", spec.Name)
		seenNames[spec.Name] = true
	}
	require.True(t, seenNames["csv_profile"])
	require.True(t, seenNames["refund_policy_check"])
}

func readJSON(t *testing.T, path string, dst interface{}) {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, dst))
}
