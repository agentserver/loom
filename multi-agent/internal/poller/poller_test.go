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
			w.Write([]byte(`{"task_id":"t1","skill":"chat","prompt":"hi","timeout_seconds":30}`))
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
	d := dispatch.New(map[string]executor.Executor{"": echoExec{}}, stubJ{}, s)

	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(statuses) >= 2 // running + completed
	}, time.Second, 20*time.Millisecond)

	mu.Lock()
	s0, s1 := statuses[0], statuses[1]
	mu.Unlock()

	require.Equal(t, "running", s0)
	require.Equal(t, "completed", s1)
}
