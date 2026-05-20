package observerclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yourorg/multi-agent/internal/observer"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewDisabledSkipsRegisterAndDropsEvents(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	c, err := New(Config{Enabled: false, URL: srv.URL})
	require.NoError(t, err)
	require.False(t, c.Enabled())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()
	require.Equal(t, int32(0), calls.Load())
}

func TestNewColdStartRegistersAndEmits(t *testing.T) {
	received := make(chan observer.Event, 1)
	var lastAuth atomic.Value
	lastAuth.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"agent-1","role":"slave","token":"tk_issued"}`))
		case "/api/events":
			lastAuth.Store(r.Header.Get("Authorization"))
			var ev observer.Event
			_ = json.NewDecoder(r.Body).Decode(&ev)
			received <- ev
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_secret", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.True(t, c.Enabled())
	require.Equal(t, "tk_issued", c.Token())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()

	select {
	case ev := <-received:
		require.Equal(t, "agent-1", ev.AgentID)
		require.Equal(t, "ws-1", ev.WorkspaceID)
		require.Equal(t, observer.RoleSlave, ev.AgentRole)
		require.Equal(t, "Bearer tk_issued", lastAuth.Load())
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive event before timeout")
	}
}

func TestNewWarmStartSkipsRegister(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_cached"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agents/register" {
			t.Fatalf("register must not be called when token file exists")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_secret", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.Equal(t, "tk_cached", c.Token())
	c.Close()
}

func TestNewRegisterFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid api key", http.StatusForbidden)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	_, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_bad", TokenStatePath: path,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr))
}

func TestCloseReturnsWhenPostStalls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_cached"))

	c, err := New(Config{
		Enabled: true, URL: "http://observer.example", WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)

	block := make(chan struct{})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-block
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Status:     "202 Accepted",
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})

	c.Emit(observer.Event{TaskID: "task-1"})
	done := make(chan struct{})
	go func() { c.Close(); close(done) }()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		close(block)
		t.Fatal("Close did not return after bounded flush timeout")
	}
	close(block)
}
