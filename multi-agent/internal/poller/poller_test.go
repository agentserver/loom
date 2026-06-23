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

// chatWrappingExec produces an executor.Result the dispatcher will wrap into
// a kind:final envelope (chat-skill semantics). Used to assert the poller
// forwards the wrapped marker as the agentserver `result` field, not the
// raw summary string.
type chatWrappingExec struct {
	summary, sessionID string
}

func (e chatWrappingExec) Run(ctx context.Context, t executor.Task, sink executor.Sink) (executor.Result, error) {
	sink.Close()
	return executor.Result{Summary: e.summary, SessionID: e.sessionID}, nil
}

func TestPoller_ForwardsWrappedMarkerAsAgentserverResult(t *testing.T) {
	// Without this forwarding, the driver's recordTerminalChild (#24 P2)
	// silently no-ops whenever the observer relay is unavailable — because
	// the kind-marker envelope only existed in the observer event payload
	// and the local slave store, never in agentserver's task.result.
	var taskHandedOut atomic.Bool
	var mu sync.Mutex
	var completedResult json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/tasks/poll" {
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"task_id":"t-wrap","skill":"chat","prompt":"hi","timeout_seconds":30}]`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Status == "completed" {
			mu.Lock()
			completedResult = msg.Result
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	exec := chatWrappingExec{summary: "ok", sessionID: "slave-thr-xyz"}
	d := dispatch.New(map[string]executor.Executor{"": exec, "chat": exec}, stubJ{}, s, nil)

	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(completedResult) > 0
	}, 15*time.Second, 20*time.Millisecond)

	mu.Lock()
	got := completedResult
	mu.Unlock()

	// Result must be the structured kind-marker envelope, not a JSON-encoded
	// summary string. sessionIDFromMarker reads exactly this shape.
	var wrapper struct {
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
		Summary   string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(got, &wrapper), "result must be JSON object: %s", string(got))
	require.Equal(t, "final", wrapper.Kind, "result must carry kind=final, got %s", string(got))
	require.Equal(t, "slave-thr-xyz", wrapper.SessionID, "result must carry the slave session id (drives reverse parent link)")
	require.Equal(t, "ok", wrapper.Summary)
}

func TestPoller_DrainPendingAckPreservesJSONEnvelope(t *testing.T) {
	// PopPendingAcks returns Reason = row.Output. For chat skills that's a
	// JSON kind-marker envelope (e.g. `{"kind":"final",...}`). If the ack
	// drainer JSON-encodes it again it arrives at agentserver as
	// `"{\"kind\":\"final\"...}"`, breaking sessionIDFromMarker downstream.
	// Forward valid JSON verbatim.
	var taskHandedOut atomic.Bool
	var mu sync.Mutex
	var ackedResult json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/tasks/poll" {
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`)) // no new tasks; let the drainer run
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Status == "completed" {
			mu.Lock()
			ackedResult = msg.Result
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	envelope := `{"kind":"final","summary":"hi","session_id":"thr-pending"}`
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	// Seed: a completed task with the wrapped envelope as its stored output,
	// then queue a pending ack that the drainer will pick up.
	_, err := s.InsertIfAbsent(store.Task{ID: "t-pending", Skill: "chat", Prompt: "hi"})
	require.NoError(t, err)
	require.NoError(t, s.Complete("t-pending", envelope))
	require.NoError(t, s.EnqueuePendingAck("t-pending", "completed"))

	d := dispatch.New(map[string]executor.Executor{"": echoExec{}}, stubJ{}, s, nil)
	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ackedResult) > 0
	}, 5*time.Second, 20*time.Millisecond)

	mu.Lock()
	got := ackedResult
	mu.Unlock()

	// Result must be the envelope, NOT a JSON-encoded string of the envelope.
	var wrapper struct {
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal(got, &wrapper), "ack body result must be the raw envelope: %s", string(got))
	require.Equal(t, "final", wrapper.Kind)
	require.Equal(t, "thr-pending", wrapper.SessionID, "session id must survive ack-drain round trip")
}

func TestPoller_DrainPendingAckNonChatJSONOutputForwardedAsString(t *testing.T) {
	// Bash/file/MCP outputs that happen to be valid JSON (e.g. {"ok":true})
	// must round-trip through ack drain the same way they round-trip through
	// the normal path: as a JSON-encoded STRING. Earlier `json.Valid` short
	// circuit swapped the wire type to "object", breaking consumers that
	// json.Unmarshal'd info.Result into a `var raw string`.
	var taskHandedOut atomic.Bool
	var mu sync.Mutex
	var ackedResult json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/tasks/poll" {
			if !taskHandedOut.CompareAndSwap(false, true) {
				w.WriteHeader(204)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			Status string          `json:"status"`
			Result json.RawMessage `json:"result"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Status == "completed" {
			mu.Lock()
			ackedResult = msg.Result
			mu.Unlock()
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// A bash task whose stdout happens to be valid JSON text. Stored as raw
	// text in row.Output (no chat envelope wrapping for non-chat skills).
	rawJSONOutput := `{"ok":true,"items":[1,2,3]}`
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	_, err := s.InsertIfAbsent(store.Task{ID: "t-bash-json", Skill: "bash", Prompt: "echo"})
	require.NoError(t, err)
	require.NoError(t, s.Complete("t-bash-json", rawJSONOutput))
	require.NoError(t, s.EnqueuePendingAck("t-bash-json", "completed"))

	d := dispatch.New(map[string]executor.Executor{"": echoExec{}}, stubJ{}, s, nil)
	p := New(Config{ServerURL: srv.URL, ProxyToken: "ptoken", IdlePoll: 50 * time.Millisecond, ActivePoll: 10 * time.Millisecond}, d, s)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go p.Run(ctx)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ackedResult) > 0
	}, 5*time.Second, 20*time.Millisecond)

	mu.Lock()
	got := ackedResult
	mu.Unlock()

	// Must arrive as a JSON-encoded string, not as the raw object. The
	// normal-path poller (above, TestPoller_PollsAndCompletes) encodes
	// summary as a string via json.Marshal; ack drain must match.
	var asString string
	if err := json.Unmarshal(got, &asString); err != nil {
		t.Fatalf("non-chat JSON output must be wire-encoded as a JSON string, got raw=%s", string(got))
	}
	require.Equal(t, rawJSONOutput, asString, "JSON-encoded string must round-trip to the raw bash output")
}
