package poller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/dispatch"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/store"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type echoExec struct{}

func (echoExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	sink.Close()
	return executor.Result{Summary: "echo: " + t.Prompt}, nil
}

type stubJ struct{}

func (stubJ) Record(context.Context, executor.Task, executor.Result) error { return nil }

func TestPoller_PollsAndCompletes(t *testing.T) {
	var taskHandedOut atomic.Bool
	var mu sync.Mutex
	var statuses []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/tasks/poll" {
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"task_id":"t1","skill":"chat","prompt":"hi","timeout_seconds":30}]`))
			return
		}
		// /api/agent/tasks/{id}/status
		body, _ := io.ReadAll(r.Body)
		var msg struct{ Status string }
		_ = json.Unmarshal(body, &msg)
		mu.Lock()
		statuses = append(statuses, msg.Status)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	d := dispatch.New(map[string]executor.Executor{"": echoExec{}}, stubJ{}, s, nil)

	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(statuses) >= 2 // running + completed
	}, 15*time.Second, 20*time.Millisecond)

	mu.Lock()
	s0, s1 := statuses[0], statuses[1]
	mu.Unlock()

	require.Equal(t, "running", s0)
	require.Equal(t, "completed", s1)
}

func TestPoller_ExecutesEveryTaskReturnedByPollBatch(t *testing.T) {
	var taskHandedOut atomic.Bool
	var mu sync.Mutex
	statuses := map[string][]string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/agent/tasks/poll":
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"task_id":"t1","skill":"chat","prompt":"first","timeout_seconds":30},
				{"task_id":"t2","skill":"chat","prompt":"second","timeout_seconds":30}
			]`))
			return
		default:
			body, _ := io.ReadAll(r.Body)
			var msg struct{ Status string }
			_ = json.Unmarshal(body, &msg)
			taskID := filepath.Base(filepath.Dir(r.URL.Path))
			mu.Lock()
			statuses[taskID] = append(statuses[taskID], msg.Status)
			mu.Unlock()
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	d := dispatch.New(map[string]executor.Executor{"": echoExec{}}, stubJ{}, s, nil)

	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(statuses["t1"]) >= 2 && len(statuses["t2"]) >= 2
	}, 15*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"running", "completed"}, statuses["t1"])
	require.Equal(t, []string{"running", "completed"}, statuses["t2"])
}

// recordingExec captures the executor.Task it receives so tests can assert
// on per-field plumbing (e.g. that the poller stripped the loom_origin
// marker and surfaced the parsed parent tuple).
type recordingExec struct {
	mu  sync.Mutex
	got executor.Task
}

func (r *recordingExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	r.mu.Lock()
	r.got = t
	r.mu.Unlock()
	sink.Close()
	return executor.Result{Summary: "ok"}, nil
}

func (r *recordingExec) snapshot() executor.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got
}

func TestPoller_LoomOriginParsedIntoTaskParentAndStrippedFromSystemContext(t *testing.T) {
	// The driver stamps a single-line JSON marker into SystemContext; the
	// slave poller must lift it into Task.Parent* and strip it so JSON-prompt
	// skills downstream don't choke on the prefix.
	marker := agentbackend.BuildLoomOrigin("drv-1", "prod-driver", "thread-parent")
	payload := marker + "trailing preamble"

	var taskHandedOut atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/tasks/poll" {
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			resp := []map[string]interface{}{{
				"task_id":         "t-loom",
				"skill":           "chat",
				"prompt":          "do work",
				"system_context":  payload,
				"timeout_seconds": 30,
			}}
			b, _ := json.Marshal(resp)
			w.Write(b)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	rec := &recordingExec{}
	d := dispatch.New(map[string]executor.Executor{"": rec, "chat": rec}, stubJ{}, s, nil)

	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		return rec.snapshot().ID == "t-loom"
	}, 15*time.Second, 20*time.Millisecond)

	got := rec.snapshot()
	require.Equal(t, "thread-parent", got.ParentSessionID, "ParentSessionID populated from marker")
	require.Equal(t, "drv-1", got.ParentAgentID, "ParentAgentID populated from marker")
	require.Equal(t, "prod-driver", got.ParentDisplayName, "ParentDisplayName populated from marker")
	require.NotContains(t, got.SystemContext, "loom_origin", "marker stripped from SystemContext")
	require.Contains(t, got.SystemContext, "trailing preamble", "non-marker preamble preserved")
}
