package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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

// observerProgress fetches the 5 progress fields for one task via the tokened
// GET /api/tasks/{task_id}/progress endpoint. Returns a zero value on any
// failure (observer disabled, no live token, network error, non-2xx, decode
// error, task not found) so callers always treat the observer's view as a
// best-effort enrichment of agentserver's TaskInfo.
func (t *Tools) observerProgress(ctx context.Context, taskID string) taskProgress {
	if t == nil || t.cfg == nil || !t.cfg.Observer.Enabled || t.cfg.Observer.URL == "" {
		return taskProgress{}
	}
	if taskID == "" {
		return taskProgress{}
	}
	ts := toTokenSource(t.observer)
	if ts == nil {
		return taskProgress{}
	}
	token := ts.Token()
	if token == "" {
		return taskProgress{}
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	u := strings.TrimRight(t.cfg.Observer.URL, "/") + "/api/tasks/" + url.PathEscape(taskID) + "/progress"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return taskProgress{}
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return taskProgress{}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return taskProgress{}
	}

	var p taskProgress
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return taskProgress{}
	}
	return p
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
