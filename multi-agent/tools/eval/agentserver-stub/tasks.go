package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// randomHex n → 2n-char hex string; used for task_id
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// createTaskRequest — matches driver-side agentsdk.DelegateTaskRequest (spec §4.5)
type createTaskRequest struct {
	TargetID        string   `json:"target_id"`
	Skill           string   `json:"skill,omitempty"`
	Prompt          string   `json:"prompt"`
	SystemContext   string   `json:"system_context,omitempty"`
	MaxTurns        int      `json:"max_turns,omitempty"`
	MaxBudgetUSD    float64  `json:"max_budget_usd,omitempty"`
	TimeoutSeconds  int      `json:"timeout_seconds,omitempty"`
	DelegationChain []string `json:"delegation_chain,omitempty"`
	RequesterID     string   `json:"requester_id,omitempty"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.TargetID == "" || req.Prompt == "" {
		http.Error(w, "target_id and prompt required", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	target, ok := s.cards[req.TargetID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "target agent not found", http.StatusNotFound)
		return
	}
	if target.WorkspaceID != caller.WorkspaceID {
		http.Error(w, "target agent not in workspace", http.StatusForbidden)
		return
	}
	tid := "task_" + randomHex(16)
	task := &stubTask{
		ID:            tid,
		WorkspaceID:   caller.WorkspaceID,
		RequesterID:   caller.SandboxID,
		TargetID:      req.TargetID,
		Skill:         req.Skill,
		Prompt:        req.Prompt,
		SystemContext: req.SystemContext,
		MaxTurns:      req.MaxTurns,
		MaxBudgetUSD:  req.MaxBudgetUSD,
		Timeout:       req.TimeoutSeconds,
		Status:        "pending",
		CreatedAt:     time.Now().UTC(),
	}
	s.mu.Lock()
	s.tasks[tid] = task
	s.tasksBySandbox[req.TargetID] = append(s.tasksBySandbox[req.TargetID], tid)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"task_id":    tid,
		"session_id": "",
		"status":     "pending",
	})
}

// pollTaskEntry matches the shape driver-side / poller-side clients parse.
type pollTaskEntry struct {
	TaskID        string  `json:"task_id"`
	Prompt        string  `json:"prompt"`
	SystemContext string  `json:"system_context"`
	SessionID     string  `json:"session_id,omitempty"`
	MaxTurns      int     `json:"max_turns"`
	MaxBudgetUSD  float64 `json:"max_budget_usd"`
}

const pollBatchSize = 5

func (s *Server) handlePollTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	requested := r.URL.Query().Get("sandbox_id")
	if requested != "" && requested != caller.SandboxID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	sid := caller.SandboxID

	s.mu.Lock()
	pending := s.tasksBySandbox[sid]
	assigned := make([]pollTaskEntry, 0, pollBatchSize)
	remaining := pending[:0]
	for _, tid := range pending {
		if len(assigned) < pollBatchSize {
			t, ok := s.tasks[tid]
			if !ok || t.Status != "pending" {
				continue
			}
			t.Status = "assigned"
			assigned = append(assigned, pollTaskEntry{
				TaskID:        t.ID,
				Prompt:        t.Prompt,
				SystemContext: t.SystemContext,
				SessionID:     t.SessionID,
				MaxTurns:      t.MaxTurns,
				MaxBudgetUSD:  t.MaxBudgetUSD,
			})
		} else {
			remaining = append(remaining, tid)
		}
	}
	s.tasksBySandbox[sid] = remaining
	s.mu.Unlock()

	if len(assigned) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(assigned)
}

// updateStatusRequest — accepts both loom poller path (`result`) and SDK path (`output`).
type updateStatusRequest struct {
	Status        string          `json:"status"`
	Result        json.RawMessage `json:"result,omitempty"`
	Output        string          `json:"output,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`
	TotalCostUSD  float64         `json:"total_cost_usd,omitempty"`
	NumTurns      int             `json:"num_turns,omitempty"`
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Path: /api/agent/tasks/{id}/status
	path := strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/")
	tid := strings.TrimSuffix(path, "/status")
	if tid == "" || strings.Contains(tid, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	var req updateStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	task, ok := s.tasks[tid]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if task.TargetID != caller.SandboxID {
		s.mu.Unlock()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	task.Status = req.Status
	if len(req.Result) > 0 {
		task.Result = req.Result
	}
	if req.Output != "" {
		task.Output = req.Output
	}
	if req.FailureReason != "" {
		task.FailureReason = req.FailureReason
	}
	if req.TotalCostUSD > 0 {
		task.TotalCostUSD = req.TotalCostUSD
	}
	if req.NumTurns > 0 {
		task.NumTurns = req.NumTurns
	}
	if req.Status == "completed" || req.Status == "failed" || req.Status == "cancelled" {
		task.CompletedAt = time.Now().UTC()
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	caller, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	tid := strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/")
	if tid == "" || strings.Contains(tid, "/") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	task, ok := s.tasks[tid]
	if !ok || task.WorkspaceID != caller.WorkspaceID {
		s.mu.RUnlock()
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	resp := map[string]any{
		"task_id":      task.ID,
		"workspace_id": task.WorkspaceID,
		"requester_id": task.RequesterID,
		"target_id":    task.TargetID,
		"prompt":       task.Prompt,
		"status":       task.Status,
		"num_turns":    task.NumTurns,
		"created_at":   task.CreatedAt.Format(time.RFC3339),
	}
	if task.Skill != "" {
		resp["skill"] = task.Skill
	}
	if task.SessionID != "" {
		resp["session_id"] = task.SessionID
	}
	if task.TotalCostUSD > 0 {
		resp["total_cost_usd"] = task.TotalCostUSD
	}
	if len(task.Result) > 0 {
		resp["result"] = task.Result
	}
	if task.FailureReason != "" {
		resp["failure_reason"] = task.FailureReason
	}
	if !task.CompletedAt.IsZero() {
		resp["completed_at"] = task.CompletedAt.Format(time.RFC3339)
	}
	if r.URL.Query().Get("include_output") == "true" {
		if task.Output != "" {
			resp["output"] = task.Output
		} else if len(task.Result) > 0 {
			// Fallback: unwrap string-JSON result into output (spec §4.8, fix plan review r2 P1-3).
			var strval string
			if err := json.Unmarshal(task.Result, &strval); err == nil {
				resp["output"] = strval
			}
		}
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// dispatchTaskByID routes /api/agent/tasks/{id} vs /api/agent/tasks/{id}/status.
// /api/agent/tasks (no trailing slash) is handled by handleCreateTask, and
// /api/agent/tasks/poll by handlePollTasks — both mounted separately.
func (s *Server) dispatchTaskByID(w http.ResponseWriter, r *http.Request) {
	suffix := strings.TrimPrefix(r.URL.Path, "/api/agent/tasks/")
	if strings.HasSuffix(suffix, "/status") {
		s.handleUpdateStatus(w, r)
		return
	}
	s.handleGetTask(w, r)
}
