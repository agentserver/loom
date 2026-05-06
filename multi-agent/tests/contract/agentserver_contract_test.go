//go:build contract

package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/dispatch"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/poller"
	"github.com/yourorg/multi-agent/internal/store"
)

type noopExec struct{}

func (noopExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	sink.Close()
	return executor.Result{Summary: "ok"}, nil
}

type noopJ struct{}

func (noopJ) Record(context.Context, executor.Task, executor.Result) error { return nil }

func TestContract_PollUsesProxyTokenAndStatusUpdateShape(t *testing.T) {
	var mu sync.Mutex
	var pollAuth string
	var seenStatuses []map[string]interface{}
	var taskHandedOut atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/agent/tasks/poll":
			mu.Lock()
			pollAuth = r.Header.Get("Authorization")
			mu.Unlock()
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"task_id": "tc1", "skill": "chat", "prompt": "p",
			})
		case strings.HasPrefix(r.URL.Path, "/api/agent/tasks/") && strings.HasSuffix(r.URL.Path, "/status"):
			require.Equal(t, "PUT", r.Method)
			require.Equal(t, "Bearer ptoken", r.Header.Get("Authorization"))
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			body, _ := io.ReadAll(r.Body)
			var m map[string]interface{}
			require.NoError(t, json.Unmarshal(body, &m))
			mu.Lock()
			seenStatuses = append(seenStatuses, m)
			mu.Unlock()
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	d := dispatch.New(map[string]executor.Executor{"": noopExec{}}, noopJ{}, s)
	p := poller.New(poller.Config{
		ServerURL: srv.URL, ProxyToken: "ptoken",
		IdlePoll: 50 * time.Millisecond,
	}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seenStatuses) >= 2
	}, time.Second, 20*time.Millisecond)

	mu.Lock()
	require.Equal(t, "Bearer ptoken", pollAuth)
	require.Equal(t, "running", seenStatuses[0]["status"])
	require.Equal(t, "completed", seenStatuses[1]["status"])
	require.Equal(t, "ok", seenStatuses[1]["output"])
	mu.Unlock()
}

func TestContract_FailedHasFailureReason(t *testing.T) {
	var mu sync.Mutex
	var failureSeen map[string]interface{}
	var hand atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/tasks/poll" {
			if !hand.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"task_id": "x", "skill": "chat", "prompt": "p"})
			return
		}
		body, _ := io.ReadAll(r.Body)
		var m map[string]interface{}
		json.Unmarshal(body, &m)
		if m["status"] == "failed" {
			mu.Lock()
			failureSeen = m
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	d := dispatch.New(map[string]executor.Executor{"": failingExec{}}, noopJ{}, s)
	p := poller.New(poller.Config{ServerURL: srv.URL, ProxyToken: "p", IdlePoll: 50 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failureSeen != nil
	}, time.Second, 20*time.Millisecond)
	mu.Lock()
	require.NotEmpty(t, failureSeen["failure_reason"])
	mu.Unlock()
}

type failingExec struct{}

func (failingExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	sink.Close()
	return executor.Result{}, errBoom
}

var errBoom = newErr("boom")

func newErr(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }
