package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/yourorg/salve_agent/internal/config"
	"github.com/yourorg/salve_agent/internal/store"
)

type Handler struct {
	s          *store.Store
	journalDir string
	cfg        *config.Config
	started    time.Time
}

func NewHandler(s *store.Store, journalDir string, cfg *config.Config) http.Handler {
	h := &Handler{s: s, journalDir: journalDir, cfg: cfg, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/state", h.state)
	mux.HandleFunc("/tasks", h.listTasks)
	mux.HandleFunc("/tasks/", h.taskRouter)
	mux.HandleFunc("/", h.dashboard)
	return mux
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"ok":true,"uptime_s":%d}`, int(time.Since(h.started).Seconds()))
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	if h.journalDir == "" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		return
	}
	data, err := os.ReadFile(filepath.Join(h.journalDir, "CURRENT_STATE.md"))
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write(data)
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.s.ListTasks(50, 0)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func (h *Handler) taskRouter(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/tasks/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	if len(parts) == 2 {
		switch parts[1] {
		case "stream":
			h.stream(w, r, id)
			return
		case "children":
			h.children(w, r, id)
			return
		}
	}
	h.taskDetail(w, r, id)
}

func (h *Handler) taskDetail(w http.ResponseWriter, r *http.Request, id string) {
	row, chunks, err := h.s.GetTaskWithChunks(id)
	if err != nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	children, _ := h.s.ListSubTasks(id)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"task": row, "chunks": chunks, "children": children,
	})
}

func (h *Handler) children(w http.ResponseWriter, r *http.Request, id string) {
	rows, err := h.s.ListSubTasks(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rows == nil {
		rows = []store.SubTaskRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rows)
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Subscribe before fetching chunks to avoid missing events published
	// between GetTaskWithChunks and Subscribe.
	ch, cancel := h.s.Subscribe(id)
	defer cancel()

	row, chunks, err := h.s.GetTaskWithChunks(id)
	if err != nil {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	for _, c := range chunks {
		writeSSE(w, string(c.Type), c.Data)
	}
	flusher.Flush()

	if row.Status == "completed" || row.Status == "failed" {
		writeSSE(w, "done", "")
		flusher.Flush()
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				writeSSE(w, "done", "")
				flusher.Flush()
				return
			}
			writeSSE(w, string(ev.Type), ev.Data)
			flusher.Flush()
			if ev.Type == store.EventDone {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, ev, data string) {
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, strings.ReplaceAll(data, "\n", "\\n"))
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	tasks, _ := h.s.ListTasks(20, 0)
	state := ""
	if h.journalDir != "" {
		b, _ := os.ReadFile(filepath.Join(h.journalDir, "CURRENT_STATE.md"))
		state = string(b)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1>", h.cfg.Discovery.DisplayName, h.cfg.Discovery.DisplayName)
	fmt.Fprintf(w, "<h2>Tasks</h2><ul>")
	for _, t := range tasks {
		fmt.Fprintf(w, "<li><a href=\"%s\">%s</a> [%s] %s</li>",
			path.Join("/tasks", t.ID), t.ID, t.Status, htmlEscape(t.Skill))
	}
	fmt.Fprintf(w, "</ul><h2>State</h2><pre>%s</pre>", htmlEscape(state))
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
