package webui

import (
	"bufio"
	"context"
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

var _ = context.Background
