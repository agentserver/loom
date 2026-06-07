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

func TestNewRegistersButTelemetryDefaultsDisabled(t *testing.T) {
	var eventCalls atomic.Int32
	var registerCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			registerCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws1","token":"tk"}`))
		case "/api/events":
			eventCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "observer.token")
	c, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws1", AgentID: "agent1",
		AgentRole: observer.RoleSlave, APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.True(t, c.Enabled())
	c.Emit(observer.Event{TaskID: "t"})
	c.Close()
	require.Equal(t, int32(1), registerCalls.Load())
	require.Equal(t, int32(0), eventCalls.Load())
}

func TestNewColdStartRegistersAndEmits(t *testing.T) {
	received := make(chan observer.Event, 1)
	var lastAuth atomic.Value
	lastAuth.Store("")
	var lastTelemetryKey atomic.Value
	lastTelemetryKey.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"agent-1","role":"slave","token":"tk_issued"}`))
		case "/api/events":
			lastAuth.Store(r.Header.Get("Authorization"))
			lastTelemetryKey.Store(r.Header.Get("X-Loom-Telemetry-Key"))
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
		Enabled: true, TelemetryEnabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak_secret", TokenStatePath: path,
		TelemetryAPIKey: "ops-secret",
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
		require.Equal(t, "ops-secret", lastTelemetryKey.Load())
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive event before timeout")
	}

	// Spec requires the issued token to be persisted with mode 0600.
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	persisted, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "tk_issued", string(persisted))
}

func TestNewColdStartRegistersButTelemetryDefaultsDisabled(t *testing.T) {
	var eventCalls atomic.Int32
	var registerCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			registerCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","agent_id":"agent-1","role":"slave","token":"tk_issued"}`))
		case "/api/events":
			eventCalls.Add(1)
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

	require.Equal(t, int32(1), registerCalls.Load(), "observer registration must remain enabled")
	require.Equal(t, int32(0), eventCalls.Load(), "telemetry must be opt-in")
}

func TestNewWithAgentserverProxyTokenSkipsObserverRegistration(t *testing.T) {
	var registerCalls atomic.Int32
	var lastAuth atomic.Value
	lastAuth.Store("")
	received := make(chan observer.Event, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			registerCalls.Add(1)
			http.Error(w, "register disabled", http.StatusNotFound)
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

	c, err := New(Config{
		Enabled: true, TelemetryEnabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agentserver-agent", AgentRole: observer.RoleSlave,
		AgentserverProxyToken: "proxy-token",
	})
	require.NoError(t, err)
	require.Equal(t, "proxy-token", c.Token())

	c.Emit(observer.Event{TaskID: "task-1"})
	c.Close()

	require.Equal(t, int32(0), registerCalls.Load(), "proxy-token mode must not call observer registration")
	require.Equal(t, "Bearer proxy-token", lastAuth.Load())
	select {
	case ev := <-received:
		require.Equal(t, "ws-1", ev.WorkspaceID)
		require.Equal(t, "agentserver-agent", ev.AgentID)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive proxy-token event before timeout")
	}
}

func TestNewEnabledRequiresProxyTokenOrLegacyRegistrationConfig(t *testing.T) {
	_, err := New(Config{
		Enabled:     true,
		URL:         "https://observer.example",
		WorkspaceID: "ws-1",
		AgentID:     "agent-1",
		AgentRole:   observer.RoleSlave,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agentserver proxy token or observer api_key")
}

func TestNewWorkspaceMismatchReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workspace_id":"OTHER-WS","token":"tk_x"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	_, err := New(Config{
		Enabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "OTHER-WS")
	require.Contains(t, err.Error(), "ws-1")
	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "mismatch must not persist a token")
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
		Enabled: true, TelemetryEnabled: true, URL: "http://observer.example", WorkspaceID: "ws-1",
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

func TestPost401TriggersReRegisterAndUpdatesToken(t *testing.T) {
	var eventCalls atomic.Int32
	var regCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			regCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_v2"}`))
		case "/api/events":
			n := eventCalls.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer tk_v2" {
				t.Errorf("second event Authorization want Bearer tk_v2, got %s", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_v1"))

	c, err := New(Config{
		Enabled: true, TelemetryEnabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)
	require.Equal(t, "tk_v1", c.Token())

	c.Emit(observer.Event{TaskID: "t1"})
	c.Emit(observer.Event{TaskID: "t2"})
	c.Close()

	require.Equal(t, int32(1), regCalls.Load(), "expected exactly one re-register")
	require.Equal(t, "tk_v2", c.Token())

	got, _ := os.ReadFile(path)
	require.Equal(t, "tk_v2", string(got))
}

func TestPost401WithinCooldownSkipsReRegister(t *testing.T) {
	var regCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			regCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_v2"}`))
		case "/api/events":
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_v1"))

	c, err := New(Config{
		Enabled: true, TelemetryEnabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		c.Emit(observer.Event{TaskID: "t"})
	}
	c.Close()

	require.Equal(t, int32(1), regCalls.Load(),
		"expected exactly one register call under cooldown; got %d", regCalls.Load())
}

func TestPost403DoesNotTriggerReRegister(t *testing.T) {
	var regCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/agents/register":
			regCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspace_id":"ws-1","token":"tk_v2"}`))
		case "/api/events":
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "observer.token")
	require.NoError(t, writeTokenFile(path, "tk_v1"))

	c, err := New(Config{
		Enabled: true, TelemetryEnabled: true, URL: srv.URL, WorkspaceID: "ws-1",
		AgentID: "agent-1", AgentRole: observer.RoleSlave,
		APIKey: "ak", TokenStatePath: path,
	})
	require.NoError(t, err)
	c.Emit(observer.Event{TaskID: "t"})
	c.Close()

	require.Equal(t, int32(0), regCalls.Load(), "403 must not trigger re-register")
}
