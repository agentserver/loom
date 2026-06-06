package observerstore

import (
	"database/sql"
	"encoding/json"
	"io"

	"github.com/yourorg/multi-agent/internal/observer"
)

type Workspace struct {
	ID   string
	Name string
}

// APIKeySpec is one row of the top-level api_keys table.
type APIKeySpec struct {
	ID   string
	Key  string
	Note string
}

type Agent struct {
	WorkspaceID string
	ID          string
	Role        string
	DisplayName string
}

type TaskView struct {
	WorkspaceID         string           `json:"workspace_id"`
	TaskID              string           `json:"task_id"`
	DriverID            string           `json:"driver_id"`
	MasterID            string           `json:"master_id"`
	SlaveID             string           `json:"slave_id"`
	Summary             string           `json:"summary"`
	Status              string           `json:"status"`
	HasMCP              bool             `json:"has_mcp"`
	MCPStatus           string           `json:"mcp_status"`
	LatestProgress      string           `json:"latest_progress"`
	LatestProgressPhase string           `json:"latest_progress_phase"`
	LatestProgressAt    string           `json:"latest_progress_at"`
	FinalOutput         string           `json:"final_output"`
	IsFinal             bool             `json:"is_final"`
	Output              string           `json:"output"`
	Error               string           `json:"error"`
	Subtasks            []SubtaskView    `json:"subtasks"`
	MCPServers          []MCPServerView  `json:"mcp_servers"`
	Events              []observer.Event `json:"events,omitempty"`
}

type SubtaskView struct {
	ParentTaskID        string `json:"parent_task_id"`
	SubtaskID           string `json:"subtask_id"`
	ChildTaskID         string `json:"child_task_id"`
	MasterID            string `json:"master_id"`
	SlaveID             string `json:"slave_id"`
	Summary             string `json:"summary"`
	DisplayLabel        string `json:"display_label"`
	Status              string `json:"status"`
	MCPStatus           string `json:"mcp_status"`
	LatestProgress      string `json:"latest_progress"`
	LatestProgressPhase string `json:"latest_progress_phase"`
	LatestProgressAt    string `json:"latest_progress_at"`
	Output              string `json:"output"`
	Error               string `json:"error"`
}

type MCPServerView struct {
	WorkspaceID     string          `json:"workspace_id"`
	TaskID          string          `json:"task_id"`
	ParentTaskID    string          `json:"parent_task_id"`
	SlaveID         string          `json:"slave_id"`
	Name            string          `json:"name"`
	Tools           json.RawMessage `json:"tools"`
	ToolDescriptors json.RawMessage `json:"tool_descriptors,omitempty"`
}

const (
	ArtifactStateRegistered = "registered"
	ArtifactStatePending    = "pending"
	ArtifactStateAvailable  = "available"
	WriteStateRegistered    = "registered"
	WriteStateCompleted     = "completed"
)

type ArtifactCreate struct {
	WorkspaceID  string
	OwnerAgentID string
	Path         string
	Kind         string
	MIME         string
	Bytes        int64
	SHA256       string
	State        string
}

type Artifact struct {
	ID           string `json:"artifact_id"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	OwnerAgentID string `json:"owner_agent_id,omitempty"`
	Path         string `json:"path,omitempty"`
	Kind         string `json:"kind,omitempty"`
	MIME         string `json:"mime,omitempty"`
	State        string `json:"state"`
	Bytes        int64  `json:"bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ObjectKey    string `json:"object_key,omitempty"`
}

type ArtifactRequest struct {
	RequestID    string `json:"request_id"`
	ArtifactID   string `json:"artifact_id"`
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	State        string `json:"state"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	OwnerAgentID string `json:"owner_agent_id,omitempty"`
}

type ArtifactContent struct {
	Artifact
	Body io.ReadCloser
}

type WriteCreate struct {
	WorkspaceID  string
	OwnerAgentID string
	TaskID       string
	Path         string
	Overwrite    bool
}

type Write struct {
	ID            string `json:"write_id"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	OwnerAgentID  string `json:"owner_agent_id,omitempty"`
	WriterAgentID string `json:"writer_agent_id,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	Path          string `json:"path"`
	Overwrite     bool   `json:"overwrite"`
	State         string `json:"state"`
	MIME          string `json:"mime,omitempty"`
	Bytes         int64  `json:"bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	ObjectKey     string `json:"object_key,omitempty"`
	Content       []byte `json:"-"`
}

