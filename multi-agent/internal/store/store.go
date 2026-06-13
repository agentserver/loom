package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	subs map[string][]chan Event
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, subs: make(map[string][]chan Event)}, nil
}

func (s *Store) Close() error      { return s.db.Close() }
func (s *Store) DB() *sql.DB       { return s.db } // test-only accessor

type Task struct {
	ID            string
	Skill         string
	Prompt        string
	SystemContext string
	TimeoutSec    int
}

type TaskRow struct {
	ID         string
	Skill      string
	Prompt     string
	Status     string
	Output     string
	Error      string
	CreatedAt  string
	StartedAt  string
	FinishedAt string
}

type Chunk struct {
	Type EventType
	Data string
	TS   string
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) Insert(t Task) error {
	_, err := s.db.Exec(
		`INSERT INTO tasks(id,skill,prompt,status,created_at) VALUES(?,?,?,?,?)`,
		t.ID, t.Skill, t.Prompt, "assigned", nowUTC(),
	)
	return err
}

// InsertIfAbsent inserts a task row if no row with this ID exists, returning
// (true, nil). On primary-key conflict returns (false, nil) — the caller can
// then look up the existing row to decide whether to replay (completed) or
// silently skip (running/assigned). Used by master + slave dispatch entrypoints
// to make task delivery idempotent: a re-delivered task ID never spawns a
// second executor.
func (s *Store) InsertIfAbsent(t Task) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO tasks(id,skill,prompt,status,created_at) VALUES(?,?,?,?,?)`,
		t.ID, t.Skill, t.Prompt, "assigned", nowUTC(),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) MarkRunning(id string) error {
	res, err := s.db.Exec(
		`UPDATE tasks SET status='running', started_at=? WHERE id=? AND status='assigned'`,
		nowUTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %s not in assigned state", id)
	}
	return nil
}

func (s *Store) Complete(id, output string) error {
	res, err := s.db.Exec(
		`UPDATE tasks SET status='completed', output=?, finished_at=? WHERE id=? AND status IN ('assigned','running')`,
		output, nowUTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %s not in active state", id)
	}
	return nil
}

func (s *Store) Fail(id, reason string) error {
	res, err := s.db.Exec(
		`UPDATE tasks SET status='failed', error=?, finished_at=? WHERE id=? AND status IN ('assigned','running')`,
		reason, nowUTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %s not in active state", id)
	}
	return nil
}

func (s *Store) GetTaskWithChunks(id string) (TaskRow, []Chunk, error) {
	var r TaskRow
	var output, errStr, started, finished sql.NullString
	err := s.db.QueryRow(
		`SELECT id,skill,prompt,status,output,error,created_at,started_at,finished_at FROM tasks WHERE id=?`, id,
	).Scan(&r.ID, &r.Skill, &r.Prompt, &r.Status, &output, &errStr, &r.CreatedAt, &started, &finished)
	if err != nil {
		return r, nil, err
	}
	r.Output, r.Error, r.StartedAt, r.FinishedAt = output.String, errStr.String, started.String, finished.String

	rows, err := s.db.Query(`SELECT type, data, ts FROM task_chunks WHERE task_id=? ORDER BY id ASC`, id)
	if err != nil {
		return r, nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		var t string
		if err := rows.Scan(&t, &c.Data, &c.TS); err != nil {
			return r, nil, err
		}
		c.Type = EventType(t)
		chunks = append(chunks, c)
	}
	return r, chunks, rows.Err()
}

func (s *Store) ListTasks(limit, offset int) ([]TaskRow, error) {
	rows, err := s.db.Query(
		`SELECT id,skill,prompt,status,output,error,created_at,started_at,finished_at FROM tasks ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		var r TaskRow
		var output, errStr, started, finished sql.NullString
		if err := rows.Scan(&r.ID, &r.Skill, &r.Prompt, &r.Status, &output, &errStr, &r.CreatedAt, &started, &finished); err != nil {
			return nil, err
		}
		r.Output, r.Error, r.StartedAt, r.FinishedAt = output.String, errStr.String, started.String, finished.String
		out = append(out, r)
	}
	return out, rows.Err()
}

type sink struct {
	s      *Store
	taskID string
}

type Sink interface {
	Write(eventType, data string)
	Close()
}

func (s *Store) ChunkSink(taskID string) Sink { return &sink{s: s, taskID: taskID} }

func (sk *sink) Write(eventType, data string) {
	_, err := sk.s.db.Exec(
		`INSERT INTO task_chunks(task_id,ts,type,data) VALUES(?,?,?,?)`,
		sk.taskID, nowUTC(), eventType, data,
	)
	if err != nil {
		// store failure is best-effort for chunks; don't abort task.
		return
	}
	sk.s.publish(sk.taskID, Event{Type: EventType(eventType), Data: data})
}

func (sk *sink) Close() {
	sk.s.publish(sk.taskID, Event{Type: EventDone})
	sk.s.unsubscribeAll(sk.taskID)
}

