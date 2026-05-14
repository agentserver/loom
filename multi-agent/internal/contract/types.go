package contract

import "encoding/json"

const Version = 1

const (
	RoutingDirectFirst = "direct_first"
	RoutingMasterOnly  = "master_only"
)

const (
	CodePersistenceObserverArtifactStore = "observer_artifact_store"
	ExposeCodeOnRequest                  = "on_request"
	WriteModeArtifactOnly                = "artifact_only"
	WriteModePatch                       = "patch"
	WriteModeRepoCommit                  = "repo_commit"
	WriteTargetArtifact                  = "artifact"
)

type TaskContract struct {
	Version                int                    `json:"version"`
	ConversationID         string                 `json:"conversation_id"`
	Intent                 IntentSpec             `json:"intent"`
	DataContract           DataContract           `json:"data_contract"`
	ExecutionPolicy        ExecutionPolicy        `json:"execution_policy"`
	CapabilityRequirements CapabilityRequirements `json:"capability_requirements"`
}

type IntentSpec struct {
	Goal            string   `json:"goal"`
	BusinessContext string   `json:"business_context,omitempty"`
	SuccessCriteria []string `json:"success_criteria"`
	NonGoals        []string `json:"non_goals,omitempty"`
}

type DataContract struct {
	ReadArtifacts []ArtifactRef `json:"read_artifacts"`
	WriteTargets  []WriteTarget `json:"write_targets"`
}

type ArtifactRef struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind,omitempty"`
	Name       string `json:"name,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type WriteTarget struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type ExecutionPolicy struct {
	Routing                          string   `json:"routing"`
	AllowMaster                      *bool    `json:"allow_master,omitempty"`
	AllowBuildMCP                    bool     `json:"allow_build_mcp"`
	AllowCodeArtifacts               bool     `json:"allow_code_artifacts"`
	CodePersistence                  string   `json:"code_persistence"`
	ExposeCodeToUser                 string   `json:"expose_code_to_user"`
	WriteMode                        string   `json:"write_mode"`
	MaxDAGNodes                      int      `json:"max_dag_nodes"`
	MaxDepth                         int      `json:"max_depth"`
	MaxConcurrency                   int      `json:"max_concurrency"`
	RequirePlanApproval              bool     `json:"require_plan_approval"`
	RequireUserApprovalForRepoWrites bool     `json:"require_user_approval_for_repo_writes"`
	AllowedTargets                   []string `json:"allowed_targets,omitempty"`
}

type CapabilityRequirements struct {
	Skills    []string        `json:"skills"`
	Tools     []string        `json:"tools"`
	Resources json.RawMessage `json:"resources,omitempty"`
}

type ResourceSnapshot struct {
	GeneratedAt string          `json:"generated_at"`
	Agents      []ResourceAgent `json:"agents"`
}

type ResourceAgent struct {
	AgentID     string          `json:"agent_id"`
	ShortID     string          `json:"short_id,omitempty"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description,omitempty"`
	Status      string          `json:"status"`
	Skills      []string        `json:"skills,omitempty"`
	Tools       []string        `json:"tools,omitempty"`
	MCPTools    json.RawMessage `json:"mcp_tools,omitempty"`
	Resources   json.RawMessage `json:"resources,omitempty"`
}

type ArtifactRecord struct {
	ArtifactID      string `json:"artifact_id"`
	ConversationID  string `json:"conversation_id"`
	TaskID          string `json:"task_id"`
	ProducerAgentID string `json:"producer_agent_id"`
	Kind            string `json:"kind"`
	Language        string `json:"language,omitempty"`
	Name            string `json:"name"`
	SHA256          string `json:"sha256"`
	Bytes           int64  `json:"bytes"`
	ContentRef      string `json:"content_ref"`
	VisibleToUser   bool   `json:"visible_to_user"`
	Purpose         string `json:"purpose"`
}
