package observerweb

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/yourorg/multi-agent/internal/observer"
	"github.com/yourorg/multi-agent/internal/observerstore"
)

const maxEventBodyBytes = 1 << 20

type Store interface {
	ValidateToken(token string) (observerstore.Agent, bool, error)
	Ingest(ev observer.Event) error
	ListTasks() ([]observerstore.TaskView, error)
}

func New(s Store) http.Handler {
	h := &handler{s: s}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", h.postEvent)
	mux.HandleFunc("/api/tasks", h.apiTasks)
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

func bearerToken(auth string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	return token, token != ""
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
<td>{{.DisplayLabel}}<div class="muted">{{.ChildTaskID}}</div></td>
<td>{{.SlaveID}}</td>
<td class="status">{{.Status}}</td>
<td>{{if .Output}}{{.Output}}{{else}}{{.Error}}{{end}}</td>
<td>{{if .MCPStatus}}{{.MCPStatus}}{{else}}none{{end}}</td>
</tr>
{{end}}
{{else}}
<tr>
<td>{{.Summary}}<div class="muted">{{.TaskID}}</div></td>
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
<td>{{.Summary}}<div class="muted">{{.TaskID}}</div></td>
<td class="status">{{.Status}}</td>
<td>{{.MasterID}}</td>
<td>{{range .Subtasks}}<div class="subtask">{{.DisplayLabel}} <span class="status">{{.Status}}</span> <span class="muted">{{.SlaveID}}</span></div>{{end}}</td>
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
<td>{{.Summary}}<div class="muted">{{.TaskID}}</div></td>
<td class="status">{{.Status}}</td>
<td>{{.DriverID}}</td>
<td>{{.MasterID}}</td>
<td>{{range .Subtasks}}<div class="subtask">{{.SlaveID}} <span class="status">{{.Status}}</span></div>{{end}}</td>
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
