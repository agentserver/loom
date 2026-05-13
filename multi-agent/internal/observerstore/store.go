package observerstore

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/yourorg/multi-agent/internal/capability"
	"github.com/yourorg/multi-agent/internal/observer"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *sql.DB
}

type Workspace struct {
	ID   string
	Name string
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

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func ensureColumns(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN mcp_status TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN latest_progress TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN latest_progress_phase TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN latest_progress_at TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN final_output TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN is_final INTEGER NOT NULL DEFAULT 0`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE subtasks ADD COLUMN mcp_status TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE subtasks ADD COLUMN latest_progress TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE subtasks ADD COLUMN latest_progress_phase TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE subtasks ADD COLUMN latest_progress_at TEXT NOT NULL DEFAULT ''`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN slave_agent_id TEXT`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN output TEXT`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE tasks ADD COLUMN error TEXT`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	if _, err := db.Exec(`ALTER TABLE mcp_servers ADD COLUMN tool_descriptors TEXT`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	return nil
}

func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column")
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) UpsertWorkspace(w Workspace) error {
	_, err := s.db.Exec(`INSERT INTO workspaces(id, name) VALUES(?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name`, w.ID, w.Name)
	return err
}

func (s *Store) UpsertAgent(a Agent, token string) error {
	if token == "" {
		return errors.New("observerstore: agent token must not be empty")
	}
	_, err := s.db.Exec(`INSERT INTO agents(workspace_id, id, role, display_name, token_hash) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, id) DO UPDATE SET role=excluded.role, display_name=excluded.display_name, token_hash=excluded.token_hash`,
		a.WorkspaceID, a.ID, a.Role, a.DisplayName, tokenHash(token))
	return err
}

func (s *Store) ValidateToken(token string) (Agent, bool, error) {
	if token == "" {
		return Agent{}, false, nil
	}
	var a Agent
	err := s.db.QueryRow(`SELECT workspace_id, id, role, display_name FROM agents WHERE token_hash=?`, tokenHash(token)).
		Scan(&a.WorkspaceID, &a.ID, &a.Role, &a.DisplayName)
	if err == sql.ErrNoRows {
		return Agent{}, false, nil
	}
	if err != nil {
		return Agent{}, false, err
	}
	return a, true, nil
}

func (s *Store) EventCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM events`).Scan(&count)
	return count, err
}

