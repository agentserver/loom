package observerweb

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

const maxEventBodyBytes = 1 << 20

type Store interface {
	ValidateToken(token string) (observerstore.Agent, bool, error)
	Ingest(ev observer.Event) error
	ListTasks() ([]observerstore.TaskView, error)
	ListEvents(taskID string) ([]observer.Event, error)
	CreateArtifact(observerstore.ArtifactCreate) (observerstore.Artifact, error)
	RequestArtifact(workspaceID, requesterAgentID, artifactID string) (observerstore.ArtifactRequest, error)
	ListArtifactRequests(workspaceID, ownerAgentID string) ([]observerstore.ArtifactRequest, error)
	StoreArtifactContent(workspaceID, ownerAgentID, artifactID, mime string, body io.Reader) error
	OpenArtifactContent(workspaceID, artifactID string) (observerstore.ArtifactContent, error)
	CreateWrite(observerstore.WriteCreate) (observerstore.Write, error)
	StoreWriteContent(workspaceID, writerAgentID, writeID, mime string, body io.Reader) error
	UpdateWriteTaskID(workspaceID, ownerAgentID, writeID, taskID string) error
	ListCompletedWrites(workspaceID, ownerAgentID, taskID string) ([]observerstore.Write, error)
	SaveTaskContract(observerstore.TaskContractRecord) error
	GetTaskContract(workspaceID, taskID string) (observerstore.TaskContractRecord, error)
	SaveResourceSnapshot(observerstore.ResourceSnapshotRecord) error
	GetLatestResourceSnapshot(workspaceID string) (observerstore.ResourceSnapshotRecord, error)
}

func New(s Store) http.Handler {
	h := &handler{s: s}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", h.postEvent)
	mux.HandleFunc("/api/tasks", h.apiTasks)
	mux.HandleFunc("/api/artifacts", h.artifacts)
	mux.HandleFunc("/api/artifacts/", h.artifactRouter)
	mux.HandleFunc("/api/artifact-requests", h.artifactRequests)
	mux.HandleFunc("/api/write-tokens", h.writeTokens)
	mux.HandleFunc("/api/writes", h.writes)
	mux.HandleFunc("/api/writes/", h.writeRouter)
	mux.HandleFunc("/api/task-contracts", h.taskContracts)
	mux.HandleFunc("/api/task-contracts/", h.taskContractByID)
	mux.HandleFunc("/api/resource-snapshots", h.resourceSnapshots)
	mux.HandleFunc("/api/resource-snapshots/latest", h.latestResourceSnapshot)
	mux.HandleFunc("/drivers", h.page("Drivers", "drivers"))
	mux.HandleFunc("/masters", h.page("Masters", "masters"))
	mux.HandleFunc("/slaves", h.page("Slaves", "slaves"))
	mux.HandleFunc("/", h.dashboard)
	return mux
}

type handler struct {
	s Store
}

func (h *handler) postEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.apiEvents(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}
	agent, ok, err := h.s.ValidateToken(token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}

	var ev observer.Event
	r.Body = http.MaxBytesReader(w, r.Body, maxEventBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&ev); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); err != io.EOF {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if ev.WorkspaceID != agent.WorkspaceID || ev.AgentID != agent.ID || ev.AgentRole != agent.Role {
		http.Error(w, "workspace or agent mismatch", http.StatusForbidden)
		return
	}
	if err := h.s.Ingest(ev); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *handler) apiEvents(w http.ResponseWriter, r *http.Request) {
	events, err := h.s.ListEvents(r.URL.Query().Get("task_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(events); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func bearerToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	return token, token != ""
}

func (h *handler) authenticate(w http.ResponseWriter, r *http.Request) (observerstore.Agent, bool) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
		ok = token != ""
		if !ok {
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return observerstore.Agent{}, false
		}
	}
	agent, ok, err := h.s.ValidateToken(token)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return observerstore.Agent{}, false
	}
	if !ok {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return observerstore.Agent{}, false
	}
	return agent, true
}

func absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = strings.Split(xf, ",")[0]
	}
	return scheme + "://" + r.Host + path
}

func (h *handler) artifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		MIME   string `json:"mime"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
		Mode   string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Kind == "" {
		req.Kind = "file"
	}
	art, err := h.s.CreateArtifact(observerstore.ArtifactCreate{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		Path: req.Path, Kind: req.Kind, MIME: req.MIME, Bytes: req.Bytes, SHA256: req.SHA256,
		State: observerstore.ArtifactStateRegistered,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"artifact_id": art.ID,
		"url":         absoluteURL(r, "/api/artifacts/"+url.PathEscape(art.ID)),
		"state":       art.State,
	})
}

func (h *handler) artifactRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/artifacts/")
	if strings.HasSuffix(rest, "/content") {
		id := strings.TrimSuffix(rest, "/content")
		h.putArtifactContent(w, r, id)
		return
	}
	if strings.HasSuffix(rest, "/list") || strings.HasSuffix(rest, "/blob") {
		// Directory lazy list/blob requests use the same pending request path in
		// v1; rel paths are carried by the URL query for driver-side resolution.
		id := strings.TrimSuffix(strings.TrimSuffix(rest, "/list"), "/blob")
		h.getArtifact(w, r, id)
		return
	}
	h.getArtifact(w, r, rest)
}

func (h *handler) getArtifact(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	content, err := h.s.OpenArtifactContent(agent.WorkspaceID, id)
	if err == nil {
		if content.MIME != "" {
			w.Header().Set("Content-Type", content.MIME)
		}
		defer content.Body.Close()
		io.Copy(w, content.Body) //nolint:errcheck
		return
	}
	req, err := h.s.RequestArtifact(agent.WorkspaceID, agent.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "2")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"state":       req.State,
		"artifact_id": req.ArtifactID,
		"request_id":  req.RequestID,
	})
}

func (h *handler) putArtifactContent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if err := h.s.StoreArtifactContent(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) artifactRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	reqs, err := h.s.ListArtifactRequests(agent.WorkspaceID, agent.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if reqs == nil {
		reqs = []observerstore.ArtifactRequest{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"requests": reqs})
}

func (h *handler) writeTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskID    string `json:"task_id"`
		Path      string `json:"path"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	wr, err := h.s.CreateWrite(observerstore.WriteCreate{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		TaskID: req.TaskID, Path: req.Path, Overwrite: req.Overwrite,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"write_id": wr.ID,
		"put_url":  absoluteURL(r, "/api/writes/"+url.PathEscape(wr.ID)),
	})
}

func (h *handler) writeRouter(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch {
		h.patchWrite(w, r)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/writes/")
	if err := h.s.StoreWriteContent(agent.WorkspaceID, agent.ID, id, r.Header.Get("Content-Type"), r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) patchWrite(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/writes/")
	if err := h.s.UpdateWriteTaskID(agent.WorkspaceID, agent.ID, id, req.TaskID); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) writes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	taskID := r.URL.Query().Get("task_id")
	rows, err := h.s.ListCompletedWrites(agent.WorkspaceID, agent.ID, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []observerstore.Write{}
	}
	type writeResponse struct {
		WriteID       string `json:"write_id"`
		Path          string `json:"path"`
		Overwrite     bool   `json:"overwrite"`
		State         string `json:"state"`
		MIME          string `json:"mime,omitempty"`
		Bytes         int64  `json:"bytes,omitempty"`
		SHA256        string `json:"sha256,omitempty"`
		Content       []byte `json:"content"`
		WriterAgentID string `json:"writer_agent_id,omitempty"`
	}
	resp := make([]writeResponse, len(rows))
	for i, row := range rows {
		resp[i] = writeResponse{
			WriteID: row.ID, Path: row.Path, Overwrite: row.Overwrite,
			State: row.State, MIME: row.MIME, Bytes: row.Bytes, SHA256: row.SHA256,
			Content: row.Content, WriterAgentID: row.WriterAgentID,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"writes": resp})
}

func (h *handler) taskContracts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		TaskID         string          `json:"task_id"`
		ConversationID string          `json:"conversation_id"`
		Body           json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	record := observerstore.TaskContractRecord{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		TaskID: req.TaskID, ConversationID: req.ConversationID, Body: req.Body,
	}
	if err := h.s.SaveTaskContract(record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	got, err := h.s.GetTaskContract(agent.WorkspaceID, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(got)
}

func (h *handler) taskContractByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, "/api/task-contracts/")
	got, err := h.s.GetTaskContract(agent.WorkspaceID, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if got.OwnerAgentID != agent.ID && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(got)
}

func (h *handler) resourceSnapshots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		SnapshotID string          `json:"snapshot_id"`
		Body       json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	record := observerstore.ResourceSnapshotRecord{
		WorkspaceID: agent.WorkspaceID, OwnerAgentID: agent.ID,
		SnapshotID: req.SnapshotID, Body: req.Body,
	}
	if err := h.s.SaveResourceSnapshot(record); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(record)
}

func (h *handler) latestResourceSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agent, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if agent.Role != observer.RoleDriver && agent.Role != observer.RoleMaster {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	got, err := h.s.GetLatestResourceSnapshot(agent.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(got)
}

func (h *handler) apiTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tasks, err := h.s.ListTasks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(tasks); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	h.renderPage(w, r, "Dashboard", "dashboard")
}

func (h *handler) page(title, view string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.renderPage(w, r, title, view)
	}
}

