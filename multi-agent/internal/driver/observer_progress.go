package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type taskProgress struct {
	LatestProgress      string `json:"latest_progress"`
	LatestProgressPhase string `json:"latest_progress_phase"`
	LatestProgressAt    string `json:"latest_progress_at"`
	FinalOutput         string `json:"final_output"`
	IsFinal             bool   `json:"is_final"`
}

func (t *Tools) observerProgress(ctx context.Context, taskID string) taskProgress {
	if t == nil || t.cfg == nil || !t.cfg.Observer.Enabled || t.cfg.Observer.URL == "" {
		return taskProgress{}
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(t.cfg.Observer.URL, "/")+"/api/tasks", nil)
	if err != nil {
		return taskProgress{}
	}

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return taskProgress{}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return taskProgress{}
	}

	var tasks []struct {
		TaskID string `json:"task_id"`
		taskProgress
	}
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return taskProgress{}
	}
	for _, task := range tasks {
		if task.TaskID == taskID {
			return task.taskProgress
		}
	}
	return taskProgress{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
