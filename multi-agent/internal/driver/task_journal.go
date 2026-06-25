package driver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

const (
	taskJournalEvent      = "delegate_task"
	defaultTaskJournalMax = 50
	maxTaskJournalLimit   = 500
)

type TaskRecord struct {
	TS                string                  `json:"-"`
	Event             string                  `json:"-"`
	Tool              string                  `json:"-"`
	TaskID            string                  `json:"-"`
	TargetID          string                  `json:"-"`
	TargetDisplayName string                  `json:"-"`
	Skill             string                  `json:"-"`
	Status            string                  `json:"-"`
	Wait              bool                    `json:"-"`
	TimeoutSec        int                     `json:"-"`
	ChildAgentID      string                  `json:"-"`
	Terminal          bool                    `json:"-"`
	SessionRef        agentbackend.SessionRef `json:"-"`
	ChildSessionRef   agentbackend.SessionRef `json:"-"`
}

// recordWire is the on-disk JSON shape. SessionRef and ChildSessionRef are
// flattened to sibling fields (session_id + bridge_session_id +
// child_session_id + child_bridge_session_id) — encoding/json does not
// flatten nested struct fields into siblings on its own, so the marshal
// path explicitly maps SessionRef.Backend / .Bridge to the wire keys.
//
// Read path uses the bridge-id prefix classifier ("^cse_" → Bridge, else
// Backend) when ONLY the legacy single field is present. Modern rows that
// carry the explicit bridge_session_id sibling bypass the classifier.
type recordWire struct {
	TS                   string `json:"ts"`
	Event                string `json:"event"`
	Tool                 string `json:"tool"`
	TaskID               string `json:"task_id"`
	SessionID            string `json:"session_id,omitempty"`
	BridgeSessionID      string `json:"bridge_session_id,omitempty"`
	TargetID             string `json:"target_id,omitempty"`
	TargetDisplayName    string `json:"target_display_name,omitempty"`
	Skill                string `json:"skill,omitempty"`
	Status               string `json:"status,omitempty"`
	Wait                 bool   `json:"wait"`
	TimeoutSec           int    `json:"timeout_sec,omitempty"`
	ChildSessionID       string `json:"child_session_id,omitempty"`
	ChildBridgeSessionID string `json:"child_bridge_session_id,omitempty"`
	ChildAgentID         string `json:"child_agent_id,omitempty"`
	Terminal             bool   `json:"terminal,omitempty"`
}

// MarshalJSON flattens SessionRef.Backend/.Bridge as sibling JSON fields.
func (r TaskRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(recordWire{
		TS:                   r.TS,
		Event:                r.Event,
		Tool:                 r.Tool,
		TaskID:               r.TaskID,
		SessionID:            r.SessionRef.Backend,
		BridgeSessionID:      r.SessionRef.Bridge,
		TargetID:             r.TargetID,
		TargetDisplayName:    r.TargetDisplayName,
		Skill:                r.Skill,
		Status:               r.Status,
		Wait:                 r.Wait,
		TimeoutSec:           r.TimeoutSec,
		ChildSessionID:       r.ChildSessionRef.Backend,
		ChildBridgeSessionID: r.ChildSessionRef.Bridge,
		ChildAgentID:         r.ChildAgentID,
		Terminal:             r.Terminal,
	})
}

// UnmarshalJSON inflates the wire shape and reconstructs SessionRef values.
// Modern rows: explicit bridge_session_id is present → fields go to their
// explicit targets. Legacy rows: only session_id is present → classifier
// routes ^cse_ to Bridge, anything else to Backend.
func (r *TaskRecord) UnmarshalJSON(data []byte) error {
	var w recordWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	r.TS = w.TS
	r.Event = w.Event
	r.Tool = w.Tool
	r.TaskID = w.TaskID
	r.TargetID = w.TargetID
	r.TargetDisplayName = w.TargetDisplayName
	r.Skill = w.Skill
	r.Status = w.Status
	r.Wait = w.Wait
	r.TimeoutSec = w.TimeoutSec
	r.ChildAgentID = w.ChildAgentID
	r.Terminal = w.Terminal
	r.SessionRef = classifyLegacyID(w.SessionID, w.BridgeSessionID)
	r.ChildSessionRef = classifyLegacyID(w.ChildSessionID, w.ChildBridgeSessionID)
	r.ChildSessionRef.AgentID = w.ChildAgentID
	return nil
}

