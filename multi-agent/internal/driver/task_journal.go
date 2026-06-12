package driver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	taskJournalEvent      = "delegate_task"
	defaultTaskJournalMax = 50
	maxTaskJournalLimit   = 500
)

type TaskRecord struct {
	TS                string `json:"ts"`
	Event             string `json:"event"`
	Tool              string `json:"tool"`
	TaskID            string `json:"task_id"`
	SessionID         string `json:"session_id,omitempty"`
	TargetID          string `json:"target_id,omitempty"`
	TargetDisplayName string `json:"target_display_name,omitempty"`
	Skill             string `json:"skill,omitempty"`
	Status            string `json:"status,omitempty"`
	Wait              bool   `json:"wait"`
	TimeoutSec        int    `json:"timeout_sec,omitempty"`
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