func (s *Store) Subscribe(taskID string) (<-chan Event, func()) {
	ch := make(chan Event, 32)
	s.mu.Lock()
	s.subs[taskID] = append(s.subs[taskID], ch)
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, c := range s.subs[taskID] {
			if c == ch {
				s.subs[taskID] = append(s.subs[taskID][:i], s.subs[taskID][i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, cancel
}

func (s *Store) publish(taskID string, e Event) {
	s.mu.Lock()
	subs := append([]chan Event(nil), s.subs[taskID]...)
	s.mu.Unlock()
	for _, c := range subs {
		select {
		case c <- e:
		default:
			// slow consumer — drop rather than block writer
		}
	}
}

func (s *Store) unsubscribeAll(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.subs[taskID] {
		close(c)
	}
	delete(s.subs, taskID)
}

type PendingAck struct {
	TaskID string
	Status string // "completed" | "failed"
	Reason string // optional
}

func (s *Store) Recover() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM tasks WHERE status IN ('assigned','running')`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		if _, err := tx.Exec(
			`UPDATE tasks SET status='failed', error=?, finished_at=? WHERE id=?`,
			"agent restarted", nowUTC(), id,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO pending_acks(task_id,status,enqueued_at) VALUES(?,?,?)`,
			id, "failed", nowUTC(),
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE sub_tasks SET status='cancelled', finished_at=? WHERE parent_id=? AND status IN ('pending','assigned')`,
			nowUTC(), id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EnqueuePendingAck(id, status string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO pending_acks(task_id,status,enqueued_at) VALUES(?,?,?)`,
		id, status, nowUTC(),
	)
	return err
}

func (s *Store) PopPendingAcks() ([]PendingAck, error) {
	rows, err := s.db.Query(`SELECT pa.task_id, pa.status, COALESCE(t.error,''), COALESCE(t.output,'')
                             FROM pending_acks pa JOIN tasks t ON t.id=pa.task_id
                             ORDER BY pa.enqueued_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingAck
	for rows.Next() {
		var p PendingAck
		var output string
		if err := rows.Scan(&p.TaskID, &p.Status, &p.Reason, &output); err != nil {
			return nil, err
		}
		if p.Status == "completed" {
			p.Reason = output
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePendingAck(id string) error {
	_, err := s.db.Exec(`DELETE FROM pending_acks WHERE task_id=?`, id)
	return err
}

type SubTaskRow struct {
	ParentID, NodeID, TargetID, ChildTaskID, Prompt string
	DependsOn  []string
	Status, Output, Error string
	CreatedAt, StartedAt, FinishedAt string
}

func (s *Store) InsertSubTasks(parentID string, rows []SubTaskRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		deps, _ := json.Marshal(r.DependsOn)
		if r.CreatedAt == "" {
			r.CreatedAt = nowUTC()
		}
		if r.Status == "" {
			r.Status = "pending"
		}
		if _, err := tx.Exec(
			`INSERT INTO sub_tasks(parent_id,node_id,target_id,child_task_id,prompt,depends_on,status,output,error,created_at,started_at,finished_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			parentID, r.NodeID, r.TargetID, nullable(r.ChildTaskID), r.Prompt, string(deps), r.Status,
			nullable(r.Output), nullable(r.Error), r.CreatedAt, nullable(r.StartedAt), nullable(r.FinishedAt),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateSubTask(parentID, nodeID string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	args := make([]interface{}, 0, len(fields)+2)
	for k, v := range fields {
		switch k {
		case "child_task_id", "prompt", "status", "output", "error", "started_at", "finished_at", "target_id":
		default:
			return fmt.Errorf("UpdateSubTask: unknown field %q", k)
		}
		keys = append(keys, k+"=?")
		args = append(args, v)
	}
	args = append(args, parentID, nodeID)
	q := "UPDATE sub_tasks SET " + strings.Join(keys, ",") + " WHERE parent_id=? AND node_id=?"
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("sub_task %s/%s not found", parentID, nodeID)
	}
	return nil
}

func (s *Store) ListSubTasks(parentID string) ([]SubTaskRow, error) {
	rows, err := s.db.Query(
		`SELECT parent_id,node_id,target_id,COALESCE(child_task_id,''),prompt,depends_on,status,
		        COALESCE(output,''),COALESCE(error,''),created_at,COALESCE(started_at,''),COALESCE(finished_at,'')
		 FROM sub_tasks WHERE parent_id=? ORDER BY created_at ASC`,
		parentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubTaskRow
	for rows.Next() {
		var r SubTaskRow
		var depsJSON string
		if err := rows.Scan(&r.ParentID, &r.NodeID, &r.TargetID, &r.ChildTaskID, &r.Prompt, &depsJSON,
			&r.Status, &r.Output, &r.Error, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(depsJSON), &r.DependsOn)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullable(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// Used to silence unused import in case context isn't referenced directly above.
var _ = context.Background