// classifyLegacyID routes a journal row's id pair into a SessionRef.
//   - If bridge is non-empty, this is a modern row: backend → Backend, bridge → Bridge.
//   - If only id is set: ^cse_ prefix → Bridge, else → Backend (legacy classifier).
//   - If both empty: zero ref.
func classifyLegacyID(id, bridge string) agentbackend.SessionRef {
	if bridge != "" {
		// Modern row: explicit fields take precedence.
		return agentbackend.SessionRef{Backend: id, Bridge: bridge}
	}
	if id == "" {
		return agentbackend.SessionRef{}
	}
	if strings.HasPrefix(id, "cse_") {
		return agentbackend.SessionRef{Bridge: id}
	}
	return agentbackend.SessionRef{Backend: id}
}

type TaskJournal struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

func NewTaskJournal(path string) (*TaskJournal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir task journal dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open task journal: %w", err)
	}
	return &TaskJournal{f: f, path: path}, nil
}

func (j *TaskJournal) Path() string { return j.path }

func (j *TaskJournal) Append(rec TaskRecord) error {
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if rec.Event == "" {
		rec.Event = taskJournalEvent
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal task journal record: %w", err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write task journal: %w", err)
	}
	if err := j.f.Sync(); err != nil {
		return fmt.Errorf("sync task journal: %w", err)
	}
	return nil
}

func (j *TaskJournal) Recent(limit int, taskID string) ([]TaskRecord, error) {
	records, _, err := j.RecentWithWarnings(limit, taskID)
	return records, err
}

// LatestByTaskID returns the most recent record for taskID (newest-first scan),
// or ok=false if none. Used at result time to read the delegation-time
// ChildAgentID/Tool/TargetID/Skill when appending the terminal record.
func (j *TaskJournal) LatestByTaskID(taskID string) (TaskRecord, bool) {
	recs, err := j.Recent(500, taskID)
	if err != nil || len(recs) == 0 {
		return TaskRecord{}, false
	}
	return recs[0], true // Recent is newest-first
}

func (j *TaskJournal) RecentWithWarnings(limit int, taskID string) ([]TaskRecord, []string, error) {
	limit = normalizeTaskJournalLimit(limit)
	j.mu.Lock()
	defer j.mu.Unlock()

	f, err := os.Open(j.path)
	if os.IsNotExist(err) {
		return []TaskRecord{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open task journal for read: %w", err)
	}
	defer f.Close()

	records := []TaskRecord{}
	warnings := []string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var rec TaskRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped malformed task journal line %d: %v", line, err))
			continue
		}
		if taskID != "" && rec.TaskID != taskID {
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("read task journal: %w", err)
	}

	// Scoped dedup: if any record for a given task_id has Terminal=true, drop
	// the non-terminal records for that same task_id. Records for task_ids that
	// have no terminal counterpart (e.g. multiple resume_task rows still running)
	// are NOT affected — they all remain visible.
	terminalTaskIDs := make(map[string]bool)
	for _, rec := range records {
		if rec.Terminal && rec.TaskID != "" {
			terminalTaskIDs[rec.TaskID] = true
		}
	}
	if len(terminalTaskIDs) > 0 {
		filtered := records[:0]
		for _, rec := range records {
			if !rec.Terminal && terminalTaskIDs[rec.TaskID] {
				continue // hide non-terminal when terminal exists for same task_id
			}
			filtered = append(filtered, rec)
		}
		records = filtered
	}

	for i, k := 0, len(records)-1; i < k; i, k = i+1, k-1 {
		records[i], records[k] = records[k], records[i]
	}
	if len(records) > limit {
		records = records[:limit]
	}
	return records, warnings, nil
}

func normalizeTaskJournalLimit(limit int) int {
	if limit <= 0 {
		return defaultTaskJournalMax
	}
	if limit > maxTaskJournalLimit {
		return maxTaskJournalLimit
	}
	return limit
}

func (j *TaskJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}
