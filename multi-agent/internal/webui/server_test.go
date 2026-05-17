package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/store"
)

func openStore(t *testing.T) *store.Store {
	s, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHealthz(t *testing.T) {
	s := openStore(t)
	h := NewHandler(s, "", &config.Config{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, 200, rr.Code)
	require.Contains(t, rr.Body.String(), `"ok":true`)
}

func TestTasks_404(t *testing.T) {
	s := openStore(t)
	h := NewHandler(s, "", &config.Config{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/nonexistent", nil))
	require.Equal(t, 404, rr.Code)
}

func TestState_RendersJournal(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CURRENT_STATE.md"), []byte("## Tools\n- a"), 0o644))
	h := NewHandler(s, dir, &config.Config{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/state", nil))
	require.Equal(t, 200, rr.Code)
	require.Contains(t, rr.Body.String(), "## Tools")
	require.Equal(t, "text/markdown; charset=utf-8", rr.Header().Get("Content-Type"))
}

func TestCapabilities_RendersCapabilityDocument(t *testing.T) {
	s := openStore(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CAPABILITIES.md"), []byte("# Capability Document\n"), 0o644))
	h := NewHandler(s, dir, &config.Config{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/capabilities", nil))
	require.Equal(t, 200, rr.Code)
	require.Equal(t, "# Capability Document\n", rr.Body.String())
	require.Equal(t, "text/markdown; charset=utf-8", rr.Header().Get("Content-Type"))
}

func TestStream_LiveAndDone(t *testing.T) {
	s := openStore(t)
	require.NoError(t, s.Insert(store.Task{ID: "live"}))
	h := NewHandler(s, "", &config.Config{})

	srv := httptest.NewServer(h)
	defer srv.Close()

	// Open SSE in background.
	req, _ := http.NewRequest("GET", srv.URL+"/tasks/live/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Push events — give the handler goroutine a moment to subscribe
	// before we write, to avoid a publish-before-subscribe race.
	time.Sleep(20 * time.Millisecond)
	sink := s.ChunkSink("live")
	sink.Write("chunk", "hello")
	sink.Close()

	sc := bufio.NewScanner(resp.Body)
	seen := []string{}
	deadline := time.Now().Add(2 * time.Second)
	for sc.Scan() && time.Now().Before(deadline) {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			seen = append(seen, strings.TrimPrefix(line, "event: "))
			if seen[len(seen)-1] == "done" {
				break
			}
		}
	}
	require.Contains(t, seen, "chunk")
	require.Contains(t, seen, "done")
}

func TestChildren_NotFound(t *testing.T) {
	s := openStore(t)
	h := NewHandler(s, "", &config.Config{})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/none/children", nil))
	require.Equal(t, 200, rr.Code)
	require.Equal(t, "[]\n", rr.Body.String())
}

func TestChildren_ListsSubTasks(t *testing.T) {
	s := openStore(t)
	require.NoError(t, s.Insert(store.Task{ID: "p"}))
	require.NoError(t, s.InsertSubTasks("p", []store.SubTaskRow{
		{ParentID: "p", NodeID: "n1", TargetID: "a", Prompt: "x", Status: "completed", Output: "ok"},
	}))
	h := NewHandler(s, "", &config.Config{})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/p/children", nil))
	require.Equal(t, 200, rr.Code)
	require.Contains(t, rr.Body.String(), `"NodeID":"n1"`)
	require.Contains(t, rr.Body.String(), `"Status":"completed"`)
}

func TestTaskDetail_IncludesChildren(t *testing.T) {
	s := openStore(t)
	require.NoError(t, s.Insert(store.Task{ID: "p"}))
	require.NoError(t, s.InsertSubTasks("p", []store.SubTaskRow{
		{ParentID: "p", NodeID: "n1", TargetID: "a", Prompt: "x", Status: "pending"},
	}))
	h := NewHandler(s, "", &config.Config{})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/tasks/p", nil))
	require.Equal(t, 200, rr.Code)
	require.Contains(t, rr.Body.String(), `"children"`)
	require.Contains(t, rr.Body.String(), `"NodeID":"n1"`)
}

type fakeMCPDispatcher struct {
	lastServer, lastTool string
	lastArgs             map[string]interface{}
	response             json.RawMessage
	err                  error
}

func (f *fakeMCPDispatcher) CallTool(ctx context.Context, server, tool string, args map[string]interface{}) (json.RawMessage, error) {
	f.lastServer, f.lastTool, f.lastArgs = server, tool, args
	if f.err != nil {
		return nil, f.err
	}
	return f.response, nil
}

func TestBridgeCall_DispatchesToMCPExecutor(t *testing.T) {
	fake := &fakeMCPDispatcher{response: json.RawMessage(`{"result":"hello","capability_changed":false}`)}
	cfg := &config.Config{Credentials: config.Credentials{ProxyToken: "tok"}}
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	h := NewHandler(s, t.TempDir(), cfg)
	SetMCPBridge(h, fake)

	srv := httptest.NewServer(h)
	defer srv.Close()

	body, _ := json.Marshal(map[string]interface{}{
		"server": "x", "tool": "echo", "args": map[string]interface{}{"a": 1},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/bridge/call", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		got, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, got)
	}
	var got map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["result"] != "hello" {
		t.Fatalf("got %+v", got)
	}
	if fake.lastServer != "x" || fake.lastTool != "echo" {
		t.Fatalf("dispatcher saw server=%q tool=%q", fake.lastServer, fake.lastTool)
	}
}

func TestBridgeCall_RejectsBadAuth(t *testing.T) {
	cfg := &config.Config{Credentials: config.Credentials{ProxyToken: "tok"}}
	s, _ := store.Open(filepath.Join(t.TempDir(), "x.db"))
	defer s.Close()
	h := NewHandler(s, t.TempDir(), cfg)
	SetMCPBridge(h, &fakeMCPDispatcher{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/bridge/call", bytes.NewReader([]byte(`{"server":"x","tool":"y"}`)))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestSetDriverFiles_RoutesFilesPathToDriverHandler(t *testing.T) {
	base := http.NewServeMux()
	base.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("base-healthz"))
	})
	files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("files-hit:" + r.URL.Path))
	})
	composed := SetDriverFiles(base, files)

	w1 := httptest.NewRecorder()
	composed.ServeHTTP(w1, httptest.NewRequest("GET", "/healthz", nil))
	if w1.Body.String() != "base-healthz" {
		t.Errorf("non-files path: %q", w1.Body.String())
	}
	w2 := httptest.NewRecorder()
	composed.ServeHTTP(w2, httptest.NewRequest("GET", "/files/blob/abc", nil))
	if w2.Body.String() != "files-hit:/files/blob/abc" {
		t.Errorf("files path: %q", w2.Body.String())
	}
}
