package orchestrator

import (
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/contract"
	"github.com/yourorg/multi-agent/internal/planner"
)

func TestContractFromPromptRejectsAllowMasterFalse(t *testing.T) {
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: contract.IntentSpec{
			Goal:            "single-agent work",
			SuccessCriteria: []string{"done"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
	}
	tc.ApplyDefaults()
	tc.ExecutionPolicy.AllowMaster = contract.Bool(false)
	prompt, err := contract.EncodeEnvelope(tc, "body")
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	_, _, err = contractPolicyFromPrompt(prompt)
	if err == nil {
		t.Fatalf("expected error")
	}
	if err.Error() != "contract execution_policy.allow_master is false" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyRejectsBuildMCP(t *testing.T) {
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: contract.IntentSpec{
			Goal:            "generate code",
			SuccessCriteria: []string{"artifact saved"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}
	tc.ApplyDefaults()
	cases := []struct {
		name string
		node planner.Node
	}{
		{
			name: "skill",
			node: planner.Node{ID: "n1", TargetID: "slave", Skill: "build_mcp"},
		},
		{
			name: "kind",
			node: planner.Node{ID: "n1", TargetID: "slave", Kind: "build_mcp"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWithContractPolicy([]planner.Node{tt.node}, tc.ExecutionPolicy)
			if err == nil {
				t.Fatalf("expected policy error")
			}
			if err.Error() != "build_mcp node n1 rejected by contract policy" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidatePlanForContractRejectsBuildMCPReplan(t *testing.T) {
	tc := contract.TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: contract.IntentSpec{
			Goal:            "repair plan",
			SuccessCriteria: []string{"done"},
		},
		DataContract: contract.DataContract{
			WriteTargets: []contract.WriteTarget{{Type: contract.WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
	}
	tc.ApplyDefaults()
	replan := []planner.Node{{ID: "n1", TargetID: "slave", Kind: "build_mcp"}}

	err := validatePlanForContract(replan, tc)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "build_mcp node n1 rejected by contract policy" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyRejectsTooManyNodes(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(1),
		MaxDepth:           contract.Int(3),
		MaxConcurrency:     contract.Int(3),
	}
	nodes := []planner.Node{
		{ID: "n1", TargetID: "slave"},
		{ID: "n2", TargetID: "slave"},
	}

	err := ValidateWithContractPolicy(nodes, p)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "plan too large for contract: 2 nodes (max 1)" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyAndAgentsRejectsDisallowedTarget(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(3),
		MaxDepth:           contract.Int(3),
		MaxConcurrency:     contract.Int(3),
		AllowedTargets:     []string{"slave-a"},
	}
	nodes := []planner.Node{{ID: "n1", TargetID: "slave-b"}}
	agents := []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}, {AgentID: "slave-b", Status: "available"}}

	err := ValidateWithContractPolicyAndAgents(nodes, p, agents)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "node n1 target slave-b is not allowed by contract policy" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyAndAgentsRejectsUnknownTarget(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(3),
		MaxDepth:           contract.Int(3),
		MaxConcurrency:     contract.Int(3),
	}
	nodes := []planner.Node{{ID: "n1", TargetID: "slave-b"}}
	agents := []agentsdk.AgentCard{{AgentID: "slave-a", Status: "available"}}

	err := ValidateWithContractPolicyAndAgents(nodes, p, agents)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "node n1 target slave-b is not in current resource snapshot" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatePlanWithPolicyAndAgentsRejectsTargetWhenSnapshotEmpty(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(3),
		MaxDepth:           contract.Int(3),
		MaxConcurrency:     contract.Int(3),
	}
	nodes := []planner.Node{{ID: "n1", TargetID: "slave-b"}}

	err := ValidateWithContractPolicyAndAgents(nodes, p, nil)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "node n1 target slave-b is not in current resource snapshot" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateAppendWithPolicyAllowsDependencyOnExistingNode(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(3),
		MaxDepth:           contract.Int(2),
		MaxConcurrency:     contract.Int(3),
	}
	existing := []planner.Node{{ID: "n1", TargetID: "slave"}}
	appended := []planner.Node{{ID: "n2", TargetID: "slave", DependsOn: []string{"n1"}}}

	if err := ValidateAppendWithContractPolicy(existing, appended, p); err != nil {
		t.Fatalf("ValidateAppendWithContractPolicy: %v", err)
	}
}

func TestValidateAppendWithPolicyRejectsTotalNodeLimit(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(2),
		MaxDepth:           contract.Int(3),
		MaxConcurrency:     contract.Int(3),
	}
	existing := []planner.Node{
		{ID: "n1", TargetID: "slave"},
		{ID: "n2", TargetID: "slave"},
	}
	appended := []planner.Node{{ID: "n3", TargetID: "slave"}}

	err := ValidateAppendWithContractPolicy(existing, appended, p)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "plan too large for contract: 3 nodes (max 2)" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateWithPolicyRejectsDepthLimit(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(4),
		MaxDepth:           contract.Int(2),
		MaxConcurrency:     contract.Int(3),
	}
	nodes := []planner.Node{
		{ID: "n1", TargetID: "slave"},
		{ID: "n2", TargetID: "slave", DependsOn: []string{"n1"}},
		{ID: "n3", TargetID: "slave", DependsOn: []string{"n2"}},
	}

	err := ValidateWithContractPolicy(nodes, p)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "plan depth exceeds contract: 3 (max 2)" {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateAppendWithPolicyRejectsDepthLimit(t *testing.T) {
	p := contract.ExecutionPolicy{
		AllowMaster:        contract.Bool(true),
		AllowCodeArtifacts: contract.Bool(true),
		CodePersistence:    contract.CodePersistenceObserverArtifactStore,
		WriteMode:          contract.WriteModeArtifactOnly,
		Routing:            contract.RoutingMasterOnly,
		MaxDAGNodes:        contract.Int(4),
		MaxDepth:           contract.Int(2),
		MaxConcurrency:     contract.Int(3),
	}
	existing := []planner.Node{
		{ID: "n1", TargetID: "slave"},
		{ID: "n2", TargetID: "slave", DependsOn: []string{"n1"}},
	}
	appended := []planner.Node{{ID: "n3", TargetID: "slave", DependsOn: []string{"n2"}}}

	err := ValidateAppendWithContractPolicy(existing, appended, p)
	if err == nil {
		t.Fatalf("expected policy error")
	}
	if err.Error() != "plan depth exceeds contract: 3 (max 2)" {
		t.Fatalf("error = %v", err)
	}
}

func TestEffectiveFanoutConcurrencyUsesContractLimit(t *testing.T) {
	p := contract.ExecutionPolicy{MaxConcurrency: contract.Int(2)}

	got := effectiveFanoutConcurrency(5, p, true)
	if got != 2 {
		t.Fatalf("effectiveFanoutConcurrency = %d", got)
	}

	got = effectiveFanoutConcurrency(1, p, true)
	if got != 1 {
		t.Fatalf("effectiveFanoutConcurrency with lower config = %d", got)
	}

	got = effectiveFanoutConcurrency(5, p, false)
	if got != 5 {
		t.Fatalf("effectiveFanoutConcurrency without contract = %d", got)
	}
}