type TaskContractRecord struct {
	WorkspaceID    string          `json:"workspace_id"`
	TaskID         string          `json:"task_id"`
	ConversationID string          `json:"conversation_id"`
	OwnerAgentID   string          `json:"owner_agent_id"`
	Body           json.RawMessage `json:"body"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
}

type ResourceSnapshotRecord struct {
	WorkspaceID  string          `json:"workspace_id"`
	SnapshotID   string          `json:"snapshot_id"`
	OwnerAgentID string          `json:"owner_agent_id"`
	Body         json.RawMessage `json:"body"`
	CreatedAt    string          `json:"created_at,omitempty"`
}

// WorkspaceSummary is one row of the workspaces overview returned by
// ListWorkspaceSummaries. Used by GET /api/workspaces (added in Task 6).
type WorkspaceSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	LastSeenAt    string `json:"last_seen_at"`
	AgentCount    int    `json:"agent_count"`
	RecentEventAt string `json:"recent_event_at,omitempty"`
}

// TaskProgress is the small subset of TaskView that driver needs to interpret
// awaiting_user markers carried in slave's final_output. Used by the tokened
// GET /api/tasks/{task_id}/progress endpoint.
type TaskProgress struct {
	LatestProgress      string `json:"latest_progress"`
	LatestProgressPhase string `json:"latest_progress_phase"`
	LatestProgressAt    string `json:"latest_progress_at"`
	FinalOutput         string `json:"final_output"`
	IsFinal             bool   `json:"is_final"`
}

type Store interface {
	LookupAPIKey(key string) (keyID string, ok bool, err error)
	ReplaceTelemetryAPIKeys(keys []TelemetryAPIKeySpec) error
	LookupTelemetryAPIKey(key, workspaceID string) (keyID string, ok bool, err error)
	UpsertWorkspaceLazy(id, name, apiKeyID string) error
	AgentBoundWorkspace(agentID string) (workspaceID string, found bool, err error)
	UpsertAgent(a Agent, token, apiKeyID string) error
	ValidateToken(token string) (Agent, bool, error)
	Ingest(ev observer.Event) error
	GetTaskProgress(workspaceID, taskID string) (TaskProgress, bool, error)
	CreateArtifact(ArtifactCreate) (Artifact, error)
	RequestArtifact(workspaceID, requesterAgentID, artifactID string) (ArtifactRequest, error)
	ListArtifactRequests(workspaceID, ownerAgentID string) ([]ArtifactRequest, error)
	StoreArtifactContent(workspaceID, ownerAgentID, artifactID, mime string, body io.Reader) error
	MarkArtifactAvailable(workspaceID, ownerAgentID, artifactID, mime, sha256, objectKey string, bytes int64) error
	OpenArtifactContent(workspaceID, artifactID string) (ArtifactContent, error)
	CreateWrite(WriteCreate) (Write, error)
	StoreWriteContent(workspaceID, writerAgentID, writeID, mime string, body io.Reader) error
	MarkWriteCompleted(workspaceID, writerAgentID, writeID, mime, sha256, objectKey string, bytes int64) error
	UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error
	ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]Write, error)
	SaveTaskContract(TaskContractRecord) error
	GetTaskContract(workspaceID, taskID string) (TaskContractRecord, error)
	SaveResourceSnapshot(ResourceSnapshotRecord) error
	GetLatestResourceSnapshot(workspaceID string) (ResourceSnapshotRecord, error)
	ListWorkspaceSummaries() ([]WorkspaceSummary, error)
}

type ManagedStore interface {
	Store
	Close() error
	DB() *sql.DB
	ReplaceAPIKeys(keys []APIKeySpec) error
}
