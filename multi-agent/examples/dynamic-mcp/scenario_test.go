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

func TestDriverClarifyContractScenarioFixtures(t *testing.T) {
	root := filepath.Join("scenarios", "driver-clarify-contract")
	require.FileExists(t, filepath.Join(root, "README.md"))

	initialPrompt, err := os.ReadFile(filepath.Join(root, "request", "user_initial_prompt.txt"))
	require.NoError(t, err)
	require.Contains(t, string(initialPrompt), "退款风险")

	clarification, err := os.ReadFile(filepath.Join(root, "request", "user_clarification.txt"))
	require.NoError(t, err)
	require.Contains(t, string(clarification), "orders.csv")
	require.Contains(t, string(clarification), "refund-policy.md")
	require.Contains(t, string(clarification), "允许即时构建")

	var capabilities struct {
		ResourceSnapshot contract.ResourceSnapshot `json:"resource_snapshot"`
		Masters          []contract.ResourceAgent  `json:"masters"`
		Slaves           []contract.ResourceAgent  `json:"slaves"`
		MCPTools         []struct {
			AgentID           string          `json:"agent_id"`
			Server            string          `json:"server"`
			Name              string          `json:"name"`
			InputSchema       json.RawMessage `json:"input_schema"`
			QualifiedToolName string          `json:"qualified_tool_name"`
		} `json:"mcp_tools"`
	}
	readJSON(t, filepath.Join(root, "request", "capabilities.json"), &capabilities)
	require.Len(t, capabilities.Masters, 1)
	require.Len(t, capabilities.Slaves, 2)
	require.Len(t, capabilities.MCPTools, 1)
	require.Equal(t, "csv_profiler/profile_orders_csv", capabilities.MCPTools[0].QualifiedToolName)
	require.JSONEq(t, string(mustMarshal(t, capabilities.ResourceSnapshot.Agents)), string(mustMarshal(t, append(capabilities.Masters, capabilities.Slaves...))))

	var drafted struct {
		Contract               contract.TaskContract `json:"contract"`
		ClarificationQuestions []string              `json:"clarification_questions"`
	}
	readJSON(t, filepath.Join(root, "request", "drafted_contract.json"), &drafted)
	drafted.Contract.ApplyDefaults()
	require.NoError(t, drafted.Contract.Validate())
	require.True(t, drafted.Contract.ExecutionPolicy.AllowBuildMCP)
	require.Contains(t, drafted.Contract.CapabilityRequirements.Tools, "csv_profiler/profile_orders_csv")
	require.Contains(t, drafted.Contract.CapabilityRequirements.Tools, "refund_policy_checker/evaluate_rows")
	require.NotEmpty(t, drafted.ClarificationQuestions)

	var expectedQuestions []string
	readJSON(t, filepath.Join(root, "expected", "clarification_questions.json"), &expectedQuestions)
	require.Equal(t, expectedQuestions, drafted.ClarificationQuestions)
}

func TestDriverClarifyContractDryRunExpectation(t *testing.T) {
	root := filepath.Join("scenarios", "driver-clarify-contract")

	var dryRun struct {
		Runnable              bool                     `json:"runnable"`
		RequiresBuildMCP      bool                     `json:"requires_build_mcp"`
		RecommendedTargetID   string                   `json:"recommended_target_id"`
		RecommendedSkill      string                   `json:"recommended_skill"`
		SatisfiedTools        []string                 `json:"satisfied_tools"`
		MissingTools          []string                 `json:"missing_tools"`
		CandidateBuildTargets []contract.ResourceAgent `json:"candidate_build_targets"`
		Reasons               []string                 `json:"reasons"`
	}
	readJSON(t, filepath.Join(root, "expected", "dry_run_result.json"), &dryRun)
	require.True(t, dryRun.Runnable)
	require.True(t, dryRun.RequiresBuildMCP)
	require.Equal(t, "master-online-e2e", dryRun.RecommendedTargetID)
	require.Equal(t, "fanout", dryRun.RecommendedSkill)
	require.Equal(t, []string{"csv_profiler/profile_orders_csv"}, dryRun.SatisfiedTools)
	require.Equal(t, []string{"refund_policy_checker/evaluate_rows"}, dryRun.MissingTools)
	require.Len(t, dryRun.CandidateBuildTargets, 1)
	require.Equal(t, "builder-slave", dryRun.CandidateBuildTargets[0].AgentID)
	require.Contains(t, dryRun.Reasons[0], "missing tools can be built")
}

func readJSON(t *testing.T, path string, dst interface{}) {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, dst))
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}
