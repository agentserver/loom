package observerstore

import (
	"io"
	"testing"

	"github.com/yourorg/multi-agent/internal/observer"
)

type portableWebStore interface {
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

func TestSQLiteStoreImplementsStoreInterface(t *testing.T) {
	var _ Store = (*SQLiteStore)(nil)
}

func TestPortableWebStoreImplementsStoreInterface(t *testing.T) {
	var _ Store = (portableWebStore)(nil)
}

func TestSQLiteStoreImplementsManagedStoreInterface(t *testing.T) {
	var _ ManagedStore = (*SQLiteStore)(nil)
}