func (h *handler) renderPage(w http.ResponseWriter, r *http.Request, title, view string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tasks, err := h.s.ListTasks()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tasks = filterTasksForView(tasks, view)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, map[string]interface{}{
		"Title": title,
		"View":  view,
		"Tasks": tasks,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func filterTasksForView(tasks []observerstore.TaskView, view string) []observerstore.TaskView {
	if view == "dashboard" {
		return tasks
	}
	out := make([]observerstore.TaskView, 0, len(tasks))
	for _, task := range tasks {
		switch view {
		case "drivers":
			if task.DriverID != "" {
				out = append(out, task)
			}
		case "masters":
			if task.MasterID != "" || len(task.Subtasks) > 0 {
				out = append(out, task)
			}
		case "slaves":
			if hasSlaveActivity(task) {
				out = append(out, task)
			}
		default:
			out = append(out, task)
		}
	}
	return out
}

func hasSlaveActivity(task observerstore.TaskView) bool {
	if task.SlaveID != "" {
		return true
	}
	if task.DriverID == "" && task.MasterID == "" {
		return true
	}
	for _, subtask := range task.Subtasks {
		if subtask.ChildTaskID != "" || subtask.SlaveID != "" {
			return true
		}
	}
	return false
}

var pageTemplate = template.Must(template.New("observer").Parse(`<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
body{font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;background:#f6f8f7;color:#17212b}
header{background:#24313a;color:white;padding:14px 22px}
header strong{display:inline-block;margin-right:28px}
nav{display:inline-block}
nav a{color:white;margin-right:16px;text-decoration:none}
main{padding:20px 22px}
table{border-collapse:collapse;width:100%;background:white;border:1px solid #d8dfda}
th,td{border-bottom:1px solid #d8dfda;padding:9px 10px;text-align:left;vertical-align:top}
th{background:#edf2ef}
.muted{color:#52616b;font-size:13px}
.status{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
.subtask{margin-bottom:4px}
</style>
</head>
<body>
<header><strong>{{.Title}}</strong><nav><a href="/">Dashboard</a><a href="/drivers">Drivers</a><a href="/masters">Masters</a><a href="/slaves">Slaves</a></nav></header>
<main>
{{if eq .View "slaves"}}
<table>
<thead><tr><th>Task / Subtask</th><th>Slave</th><th>Status</th><th>Output / Error</th><th>MCP</th></tr></thead>
<tbody>
{{range .Tasks}}
{{if .Subtasks}}
{{range .Subtasks}}
<tr>
<td>{{.DisplayLabel}}<div class="muted">{{.ChildTaskID}}</div>{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}</td>
<td>{{.SlaveID}}</td>
<td class="status">{{.Status}}</td>
<td>{{if .Output}}{{.Output}}{{else}}{{.Error}}{{end}}</td>
<td>{{if .MCPStatus}}{{.MCPStatus}}{{else}}none{{end}}</td>
</tr>
{{end}}
{{else}}
<tr>
<td>{{.Summary}}<div class="muted">{{.TaskID}}</div>{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}</td>
<td>{{.SlaveID}}</td>
<td class="status">{{.Status}}</td>
<td>{{if .Output}}{{.Output}}{{else}}{{.Error}}{{end}}</td>
<td>{{if .MCPStatus}}{{.MCPStatus}}{{else}}none{{end}}</td>
</tr>
{{end}}
{{else}}
<tr><td colspan="5" class="muted">No slave activity observed yet.</td></tr>
{{end}}
</tbody>
</table>
{{else if eq .View "masters"}}
<table>
<thead><tr><th>Task</th><th>Status</th><th>Master</th><th>Decomposition</th><th>MCP</th></tr></thead>
<tbody>
{{range .Tasks}}
<tr>
<td>{{.Summary}}<div class="muted">{{.TaskID}}</div>{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}</td>
<td class="status">{{.Status}}</td>
<td>{{.MasterID}}</td>
<td>{{range .Subtasks}}<div class="subtask">{{.DisplayLabel}} <span class="status">{{.Status}}</span> <span class="muted">{{.SlaveID}}</span>{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}</div>{{end}}</td>
<td>{{if .MCPStatus}}{{.MCPStatus}}{{else}}none{{end}}</td>
</tr>
{{else}}
<tr><td colspan="5" class="muted">No master tasks observed yet.</td></tr>
{{end}}
</tbody>
</table>
{{else}}
<table>
<thead><tr><th>Task</th><th>Status</th><th>Driver</th><th>Master</th><th>Slaves</th><th>MCP</th></tr></thead>
<tbody>
{{range .Tasks}}
<tr>
<td>{{.Summary}}<div class="muted">{{.TaskID}}</div>{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}</td>
<td class="status">{{.Status}}</td>
<td>{{.DriverID}}</td>
<td>{{.MasterID}}</td>
<td>{{range .Subtasks}}<div class="subtask">{{.SlaveID}} <span class="status">{{.Status}}</span>{{if .LatestProgress}}<div class="muted">Progress: {{.LatestProgressPhase}} - {{.LatestProgress}}</div>{{end}}</div>{{end}}</td>
<td>{{if .MCPStatus}}{{.MCPStatus}}{{else}}none{{end}}</td>
</tr>
{{else}}
<tr><td colspan="6" class="muted">No tasks observed yet.</td></tr>
{{end}}
</tbody>
</table>
{{end}}
</main>
</body>
</html>`))
