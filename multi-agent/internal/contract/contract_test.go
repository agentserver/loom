package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestTaskContractApplyDefaults(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "generate a helper",
			SuccessCriteria: []string{"helper is saved as an artifact"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}

	tc.ApplyDefaults()

	if tc.ExecutionPolicy.Routing != RoutingDirectFirst {
		t.Fatalf("routing = %q", tc.ExecutionPolicy.Routing)
	}
	if !tc.ExecutionPolicy.AllowsCodeArtifacts() {
		t.Fatalf("allow_code_artifacts default = false")
	}
	if tc.ExecutionPolicy.CodePersistence != CodePersistenceObserverArtifactStore {
		t.Fatalf("code_persistence = %q", tc.ExecutionPolicy.CodePersistence)
	}
	if tc.ExecutionPolicy.WriteMode != WriteModeArtifactOnly {
		t.Fatalf("write_mode = %q", tc.ExecutionPolicy.WriteMode)
	}
	if tc.ExecutionPolicy.DAGNodeLimit() != 6 {
		t.Fatalf("max_dag_nodes = %d", tc.ExecutionPolicy.DAGNodeLimit())
	}
}

func TestTaskContractApplyDefaultsPreservesExplicitCodeArtifactsFalse(t *testing.T) {
	raw := []byte(`{
		"version": 1,
		"conversation_id": "conv-1",
		"intent": {
			"goal": "generate a helper",
			"success_criteria": ["helper is saved as an artifact"]
		},
		"data_contract": {
			"write_targets": [{"type":"artifact","kind":"code","name":"helper.go"}]
		},
		"execution_policy": {
			"allow_code_artifacts": false
		}
	}`)
	var tc TaskContract
	if err := json.Unmarshal(raw, &tc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	tc.ApplyDefaults()

	if tc.ExecutionPolicy.AllowCodeArtifacts == nil {
		t.Fatalf("allow_code_artifacts pointer is nil")
	}
	if tc.ExecutionPolicy.AllowsCodeArtifacts() {
		t.Fatalf("allow_code_artifacts false was not preserved")
	}
}

func TestTaskContractValidateRejectsExplicitZeroDAGNodeLimit(t *testing.T) {
	raw := []byte(`{
		"version": 1,
		"conversation_id": "conv-1",
		"intent": {
			"goal": "generate a helper",
			"success_criteria": ["helper is saved as an artifact"]
		},
		"data_contract": {
			"write_targets": [{"type":"artifact","kind":"code","name":"helper.go"}]
		},
		"execution_policy": {
			"max_dag_nodes": 0
		}
	}`)
	var tc TaskContract
	if err := json.Unmarshal(raw, &tc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	tc.ApplyDefaults()

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "execution_policy.max_dag_nodes must be >= 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskContractValidateRejectsMissingIntent(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "document", Name: "out.md"}},
		},
	}
	tc.ApplyDefaults()

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "intent.goal is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskContractValidateRejectsMissingConversationID(t *testing.T) {
	tc := TaskContract{
		Version: 1,
		Intent: IntentSpec{
			Goal:            "generate a helper",
			SuccessCriteria: []string{"helper is saved as an artifact"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}
	tc.ApplyDefaults()

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "conversation_id is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskContractValidateRejectsMissingWriteTargets(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "generate a helper",
			SuccessCriteria: []string{"helper is saved as an artifact"},
		},
	}
	tc.ApplyDefaults()

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "data_contract.write_targets is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskContractValidateRejectsCodeWriteTargetWhenCodeArtifactsDisabled(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "generate a helper",
			SuccessCriteria: []string{"helper is saved as an artifact"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}
	tc.ApplyDefaults()
	tc.ExecutionPolicy.AllowCodeArtifacts = Bool(false)

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "code write target requires execution_policy.allow_code_artifacts") {
		t.Fatalf("error = %v", err)
	}
}

func TestTaskContractValidateRejectsUnsupportedExposeCodePolicy(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "generate a helper",
			SuccessCriteria: []string{"helper is saved as an artifact"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "code", Name: "helper.go"}},
		},
	}
	tc.ApplyDefaults()
	tc.ExecutionPolicy.ExposeCodeToUser = "always"

	err := tc.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "execution_policy.expose_code_to_user") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	tc := TaskContract{
		Version:        1,
		ConversationID: "conv-1",
		Intent: IntentSpec{
			Goal:            "summarize artifacts",
			SuccessCriteria: []string{"summary references artifacts"},
		},
		DataContract: DataContract{
			WriteTargets: []WriteTarget{{Type: WriteTargetArtifact, Kind: "document", Name: "summary.md"}},
		},
	}
	tc.ApplyDefaults()

	prompt, err := EncodeEnvelope(tc, "Use this contract.")
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	got, body, ok, err := DecodeEnvelope(prompt)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if !ok {
		t.Fatalf("expected envelope")
	}
	if body != "Use this contract." {
		t.Fatalf("body = %q", body)
	}
	if got.ConversationID != "conv-1" {
		t.Fatalf("conversation_id = %q", got.ConversationID)
	}
}

func TestResourceSnapshotFromAgentCards(t *testing.T) {
	cardBody := json.RawMessage(`{
        "skills":["chat","mcp"],
        "tools":["echo"],
        "short_id":"short-a",
        "resources":{"memory_gb":16,"tags":["go"]}
    }`)
	snap := NewResourceSnapshot([]agentsdk.AgentCard{
		{
			AgentID:     "agent-a",
			DisplayName: "slave-a",
			Description: "worker",
			Status:      "available",
			Card:        cardBody,
		},
	}, "driver-id")

	if len(snap.Agents) != 1 {
		t.Fatalf("agents len = %d", len(snap.Agents))
	}
	a := snap.Agents[0]
	if a.AgentID != "agent-a" || a.ShortID != "short-a" {
		t.Fatalf("agent = %+v", a)
	}
	if len(a.Skills) != 2 || a.Skills[1] != "mcp" {
		t.Fatalf("skills = %#v", a.Skills)
	}
	if string(a.Resources) == "" {
		t.Fatalf("resources not preserved")
	}
}