func (s *Store) Ingest(ev observer.Event) error {
	if ev.TS == "" {
		ev.TS = nowUTC()
	}
	if ev.EventID == "" {
		var err error
		ev.EventID, err = generatedEventID()
		if err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tools, err := json.Marshal(ev.MCPTools)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT OR IGNORE INTO events(event_id, ts, workspace_id, agent_id, agent_role, type, task_id, parent_task_id, subtask_id, child_task_id, summary, subtask_summary, status, target_agent_id, target_role, mcp_server_name, mcp_tools, payload)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.EventID, ev.TS, ev.WorkspaceID, ev.AgentID, ev.AgentRole, ev.Type, ev.TaskID,
		nullString(ev.ParentTaskID), nullString(ev.SubtaskID), nullString(ev.ChildTaskID),
		nullString(ev.Summary), nullString(ev.SubtaskSummary), nullString(ev.Status),
		nullString(ev.TargetAgentID), nullString(ev.TargetRole), nullString(ev.MCPServerName),
		string(tools), string(ev.Payload))
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

func (s *Store) ListTasks() ([]TaskView, error) {
	rows, err := s.db.Query(`SELECT workspace_id, task_id, COALESCE(driver_agent_id, ''), COALESCE(master_agent_id, ''), COALESCE(slave_agent_id, ''), summary, status, has_mcp, mcp_status, latest_progress, latest_progress_phase, latest_progress_at, final_output, is_final, COALESCE(output, ''), COALESCE(error, '')
		FROM tasks ORDER BY created_at ASC, task_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TaskView
	for rows.Next() {
		var task TaskView
		var hasMCP int
		var isFinal int
		if err := rows.Scan(&task.WorkspaceID, &task.TaskID, &task.DriverID, &task.MasterID, &task.SlaveID, &task.Summary, &task.Status, &hasMCP, &task.MCPStatus, &task.LatestProgress, &task.LatestProgressPhase, &task.LatestProgressAt, &task.FinalOutput, &isFinal, &task.Output, &task.Error); err != nil {
			return nil, err
		}
		task.HasMCP = hasMCP != 0
		task.IsFinal = isFinal != 0
		task.Subtasks, err = s.listSubtasks(task.WorkspaceID, task.TaskID)
		if err != nil {
			return nil, err
		}
		task.MCPServers, err = s.listMCPServers(task.WorkspaceID, task.TaskID)
		if err != nil {
			return nil, err
		}
		task.Events, err = s.ListEvents(task.TaskID)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) ListEvents(taskID string) ([]observer.Event, error) {
	query := `SELECT event_id, ts, workspace_id, agent_id, agent_role, type, task_id, COALESCE(parent_task_id, ''), COALESCE(subtask_id, ''), COALESCE(child_task_id, ''), COALESCE(summary, ''), COALESCE(subtask_summary, ''), COALESCE(status, ''), COALESCE(target_agent_id, ''), COALESCE(target_role, ''), COALESCE(mcp_server_name, ''), COALESCE(mcp_tools, '[]'), COALESCE(payload, '')
		FROM events`
	var args []interface{}
	if taskID != "" {
		query += ` WHERE task_id=? OR parent_task_id=? OR task_id IN (SELECT child_task_id FROM subtasks WHERE parent_task_id=? AND child_task_id IS NOT NULL)`
		args = append(args, taskID, taskID, taskID)
	}
	query += ` ORDER BY ts ASC, event_id ASC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []observer.Event{}
	for rows.Next() {
		var ev observer.Event
		var tools, payload string
		if err := rows.Scan(&ev.EventID, &ev.TS, &ev.WorkspaceID, &ev.AgentID, &ev.AgentRole, &ev.Type, &ev.TaskID, &ev.ParentTaskID, &ev.SubtaskID, &ev.ChildTaskID, &ev.Summary, &ev.SubtaskSummary, &ev.Status, &ev.TargetAgentID, &ev.TargetRole, &ev.MCPServerName, &tools, &payload); err != nil {
			return nil, err
		}
		if tools != "" && tools != "null" {
			_ = json.Unmarshal([]byte(tools), &ev.MCPTools)
		}
		if payload != "" {
			ev.Payload = json.RawMessage(payload)
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (s *Store) listSubtasks(workspaceID, taskID string) ([]SubtaskView, error) {
	rows, err := s.db.Query(`SELECT parent_task_id, subtask_id, COALESCE(child_task_id, ''), COALESCE(master_agent_id, ''), COALESCE(slave_agent_id, ''), summary, display_label, status, mcp_status, latest_progress, latest_progress_phase, latest_progress_at, COALESCE(output, ''), COALESCE(error, '')
		FROM subtasks WHERE workspace_id=? AND parent_task_id=? ORDER BY created_at ASC, subtask_id ASC`, workspaceID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	subtasks := []SubtaskView{}
	for rows.Next() {
		var subtask SubtaskView
		if err := rows.Scan(&subtask.ParentTaskID, &subtask.SubtaskID, &subtask.ChildTaskID, &subtask.MasterID, &subtask.SlaveID, &subtask.Summary, &subtask.DisplayLabel, &subtask.Status, &subtask.MCPStatus, &subtask.LatestProgress, &subtask.LatestProgressPhase, &subtask.LatestProgressAt, &subtask.Output, &subtask.Error); err != nil {
			return nil, err
		}
		subtasks = append(subtasks, subtask)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return subtasks, nil
}

func (s *Store) listMCPServers(workspaceID, taskID string) ([]MCPServerView, error) {
	rows, err := s.db.Query(`SELECT workspace_id, task_id, COALESCE(parent_task_id, ''), slave_agent_id, name, tools, COALESCE(tool_descriptors, '')
		FROM mcp_servers WHERE workspace_id=? AND (task_id=? OR parent_task_id=?) ORDER BY created_at ASC, name ASC`, workspaceID, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	servers := []MCPServerView{}
	for rows.Next() {
		var server MCPServerView
		var tools, descriptors string
		if err := rows.Scan(&server.WorkspaceID, &server.TaskID, &server.ParentTaskID, &server.SlaveID, &server.Name, &tools, &descriptors); err != nil {
			return nil, err
		}
		server.Tools = json.RawMessage(tools)
		if descriptors != "" {
			server.ToolDescriptors = json.RawMessage(descriptors)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return servers, nil
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
		VALUES(?, ?, ?, ?, ?, ?, ?, CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=? AND (parent_task_id=? OR (parent_task_id IS NULL AND task_id=?))) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=? AND (parent_task_id=? OR (parent_task_id IS NULL AND task_id=?))) THEN 'created' ELSE '' END, ?, ?)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			driver_agent_id=excluded.driver_agent_id,
			master_agent_id=COALESCE(excluded.master_agent_id, tasks.master_agent_id),
			slave_agent_id=COALESCE(excluded.slave_agent_id, tasks.slave_agent_id),
			summary=excluded.summary,
			status=excluded.status,
			has_mcp=CASE WHEN tasks.has_mcp=1 OR excluded.has_mcp=1 THEN 1 ELSE 0 END,
			mcp_status=CASE WHEN tasks.mcp_status='created' THEN tasks.mcp_status WHEN excluded.mcp_status!='' THEN excluded.mcp_status ELSE tasks.mcp_status END,
			updated_at=excluded.updated_at`,
		ev.WorkspaceID, ev.TaskID, ev.AgentID, nullString(masterID), nullString(slaveID), summary, valueOr(ev.Status, "assigned"),
		ev.WorkspaceID, ev.TaskID, ev.TaskID, ev.WorkspaceID, ev.TaskID, ev.TaskID, ev.TS, ev.TS)
	return err
}

func upsertMasterTask(tx *sql.Tx, ev observer.Event) error {
	summary := ev.Summary
	if summary == "" {
		summary = ev.TaskID
	}
	_, err := tx.Exec(`INSERT INTO tasks(workspace_id, task_id, master_agent_id, summary, status, has_mcp, mcp_status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=? AND (parent_task_id=? OR (parent_task_id IS NULL AND task_id=?))) THEN 1 ELSE 0 END,
			CASE WHEN EXISTS(SELECT 1 FROM mcp_servers WHERE workspace_id=? AND (parent_task_id=? OR (parent_task_id IS NULL AND task_id=?))) THEN 'created' ELSE '' END, ?, ?)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			master_agent_id=excluded.master_agent_id,
			summary=excluded.summary,
			status=excluded.status,
			has_mcp=CASE WHEN tasks.has_mcp=1 OR excluded.has_mcp=1 THEN 1 ELSE 0 END,
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
	_, err := tx.Exec(`UPDATE tasks SET status=?, is_final=CASE WHEN ? THEN 1 ELSE is_final END, final_output=CASE WHEN ? THEN COALESCE(?, final_output) ELSE final_output END, updated_at=? WHERE workspace_id=? AND task_id=?`,
		ev.Status, terminal, terminal, output, ev.TS, ev.WorkspaceID, ev.TaskID)
	return err
}

func updateLatestProgress(tx *sql.Tx, ev observer.Event) error {
	message := payloadString(ev.Payload, "message")
	if !message.Valid {
		message = nullString(ev.Summary)
	}
	phase := payloadString(ev.Payload, "phase")
	if !phase.Valid {
		phase = nullString(ev.Type)
	}

	parentTaskID, subtaskID := linkedParentAndSubtask(tx, ev)
	if parentTaskID != "" && subtaskID != "" {
		result, err := tx.Exec(`UPDATE subtasks SET latest_progress=?, latest_progress_phase=?, latest_progress_at=?, updated_at=? WHERE workspace_id=? AND parent_task_id=? AND subtask_id=?`,
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
	_, err := tx.Exec(`UPDATE tasks SET latest_progress=?, latest_progress_phase=?, latest_progress_at=?, updated_at=? WHERE workspace_id=? AND task_id=?`,
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
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, parent_task_id, subtask_id) DO UPDATE SET
			child_task_id=excluded.child_task_id,
			master_agent_id=excluded.master_agent_id,
			slave_agent_id=excluded.slave_agent_id,
			summary=excluded.summary,
			display_label=excluded.display_label,
			status=excluded.status,
			updated_at=excluded.updated_at`,
		ev.WorkspaceID, parentTaskID, subtaskID, nullString(ev.ChildTaskID), ev.AgentID, nullString(ev.TargetAgentID),
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
	result, err := tx.Exec(`UPDATE subtasks SET slave_agent_id=?, status=?, output=COALESCE(?, output), error=COALESCE(?, error), updated_at=? WHERE workspace_id=? AND child_task_id=?`,
		ev.AgentID, status, output, eventErr, ev.TS, ev.WorkspaceID, ev.TaskID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated > 0 {
		_, err = tx.Exec(`UPDATE tasks SET updated_at=? WHERE workspace_id=? AND task_id IN (SELECT parent_task_id FROM subtasks WHERE workspace_id=? AND child_task_id=?)`,
			ev.TS, ev.WorkspaceID, ev.WorkspaceID, ev.TaskID)
		return err
	}
	summary := valueOr(ev.Summary, ev.TaskID)
	terminal := isTerminalStatus(status)
	_, err = tx.Exec(`INSERT INTO tasks(workspace_id, task_id, slave_agent_id, summary, status, output, error, final_output, is_final, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, CASE WHEN ? THEN COALESCE(?, '') ELSE '' END, CASE WHEN ? THEN 1 ELSE 0 END, ?, ?)
		ON CONFLICT(workspace_id, task_id) DO UPDATE SET
			slave_agent_id=excluded.slave_agent_id,
			status=excluded.status,
			output=COALESCE(excluded.output, tasks.output),
			error=COALESCE(excluded.error, tasks.error),
			final_output=CASE WHEN excluded.is_final=1 AND excluded.final_output!='' THEN excluded.final_output ELSE tasks.final_output END,
			is_final=CASE WHEN excluded.is_final=1 THEN 1 ELSE tasks.is_final END,
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
	result, err := tx.Exec(`UPDATE subtasks SET status=?, output=COALESCE(?, output), error=COALESCE(?, error), updated_at=? WHERE workspace_id=? AND parent_task_id=? AND subtask_id=?`,
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
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.WorkspaceID, parentTaskID, subtaskID, nullString(ev.ChildTaskID), ev.AgentID, nullString(ev.TargetAgentID),
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
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, task_id, name) DO UPDATE SET
			parent_task_id=excluded.parent_task_id,
			slave_agent_id=excluded.slave_agent_id,
			tools=excluded.tools,
			tool_descriptors=COALESCE(excluded.tool_descriptors, mcp_servers.tool_descriptors)`,
		ev.WorkspaceID, ev.TaskID, nullString(parentTaskID), ev.AgentID, ev.MCPServerName, string(tools), nullString(string(descriptors)), ev.TS)
	if err != nil {
		return err
	}

	if parentTaskID != "" {
		if _, err = tx.Exec(`UPDATE tasks SET has_mcp=1, mcp_status='created', updated_at=? WHERE workspace_id=? AND task_id=?`,
			ev.TS, ev.WorkspaceID, parentTaskID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(`UPDATE tasks SET has_mcp=1, mcp_status='created', updated_at=? WHERE workspace_id=? AND task_id=?`,
			ev.TS, ev.WorkspaceID, ev.TaskID); err != nil {
			return err
		}
	}
	if subtaskID != "" {
		_, err = tx.Exec(`UPDATE subtasks SET mcp_status='created', updated_at=? WHERE workspace_id=? AND parent_task_id=? AND subtask_id=?`,
			ev.TS, ev.WorkspaceID, parentTaskID, subtaskID)
	}
	return err
}

func reconcileMCPServersForSubtask(tx *sql.Tx, workspaceID, parentTaskID, subtaskID, childTaskID, ts string) error {
	if childTaskID == "" {
		return nil
	}
	result, err := tx.Exec(`UPDATE mcp_servers SET parent_task_id=? WHERE workspace_id=? AND task_id=? AND parent_task_id IS NULL`,
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
	if _, err := tx.Exec(`UPDATE tasks SET has_mcp=1, mcp_status='created', updated_at=? WHERE workspace_id=? AND task_id=?`,
		ts, workspaceID, parentTaskID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE subtasks SET mcp_status='created', updated_at=? WHERE workspace_id=? AND parent_task_id=? AND subtask_id=?`,
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
		if _, err := tx.Exec(`UPDATE tasks SET has_mcp=1, mcp_status='created', updated_at=? WHERE workspace_id=? AND task_id=?`,
			ev.TS, ev.WorkspaceID, parentTaskID); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(`UPDATE tasks SET mcp_status=CASE WHEN mcp_status='created' THEN mcp_status ELSE 'blocked' END, updated_at=? WHERE workspace_id=? AND task_id=?`,
			ev.TS, ev.WorkspaceID, parentTaskID); err != nil {
			return err
		}
	}
	if subtaskID == "" {
		return nil
	}
	_, err := tx.Exec(`UPDATE subtasks SET mcp_status=CASE WHEN ?='created' THEN 'created' WHEN mcp_status='created' THEN mcp_status ELSE 'blocked' END, updated_at=? WHERE workspace_id=? AND parent_task_id=? AND subtask_id=?`,
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
		if err := tx.QueryRow(`SELECT parent_task_id, subtask_id FROM subtasks WHERE workspace_id=? AND child_task_id=? LIMIT 1`, ev.WorkspaceID, ev.ChildTaskID).Scan(&parent, &subtask); err == nil {
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
		if err := tx.QueryRow(`SELECT parent_task_id, subtask_id FROM subtasks WHERE workspace_id=? AND child_task_id=? LIMIT 1`, ev.WorkspaceID, ev.TaskID).Scan(&parent, &subtask); err == nil {
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

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func generatedEventID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func taskSummary(tx *sql.Tx, workspaceID, taskID string) string {
	var summary string
	_ = tx.QueryRow(`SELECT summary FROM tasks WHERE workspace_id=? AND task_id=?`, workspaceID, taskID).Scan(&summary)
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

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
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
