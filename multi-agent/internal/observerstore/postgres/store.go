package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yourorg/multi-agent/internal/capability"
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

func (s *Store) LookupAPIKey(key string) (keyID string, ok bool, err error) {
	if key == "" {
		return "", false, nil
	}
	err = s.db.QueryRow(
		`SELECT id FROM api_keys WHERE key_hash=$1`,
		observerstore.TokenHash(key),
	).Scan(&keyID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return keyID, true, nil
}

func (s *Store) ReplaceTelemetryAPIKeys(keys []observerstore.TelemetryAPIKeySpec) error {
	seenID := map[string]bool{}
	seenHash := map[string]bool{}
	for i, k := range keys {
		if k.ID == "" {
			return fmt.Errorf("observerstore: telemetry api key[%d] id must not be empty", i)
		}
		if k.Key == "" {
			return fmt.Errorf("observerstore: telemetry api key[%s] value must not be empty", k.ID)
		}
		if seenID[k.ID] {
			return fmt.Errorf("observerstore: duplicate telemetry api key id %q", k.ID)
		}
		h := observerstore.TokenHash(k.Key)
		if seenHash[h] {
			return fmt.Errorf("observerstore: duplicate telemetry api key value (id=%q)", k.ID)
		}
		seenID[k.ID] = true
		seenHash[h] = true
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM telemetry_api_keys`); err != nil {
		return err
	}
	now := observerstore.NowUTC()
	for _, k := range keys {
		workspaceID := k.WorkspaceID
		if workspaceID == "" {
			workspaceID = "*"
		}
		if _, err := tx.Exec(
			`INSERT INTO telemetry_api_keys(id, key_hash, note, workspace_id, enabled, created_at)
			 VALUES($1, $2, $3, $4, $5, $6)`,
			k.ID, observerstore.TokenHash(k.Key), k.Note, workspaceID, k.Enabled, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LookupTelemetryAPIKey(key, workspaceID string) (keyID string, ok bool, err error) {
	if key == "" {
		return "", false, nil
	}
	err = s.db.QueryRow(
		`SELECT id
		   FROM telemetry_api_keys
		  WHERE key_hash=$1
		    AND enabled=true
		    AND (workspace_id='*' OR workspace_id=$2)
		  LIMIT 1`,
		observerstore.TokenHash(key), workspaceID,
	).Scan(&keyID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return keyID, true, nil
}

func (s *Store) UpsertWorkspaceLazy(id, name, apiKeyID string) error {
	if id == "" {
		return errors.New("observerstore: workspace id must not be empty")
	}
	if apiKeyID == "" {
		return errors.New("observerstore: apiKeyID must not be empty")
	}
	now := observerstore.NowUTC()
	_, err := s.db.Exec(
		`INSERT INTO workspaces(id, name, created_by_api_key_id, created_at, last_seen_at)
         VALUES($1, $2, $3, $4, $5)
         ON CONFLICT(id) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		id, name, apiKeyID, now, now,
	)
	return err
}

func (s *Store) AgentBoundWorkspace(agentID string) (workspaceID string, found bool, err error) {
	if agentID == "" {
		return "", false, nil
	}
	var ws string
	err = s.db.QueryRow(`SELECT workspace_id FROM agents WHERE id=$1 LIMIT 1`, agentID).Scan(&ws)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ws, true, nil
}

func (s *Store) UpsertAgent(a observerstore.Agent, token, apiKeyID string) error {
	if token == "" {
		return errors.New("observerstore: agent token must not be empty")
	}
	if apiKeyID == "" {
		return errors.New("observerstore: apiKeyID must not be empty")
	}
	_, err := s.db.Exec(
		`INSERT INTO agents(workspace_id, id, role, display_name, token_hash, created_by_api_key_id)
         VALUES($1, $2, $3, $4, $5, $6)
         ON CONFLICT(workspace_id, id) DO UPDATE SET
            role = excluded.role,
            display_name = excluded.display_name,
            token_hash = excluded.token_hash`,
		a.WorkspaceID, a.ID, a.Role, a.DisplayName, observerstore.TokenHash(token), apiKeyID,
	)
	return err
}

func (s *Store) ValidateToken(token string) (observerstore.Agent, bool, error) {
	if token == "" {
		return observerstore.Agent{}, false, nil
	}
	var a observerstore.Agent
	err := s.db.QueryRow(`SELECT workspace_id, id, role, display_name FROM agents WHERE token_hash=$1`, observerstore.TokenHash(token)).
		Scan(&a.WorkspaceID, &a.ID, &a.Role, &a.DisplayName)
	if err == sql.ErrNoRows {
		return observerstore.Agent{}, false, nil
	}
	if err != nil {
		return observerstore.Agent{}, false, err
	}
	return a, true, nil
}

func (s *Store) Ingest(ev observer.Event) error {
	if ev.TS == "" {
		ev.TS = observerstore.NowUTC()
	}
	if ev.EventID == "" {
		var err error
		ev.EventID, err = observerstore.GeneratedEventID()
		if err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	tools, err := json.Marshal(ev.MCPTools)
	if err != nil {
		return err
	}
	payload := sql.NullString{}
	if len(ev.Payload) > 0 {
		payload = sql.NullString{String: string(ev.Payload), Valid: true}
	}
	result, err := tx.Exec(`INSERT INTO events(event_id, ts, workspace_id, agent_id, agent_role, type, task_id, parent_task_id, subtask_id, child_task_id, summary, subtask_summary, status, target_agent_id, target_role, mcp_server_name, mcp_tools, payload)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17::jsonb, $18::jsonb)
		ON CONFLICT(event_id) DO NOTHING`,
		ev.EventID, ev.TS, ev.WorkspaceID, ev.AgentID, ev.AgentRole, ev.Type, ev.TaskID,
		observerstore.NullString(ev.ParentTaskID), observerstore.NullString(ev.SubtaskID), observerstore.NullString(ev.ChildTaskID),
		observerstore.NullString(ev.Summary), observerstore.NullString(ev.SubtaskSummary), observerstore.NullString(ev.Status),
		observerstore.NullString(ev.TargetAgentID), observerstore.NullString(ev.TargetRole), observerstore.NullString(ev.MCPServerName),
		string(tools), payload)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return tx.Commit()
	}
	if err := applyAggregate(tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetTaskProgress(workspaceID, taskID string) (observerstore.TaskProgress, bool, error) {
	if workspaceID == "" || taskID == "" {
		return observerstore.TaskProgress{}, false, nil
	}
	var p observerstore.TaskProgress
	err := s.db.QueryRow(
		`SELECT latest_progress, latest_progress_phase, latest_progress_at, final_output, is_final
		   FROM tasks WHERE workspace_id=$1 AND task_id=$2`,
		workspaceID, taskID,
	).Scan(&p.LatestProgress, &p.LatestProgressPhase, &p.LatestProgressAt, &p.FinalOutput, &p.IsFinal)
	if err == sql.ErrNoRows {
		return observerstore.TaskProgress{}, false, nil
	}
	if err != nil {
		return observerstore.TaskProgress{}, false, err
	}
	return p, true, nil
}

func (s *Store) CreateArtifact(create observerstore.ArtifactCreate) (observerstore.Artifact, error) {
	if create.State == "" {
		create.State = observerstore.ArtifactStateRegistered
	}
	if create.WorkspaceID == "" || create.OwnerAgentID == "" || create.Path == "" || create.Kind == "" {
		return observerstore.Artifact{}, errors.New("observerstore: workspace, owner, path, and kind are required")
	}
	id, err := observerstore.PrefixedID("art")
	if err != nil {
		return observerstore.Artifact{}, err
	}
	now := observerstore.NowUTC()
	_, err = s.db.Exec(`INSERT INTO artifacts(workspace_id, id, owner_agent_id, path, kind, mime, state, bytes, sha256, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		create.WorkspaceID, id, create.OwnerAgentID, create.Path, create.Kind, create.MIME, create.State, create.Bytes, create.SHA256, now, now)
	if err != nil {
		return observerstore.Artifact{}, err
	}
	return observerstore.Artifact{
		ID: id, WorkspaceID: create.WorkspaceID, OwnerAgentID: create.OwnerAgentID,
		Path: create.Path, Kind: create.Kind, MIME: create.MIME, State: create.State,
		Bytes: create.Bytes, SHA256: create.SHA256,
	}, nil
}

func (s *Store) RequestArtifact(workspaceID, requesterAgentID, artifactID string) (observerstore.ArtifactRequest, error) {
	var owner, path, kind, state string
	err := s.db.QueryRow(`SELECT owner_agent_id, path, kind, state FROM artifacts WHERE workspace_id=$1 AND id=$2`, workspaceID, artifactID).
		Scan(&owner, &path, &kind, &state)
	if err == sql.ErrNoRows {
		return observerstore.ArtifactRequest{}, fmt.Errorf("artifact not found")
	}
	if err != nil {
		return observerstore.ArtifactRequest{}, err
	}
	if state == observerstore.ArtifactStateAvailable {
		return observerstore.ArtifactRequest{ArtifactID: artifactID, Kind: kind, Path: path, State: observerstore.ArtifactStateAvailable, WorkspaceID: workspaceID, OwnerAgentID: owner}, nil
	}
	var existing string
	err = s.db.QueryRow(`SELECT id FROM artifact_requests WHERE workspace_id=$1 AND artifact_id=$2 AND state=$3 ORDER BY created_at ASC LIMIT 1`,
		workspaceID, artifactID, observerstore.ArtifactStatePending).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return observerstore.ArtifactRequest{}, err
	}
	if existing == "" {
		var genErr error
		existing, genErr = observerstore.PrefixedID("fetch")
		if genErr != nil {
			return observerstore.ArtifactRequest{}, genErr
		}
		now := observerstore.NowUTC()
		_, err = s.db.Exec(`INSERT INTO artifact_requests(workspace_id, id, artifact_id, requester_agent_id, owner_agent_id, state, created_at, updated_at)
			VALUES($1, $2, $3, $4, $5, $6, $7, $8)`, workspaceID, existing, artifactID, requesterAgentID, owner, observerstore.ArtifactStatePending, now, now)
		if err != nil {
			return observerstore.ArtifactRequest{}, err
		}
	}
	return observerstore.ArtifactRequest{RequestID: existing, ArtifactID: artifactID, Kind: kind, Path: path, State: observerstore.ArtifactStatePending, WorkspaceID: workspaceID, OwnerAgentID: owner}, nil
}

func (s *Store) ListArtifactRequests(workspaceID, ownerAgentID string) ([]observerstore.ArtifactRequest, error) {
	rows, err := s.db.Query(`SELECT r.id, r.artifact_id, a.kind, a.path, r.state
		FROM artifact_requests r JOIN artifacts a ON a.workspace_id=r.workspace_id AND a.id=r.artifact_id
		WHERE r.workspace_id=$1 AND r.owner_agent_id=$2 AND r.state=$3
		ORDER BY r.created_at ASC`, workspaceID, ownerAgentID, observerstore.ArtifactStatePending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observerstore.ArtifactRequest
	for rows.Next() {
		var r observerstore.ArtifactRequest
		r.WorkspaceID = workspaceID
		r.OwnerAgentID = ownerAgentID
		if err := rows.Scan(&r.RequestID, &r.ArtifactID, &r.Kind, &r.Path, &r.State); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) StoreArtifactContent(workspaceID, ownerAgentID, artifactID, mime string, body io.Reader) error {
	return errors.New("observerstore/postgres: content storage requires object store")
}

func (s *Store) OpenArtifactContent(workspaceID, artifactID string) (observerstore.ArtifactContent, error) {
	return observerstore.ArtifactContent{}, errors.New("observerstore/postgres: content storage requires object store")
}

func (s *Store) CreateWrite(create observerstore.WriteCreate) (observerstore.Write, error) {
	if create.WorkspaceID == "" || create.OwnerAgentID == "" || create.TaskID == "" || create.Path == "" {
		return observerstore.Write{}, errors.New("observerstore: workspace, owner, task, and path are required")
	}
	id, err := observerstore.PrefixedID("wr")
	if err != nil {
		return observerstore.Write{}, err
	}
	now := observerstore.NowUTC()
	_, err = s.db.Exec(`INSERT INTO writes(workspace_id, id, owner_agent_id, task_id, path, overwrite, state, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9)`, create.WorkspaceID, id, create.OwnerAgentID, create.TaskID, create.Path, create.Overwrite, observerstore.WriteStateRegistered, now, now)
	if err != nil {
		return observerstore.Write{}, err
	}
	return observerstore.Write{ID: id, WorkspaceID: create.WorkspaceID, OwnerAgentID: create.OwnerAgentID, TaskID: create.TaskID, Path: create.Path, Overwrite: create.Overwrite, State: observerstore.WriteStateRegistered}, nil
}

func (s *Store) StoreWriteContent(workspaceID, writerAgentID, writeID, mime string, body io.Reader) error {
	return errors.New("observerstore/postgres: content storage requires object store")
}

func (s *Store) UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error {
	if taskID == "" {
		return errors.New("observerstore: task_id is required")
	}
	res, err := s.db.Exec(`UPDATE writes SET task_id=$1, updated_at=$2 WHERE workspace_id=$3 AND owner_agent_id=$4 AND id=$5`,
		taskID, observerstore.NowUTC(), workspaceID, ownerAgentID, writeID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("write not found")
	}
	return nil
}

func (s *Store) ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]observerstore.Write, error) {
	rows, err := s.db.Query(`SELECT id, writer_agent_id, path, overwrite, mime, bytes, sha256
		FROM writes WHERE workspace_id=$1 AND owner_agent_id=$2 AND task_id=$3 AND state=$4
		ORDER BY updated_at ASC`, workspaceID, ownerAgentID, taskID, observerstore.WriteStateCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observerstore.Write
	for rows.Next() {
		var w observerstore.Write
		w.WorkspaceID = workspaceID
		w.OwnerAgentID = ownerAgentID
		w.TaskID = taskID
		w.State = observerstore.WriteStateCompleted
		if err := rows.Scan(&w.ID, &w.WriterAgentID, &w.Path, &w.Overwrite, &w.MIME, &w.Bytes, &w.SHA256); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) SaveTaskContract(record observerstore.TaskContractRecord) error {
	if record.WorkspaceID == "" || record.TaskID == "" || record.ConversationID == "" || record.OwnerAgentID == "" || len(record.Body) == 0 {
		return errors.New("observerstore: workspace, task, conversation, owner, and body are required")
	}
	now := observerstore.NowUTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var existingOwner string
	err = tx.QueryRow(`SELECT owner_agent_id FROM task_contracts WHERE workspace_id=$1 AND task_id=$2`,
		record.WorkspaceID, record.TaskID).Scan(&existingOwner)
	if err == sql.ErrNoRows {
		_, err = tx.Exec(`INSERT INTO task_contracts(workspace_id, task_id, conversation_id, owner_agent_id, body, created_at, updated_at)
			VALUES($1, $2, $3, $4, $5::jsonb, $6, $7)`,
			record.WorkspaceID, record.TaskID, record.ConversationID, record.OwnerAgentID, string(record.Body), now, now)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if existingOwner != record.OwnerAgentID {
		return errors.New("task contract owner mismatch")
	}
	_, err = tx.Exec(`UPDATE task_contracts SET conversation_id=$1, body=$2::jsonb, updated_at=$3
		WHERE workspace_id=$4 AND task_id=$5 AND owner_agent_id=$6`,
		record.ConversationID, string(record.Body), now, record.WorkspaceID, record.TaskID, record.OwnerAgentID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetTaskContract(workspaceID, taskID string) (observerstore.TaskContractRecord, error) {
	var out observerstore.TaskContractRecord
	var body string
	err := s.db.QueryRow(`SELECT workspace_id, task_id, conversation_id, owner_agent_id, body::text, created_at::text, updated_at::text
		FROM task_contracts WHERE workspace_id=$1 AND task_id=$2`, workspaceID, taskID).
		Scan(&out.WorkspaceID, &out.TaskID, &out.ConversationID, &out.OwnerAgentID, &body, &out.CreatedAt, &out.UpdatedAt)
	if err == sql.ErrNoRows {
		return observerstore.TaskContractRecord{}, fmt.Errorf("task contract not found")
	}
	if err != nil {
		return observerstore.TaskContractRecord{}, err
	}
	out.Body = json.RawMessage(body)
	return out, nil
}

func (s *Store) SaveResourceSnapshot(record observerstore.ResourceSnapshotRecord) error {
	if record.WorkspaceID == "" || record.SnapshotID == "" || record.OwnerAgentID == "" || len(record.Body) == 0 {
		return errors.New("observerstore: workspace, snapshot, owner, and body are required")
	}
	now := observerstore.NowUTC()
	_, err := s.db.Exec(`INSERT INTO resource_snapshots(workspace_id, snapshot_id, owner_agent_id, body, created_at)
		VALUES($1, $2, $3, $4::jsonb, $5)`, record.WorkspaceID, record.SnapshotID, record.OwnerAgentID, string(record.Body), now)
	return err
}

func (s *Store) GetLatestResourceSnapshot(workspaceID string) (observerstore.ResourceSnapshotRecord, error) {
	var out observerstore.ResourceSnapshotRecord
	var body string
	err := s.db.QueryRow(`SELECT workspace_id, snapshot_id, owner_agent_id, body::text, created_at::text
		FROM resource_snapshots WHERE workspace_id=$1 ORDER BY created_at DESC, snapshot_id DESC LIMIT 1`, workspaceID).
		Scan(&out.WorkspaceID, &out.SnapshotID, &out.OwnerAgentID, &body, &out.CreatedAt)
	if err == sql.ErrNoRows {
		return observerstore.ResourceSnapshotRecord{}, fmt.Errorf("resource snapshot not found")
	}
	if err != nil {
		return observerstore.ResourceSnapshotRecord{}, err
	}
	out.Body = json.RawMessage(body)
	return out, nil
}

func (s *Store) ListWorkspaceSummaries() ([]observerstore.WorkspaceSummary, error) {
	rows, err := s.db.Query(`
        SELECT w.id, w.name, w.last_seen_at::text,
               COALESCE((SELECT COUNT(*)::integer FROM agents a WHERE a.workspace_id = w.id), 0),
               COALESCE((SELECT MAX(ts)::text FROM events e WHERE e.workspace_id = w.id), '')
          FROM workspaces w
         ORDER BY w.last_seen_at DESC
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observerstore.WorkspaceSummary
	for rows.Next() {
		var ws observerstore.WorkspaceSummary
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.LastSeenAt, &ws.AgentCount, &ws.RecentEventAt); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *Store) ReplaceAPIKeys(keys []observerstore.APIKeySpec) error {
	if len(keys) == 0 {
		return errors.New("observerstore: ReplaceAPIKeys: refusing to replace with empty set (would lock out all agents)")
	}
	seenID := map[string]bool{}
	seenHash := map[string]bool{}
	for i, k := range keys {
		if k.ID == "" {
			return fmt.Errorf("observerstore: api key[%d] id must not be empty", i)
		}
		if k.Key == "" {
			return fmt.Errorf("observerstore: api key[%s] value must not be empty", k.ID)
		}
		if seenID[k.ID] {
			return fmt.Errorf("observerstore: duplicate api key id %q", k.ID)
		}
		h := observerstore.TokenHash(k.Key)
		if seenHash[h] {
			return fmt.Errorf("observerstore: duplicate api key value (id=%q)", k.ID)
		}
		seenID[k.ID] = true
		seenHash[h] = true
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM api_keys`); err != nil {
		return err
	}
	now := observerstore.NowUTC()
	for _, k := range keys {
		if _, err := tx.Exec(
			`INSERT INTO api_keys(id, key_hash, note, created_at) VALUES($1, $2, $3, $4)`,
			k.ID, observerstore.TokenHash(k.Key), k.Note, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EventCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT count(*)::integer FROM events`).Scan(&count)
	return count, err
}

func applyAggregate(tx *sql.Tx, ev observer.Event) error {
	if observer.IsProgressEvent(ev.Type) {
		return updateLatestProgress(tx, ev)
	}
	switch ev.Type {
	case observer.EventDriverTaskSubmitted:
		return upsertDriverTask(tx, ev)
	case observer.EventMasterTaskReceived:
		return upsertMasterTask(tx, ev)
	case observer.EventDriverTaskStatus, observer.EventMasterTaskCompleted, observer.EventMasterTaskFailed:
		return updateTaskStatus(tx, ev)
	case observer.EventMasterSubtaskDispatched, observer.EventMasterSubtaskDone:
		return upsertSubtask(tx, ev)
	case observer.EventSlaveTaskStarted, observer.EventSlaveTaskCompleted, observer.EventSlaveTaskFailed:
		return upsertSlaveTask(tx, ev)
	case observer.EventMCPServerCreated:
		return upsertMCPServer(tx, ev)
	case observer.EventMCPServerRemoved:
		return deleteMCPServer(tx, ev)
	case observer.EventMCPServerBlocked, observer.EventMasterMCPReplan:
		return applyMCPStatus(tx, ev)
	default:
		return nil
	}
}

func upsertDriverTask(tx *sql.Tx, ev observer.Event) error {
	summary := ev.Summary
	if summary == "" {
		summary = ev.TaskID
	}
	masterID := ""
	slaveID := ""
	switch ev.TargetRole {
	case observer.RoleMaster:
		masterID = ev.TargetAgentID
	case observer.RoleSlave:
		slaveID = ev.TargetAgentID
	}
	_, err := tx.Exec(`INSERT INTO tasks(workspace_id, task_id, driver_agent_id, master_agent_id, slave_agent_id, summary, status, has_mcp, mcp_status, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=$8 AND (parent_task_id=$9 OR (parent_task_id IS NULL AND task_id=$10))) THEN true ELSE false END,
			CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=$11 AND (parent_task_id=$12 OR (parent_task_id IS NULL AND task_id=$13))) THEN 'created' ELSE '' END, $14, $15)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			driver_agent_id=excluded.driver_agent_id,
			master_agent_id=COALESCE(excluded.master_agent_id, tasks.master_agent_id),
			slave_agent_id=COALESCE(excluded.slave_agent_id, tasks.slave_agent_id),
			summary=excluded.summary,
			status=excluded.status,
			has_mcp=tasks.has_mcp OR excluded.has_mcp,
			mcp_status=CASE WHEN tasks.mcp_status='created' THEN tasks.mcp_status WHEN excluded.mcp_status!='' THEN excluded.mcp_status ELSE tasks.mcp_status END,
			updated_at=excluded.updated_at`,
		ev.WorkspaceID, ev.TaskID, ev.AgentID, observerstore.NullString(masterID), observerstore.NullString(slaveID), summary, valueOr(ev.Status, "assigned"),
		ev.WorkspaceID, ev.TaskID, ev.TaskID, ev.WorkspaceID, ev.TaskID, ev.TaskID, ev.TS, ev.TS)
	return err
}

func upsertMasterTask(tx *sql.Tx, ev observer.Event) error {
	summary := ev.Summary
	if summary == "" {
		summary = ev.TaskID
	}
	_, err := tx.Exec(`INSERT INTO tasks(workspace_id, task_id, master_agent_id, summary, status, has_mcp, mcp_status, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=$6 AND (parent_task_id=$7 OR (parent_task_id IS NULL AND task_id=$8))) THEN true ELSE false END,
			CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=$9 AND (parent_task_id=$10 OR (parent_task_id IS NULL AND task_id=$11))) THEN 'created' ELSE '' END, $12, $13)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			master_agent_id=excluded.master_agent_id,
			summary=excluded.summary,
			status=excluded.status,
			has_mcp=tasks.has_mcp OR excluded.has_mcp,
			mcp_status=CASE WHEN tasks.mcp_status='created' THEN tasks.mcp_status WHEN excluded.mcp_status!='' THEN excluded.mcp_status ELSE tasks.mcp_status END,
			updated_at=excluded.updated_at`,
		ev.WorkspaceID, ev.TaskID, ev.AgentID, summary, valueOr(ev.Status, "running"),
		ev.WorkspaceID, ev.TaskID, ev.TaskID, ev.WorkspaceID, ev.TaskID, ev.TaskID, ev.TS, ev.TS)
	return err
}

func updateTaskStatus(tx *sql.Tx, ev observer.Event) error {
	if ev.Status == "" {
		return nil
	}
	output := payloadString(ev.Payload, "output")
	terminal := isTerminalStatus(ev.Status)
	_, err := tx.Exec(`UPDATE tasks SET status=$1, is_final=CASE WHEN $2::boolean THEN true ELSE is_final END, final_output=CASE WHEN $3::boolean THEN COALESCE($4::text, final_output) ELSE final_output END, updated_at=$5 WHERE workspace_id=$6 AND task_id=$7`,
		ev.Status, terminal, terminal, output, ev.TS, ev.WorkspaceID, ev.TaskID)
	return err
}

func updateLatestProgress(tx *sql.Tx, ev observer.Event) error {
	message := payloadString(ev.Payload, "message")
	if !message.Valid {
		message = observerstore.NullString(ev.Summary)
	}
	phase := payloadString(ev.Payload, "phase")
	if !phase.Valid {
		phase = observerstore.NullString(ev.Type)
	}

	parentTaskID, subtaskID := linkedParentAndSubtask(tx, ev)
	if parentTaskID != "" && subtaskID != "" {
		result, err := tx.Exec(`UPDATE subtasks SET latest_progress=$1, latest_progress_phase=$2, latest_progress_at=$3, updated_at=$4 WHERE workspace_id=$5 AND parent_task_id=$6 AND subtask_id=$7`,
			message.String, phase.String, ev.TS, ev.TS, ev.WorkspaceID, parentTaskID, subtaskID)
		if err != nil {
			return err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if updated > 0 {
			return nil
		}
		return nil
	}

	taskID := ev.TaskID
	if parentTaskID != "" {
		taskID = parentTaskID
	}
	if taskID == "" {
		return nil
	}
	_, err := tx.Exec(`UPDATE tasks SET latest_progress=$1, latest_progress_phase=$2, latest_progress_at=$3, updated_at=$4 WHERE workspace_id=$5 AND task_id=$6`,
		message.String, phase.String, ev.TS, ev.TS, ev.WorkspaceID, taskID)
	return err
}

func upsertSubtask(tx *sql.Tx, ev observer.Event) error {
	parentTaskID := ev.TaskID
	if ev.ParentTaskID != "" {
		parentTaskID = ev.ParentTaskID
	}
	subtaskID := ev.SubtaskID
	if subtaskID == "" {
		subtaskID = ev.ChildTaskID
	}
	if ev.Type == observer.EventMasterSubtaskDone {
		return updateSubtaskDone(tx, ev, parentTaskID, subtaskID)
	}

	parentSummary := taskSummary(tx, ev.WorkspaceID, parentTaskID)
	subtaskSummary := valueOr(ev.SubtaskSummary, subtaskID)
	displayLabel := joinLabel(parentSummary, subtaskSummary)
	_, err := tx.Exec(`INSERT INTO subtasks(workspace_id, parent_task_id, subtask_id, child_task_id, master_agent_id, slave_agent_id, summary, display_label, status, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT(workspace_id, parent_task_id, subtask_id) DO UPDATE SET
			child_task_id=excluded.child_task_id,
			master_agent_id=excluded.master_agent_id,
			slave_agent_id=excluded.slave_agent_id,
			summary=excluded.summary,
			display_label=excluded.display_label,
			status=excluded.status,
			updated_at=excluded.updated_at`,
		ev.WorkspaceID, parentTaskID, subtaskID, observerstore.NullString(ev.ChildTaskID), ev.AgentID, observerstore.NullString(ev.TargetAgentID),
		subtaskSummary, displayLabel, valueOr(ev.Status, "assigned"), ev.TS, ev.TS)
	if err != nil {
		return err
	}
	return reconcileMCPServersForSubtask(tx, ev.WorkspaceID, parentTaskID, subtaskID, ev.ChildTaskID, ev.TS)
}

func upsertSlaveTask(tx *sql.Tx, ev observer.Event) error {
	status := valueOr(ev.Status, "running")
	output := payloadString(ev.Payload, "output")
	eventErr := payloadString(ev.Payload, "error")
	result, err := tx.Exec(`UPDATE subtasks SET slave_agent_id=$1, status=$2, output=COALESCE($3::text, output), error=COALESCE($4::text, error), updated_at=$5 WHERE workspace_id=$6 AND child_task_id=$7`,
		ev.AgentID, status, output, eventErr, ev.TS, ev.WorkspaceID, ev.TaskID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated > 0 {
		_, err = tx.Exec(`UPDATE tasks SET updated_at=$1 WHERE workspace_id=$2 AND task_id IN (SELECT parent_task_id FROM subtasks WHERE workspace_id=$3 AND child_task_id=$4)`,
			ev.TS, ev.WorkspaceID, ev.WorkspaceID, ev.TaskID)
		return err
	}
	summary := valueOr(ev.Summary, ev.TaskID)
	terminal := isTerminalStatus(status)
	_, err = tx.Exec(`INSERT INTO tasks(workspace_id, task_id, slave_agent_id, summary, status, output, error, final_output, is_final, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, CASE WHEN $8::boolean THEN COALESCE($9::text, '') ELSE '' END, CASE WHEN $10::boolean THEN true ELSE false END, $11, $12)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			slave_agent_id=excluded.slave_agent_id,
			status=excluded.status,
			output=COALESCE(excluded.output, tasks.output),
			error=COALESCE(excluded.error, tasks.error),
			final_output=CASE WHEN excluded.is_final AND excluded.final_output!='' THEN excluded.final_output ELSE tasks.final_output END,
			is_final=CASE WHEN excluded.is_final THEN true ELSE tasks.is_final END,
			updated_at=excluded.updated_at`,
		ev.WorkspaceID, ev.TaskID, ev.AgentID, summary, status, output, eventErr, terminal, output, terminal, ev.TS, ev.TS)
	return err
}

func updateSubtaskDone(tx *sql.Tx, ev observer.Event, parentTaskID, subtaskID string) error {
	if subtaskID == "" {
		return nil
	}
	status := ev.Status
	if status == "" {
		status = "completed"
	}
	output := payloadString(ev.Payload, "output")
	eventErr := payloadString(ev.Payload, "error")
	result, err := tx.Exec(`UPDATE subtasks SET status=$1, output=COALESCE($2::text, output), error=COALESCE($3::text, error), updated_at=$4 WHERE workspace_id=$5 AND parent_task_id=$6 AND subtask_id=$7`,
		status, output, eventErr, ev.TS, ev.WorkspaceID, parentTaskID, subtaskID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated > 0 {
		return nil
	}

	parentSummary := taskSummary(tx, ev.WorkspaceID, parentTaskID)
	subtaskSummary := valueOr(ev.SubtaskSummary, subtaskID)
	displayLabel := joinLabel(parentSummary, subtaskSummary)
	_, err = tx.Exec(`INSERT INTO subtasks(workspace_id, parent_task_id, subtask_id, child_task_id, master_agent_id, slave_agent_id, summary, display_label, status, output, error, created_at, updated_at)
		VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::text, $11::text, $12, $13)`,
		ev.WorkspaceID, parentTaskID, subtaskID, observerstore.NullString(ev.ChildTaskID), ev.AgentID, observerstore.NullString(ev.TargetAgentID),
		subtaskSummary, displayLabel, status, output, eventErr, ev.TS, ev.TS)
	return err
}

func upsertMCPServer(tx *sql.Tx, ev observer.Event) error {
	tools, err := json.Marshal(ev.MCPTools)
	if err != nil {
		return err
	}
	descriptors, err := mcpToolDescriptorsJSON(ev)
	if err != nil {
		return err
	}
	parentTaskID, subtaskID := linkedParentAndSubtask(tx, ev)
	_, err = tx.Exec(`INSERT INTO mcp_servers(workspace_id, task_id, parent_task_id, slave_agent_id, name, tools, tool_descriptors, created_at)
		VALUES($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8)
		ON CONFLICT(workspace_id, task_id, name) DO UPDATE SET
			parent_task_id=excluded.parent_task_id,
			slave_agent_id=excluded.slave_agent_id,
			tools=excluded.tools,
			tool_descriptors=COALESCE(excluded.tool_descriptors, mcp_servers.tool_descriptors)`,
		ev.WorkspaceID, ev.TaskID, observerstore.NullString(parentTaskID), ev.AgentID, ev.MCPServerName, string(tools), observerstore.NullString(string(descriptors)), ev.TS)
	if err != nil {
		return err
	}

	if parentTaskID != "" {
		if _, err = tx.Exec(`UPDATE tasks SET has_mcp=true, mcp_status='created', updated_at=$1 WHERE workspace_id=$2 AND task_id=$3`,
			ev.TS, ev.WorkspaceID, parentTaskID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(`UPDATE tasks SET has_mcp=true, mcp_status='created', updated_at=$1 WHERE workspace_id=$2 AND task_id=$3`,
			ev.TS, ev.WorkspaceID, ev.TaskID); err != nil {
			return err
		}
	}
	if subtaskID != "" {
		_, err = tx.Exec(`UPDATE subtasks SET mcp_status='created', updated_at=$1 WHERE workspace_id=$2 AND parent_task_id=$3 AND subtask_id=$4`,
			ev.TS, ev.WorkspaceID, parentTaskID, subtaskID)
	}
	return err
}

func deleteMCPServer(tx *sql.Tx, ev observer.Event) error {
	_, err := tx.Exec(`DELETE FROM mcp_servers WHERE workspace_id=$1 AND name=$2`,
		ev.WorkspaceID, ev.MCPServerName)
	return err
}

func reconcileMCPServersForSubtask(tx *sql.Tx, workspaceID, parentTaskID, subtaskID, childTaskID, ts string) error {
	if childTaskID == "" {
		return nil
	}
	result, err := tx.Exec(`UPDATE mcp_servers SET parent_task_id=$1 WHERE workspace_id=$2 AND task_id=$3 AND parent_task_id IS NULL`,
		parentTaskID, workspaceID, childTaskID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		return nil
	}
	if _, err := tx.Exec(`UPDATE tasks SET has_mcp=true, mcp_status='created', updated_at=$1 WHERE workspace_id=$2 AND task_id=$3`,
		ts, workspaceID, parentTaskID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE subtasks SET mcp_status='created', updated_at=$1 WHERE workspace_id=$2 AND parent_task_id=$3 AND subtask_id=$4`,
		ts, workspaceID, parentTaskID, subtaskID)
	return err
}

func mcpToolDescriptorsJSON(ev observer.Event) ([]byte, error) {
	if len(ev.MCPToolDescriptors) > 0 {
		return json.Marshal(ev.MCPToolDescriptors)
	}
	if len(ev.Payload) == 0 {
		return nil, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return nil, err
	}
	raw, ok := payload["mcp_tool_descriptors"]
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, errors.New("observerstore: mcp_tool_descriptors must be an array")
	}
	var descriptors []capability.MCPToolDescriptor
	if err := json.Unmarshal(raw, &descriptors); err != nil {
		return nil, errors.New("observerstore: mcp_tool_descriptors must be an array of descriptor objects")
	}
	return json.Marshal(descriptors)
}

func applyMCPStatus(tx *sql.Tx, ev observer.Event) error {
	status := "blocked"
	if ev.Type == observer.EventMasterMCPReplan && ev.Status == "mcp_tool_set" {
		status = "created"
	}
	parentTaskID, subtaskID := linkedParentAndSubtask(tx, ev)
	if parentTaskID == "" {
		parentTaskID = ev.TaskID
	}
	if status == "created" {
		if _, err := tx.Exec(`UPDATE tasks SET has_mcp=true, mcp_status='created', updated_at=$1 WHERE workspace_id=$2 AND task_id=$3`,
			ev.TS, ev.WorkspaceID, parentTaskID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE tasks SET mcp_status=CASE WHEN mcp_status='created' THEN mcp_status ELSE 'blocked' END, updated_at=$1 WHERE workspace_id=$2 AND task_id=$3`,
			ev.TS, ev.WorkspaceID, parentTaskID); err != nil {
			return err
		}
	}
	if subtaskID == "" {
		return nil
	}
	_, err := tx.Exec(`UPDATE subtasks SET mcp_status=CASE WHEN $1::text='created' THEN 'created' WHEN mcp_status='created' THEN mcp_status ELSE 'blocked' END, updated_at=$2 WHERE workspace_id=$3 AND parent_task_id=$4 AND subtask_id=$5`,
		status, ev.TS, ev.WorkspaceID, parentTaskID, subtaskID)
	return err
}

func linkedParentAndSubtask(tx *sql.Tx, ev observer.Event) (string, string) {
	parentTaskID := ev.ParentTaskID
	subtaskID := ev.SubtaskID
	if parentTaskID != "" && subtaskID != "" {
		return parentTaskID, subtaskID
	}
	if ev.ChildTaskID != "" {
		var parent, subtask string
		if err := tx.QueryRow(`SELECT parent_task_id, subtask_id FROM subtasks WHERE workspace_id=$1 AND child_task_id=$2 LIMIT 1`, ev.WorkspaceID, ev.ChildTaskID).Scan(&parent, &subtask); err == nil {
			if parentTaskID == "" {
				parentTaskID = parent
			}
			if subtaskID == "" {
				subtaskID = subtask
			}
		}
	}
	if ev.TaskID != "" {
		var parent, subtask string
		if err := tx.QueryRow(`SELECT parent_task_id, subtask_id FROM subtasks WHERE workspace_id=$1 AND child_task_id=$2 LIMIT 1`, ev.WorkspaceID, ev.TaskID).Scan(&parent, &subtask); err == nil {
			if parentTaskID == "" {
				parentTaskID = parent
			}
			if subtaskID == "" {
				subtaskID = subtask
			}
		}
	}
	return parentTaskID, subtaskID
}

func taskSummary(tx *sql.Tx, workspaceID, taskID string) string {
	var summary string
	_ = tx.QueryRow(`SELECT summary FROM tasks WHERE workspace_id=$1 AND task_id=$2`, workspaceID, taskID).Scan(&summary)
	return summary
}

func joinLabel(parentSummary, subtaskSummary string) string {
	if parentSummary == "" {
		return subtaskSummary
	}
	if subtaskSummary == "" {
		return parentSummary
	}
	return parentSummary + " - " + subtaskSummary
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func payloadString(payload json.RawMessage, field string) sql.NullString {
	if len(payload) == 0 {
		return sql.NullString{}
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return sql.NullString{}
	}
	raw, ok := values[field]
	if !ok {
		return sql.NullString{}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}
