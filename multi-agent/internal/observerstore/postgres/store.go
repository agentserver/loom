package postgres

import (
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type Store struct {
	db *sql.DB
}

var _ observerstore.ManagedStore = (*Store)(nil)

func Open(cfg Config) (*Store, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func notImplemented() error {
	return errors.New("observerstore/postgres: not implemented")
}

func (s *Store) LookupAPIKey(key string) (keyID string, ok bool, err error) {
	return "", false, notImplemented()
}

func (s *Store) UpsertWorkspaceLazy(id, name, apiKeyID string) error {
	return notImplemented()
}

func (s *Store) AgentBoundWorkspace(agentID string) (workspaceID string, found bool, err error) {
	return "", false, notImplemented()
}

func (s *Store) UpsertAgent(a observerstore.Agent, token, apiKeyID string) error {
	return notImplemented()
}

func (s *Store) ValidateToken(token string) (observerstore.Agent, bool, error) {
	return observerstore.Agent{}, false, notImplemented()
}

func (s *Store) Ingest(ev observer.Event) error {
	return notImplemented()
}

func (s *Store) GetTaskProgress(workspaceID, taskID string) (observerstore.TaskProgress, bool, error) {
	return observerstore.TaskProgress{}, false, notImplemented()
}

func (s *Store) CreateArtifact(create observerstore.ArtifactCreate) (observerstore.Artifact, error) {
	return observerstore.Artifact{}, notImplemented()
}

func (s *Store) RequestArtifact(workspaceID, requesterAgentID, artifactID string) (observerstore.ArtifactRequest, error) {
	return observerstore.ArtifactRequest{}, notImplemented()
}

func (s *Store) ListArtifactRequests(workspaceID, ownerAgentID string) ([]observerstore.ArtifactRequest, error) {
	return nil, notImplemented()
}

func (s *Store) StoreArtifactContent(workspaceID, ownerAgentID, artifactID, mime string, body io.Reader) error {
	return notImplemented()
}

func (s *Store) OpenArtifactContent(workspaceID, artifactID string) (observerstore.ArtifactContent, error) {
	return observerstore.ArtifactContent{}, notImplemented()
}

func (s *Store) CreateWrite(create observerstore.WriteCreate) (observerstore.Write, error) {
	return observerstore.Write{}, notImplemented()
}

func (s *Store) StoreWriteContent(workspaceID, writerAgentID, writeID, mime string, body io.Reader) error {
	return notImplemented()
}

func (s *Store) UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error {
	return notImplemented()
}

func (s *Store) ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]observerstore.Write, error) {
	return nil, notImplemented()
}

func (s *Store) SaveTaskContract(record observerstore.TaskContractRecord) error {
	return notImplemented()
}

func (s *Store) GetTaskContract(workspaceID, taskID string) (observerstore.TaskContractRecord, error) {
	return observerstore.TaskContractRecord{}, notImplemented()
}

func (s *Store) SaveResourceSnapshot(record observerstore.ResourceSnapshotRecord) error {
	return notImplemented()
}

func (s *Store) GetLatestResourceSnapshot(workspaceID string) (observerstore.ResourceSnapshotRecord, error) {
	return observerstore.ResourceSnapshotRecord{}, notImplemented()
}

func (s *Store) ListWorkspaceSummaries() ([]observerstore.WorkspaceSummary, error) {
	return nil, notImplemented()
}

func (s *Store) ReplaceAPIKeys(keys []observerstore.APIKeySpec) error {
	return notImplemented()
}
