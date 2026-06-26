package commanderhub

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/commanderhub/authstore"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestE2E_SubjectAndWorkspaceIsolation(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-AW":  {UserID: "alice", WorkspaceID: "W"},
		"tok-AW2": {UserID: "alice", WorkspaceID: "W2"},
		"tok-BW":  {UserID: "bob", WorkspaceID: "W"},
		"tok-BW2": {UserID: "bob", WorkspaceID: "W2"},
	}}
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, "https://unused/", authstore.NewInMemoryStore()) // bearer path; device flow not exercised
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	mux.Handle("/api/daemon-link", hub)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Dial four real daemons, each under its own (user, workspace).
	mkDaemon := func(token, display string, backend agentbackend.Backend) func() {
		wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
		c := commander.NewWSClient(commander.WSConfig{
			URL: wsURL, ProxyToken: token,
			Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: display},
			Handler:        &commander.Handler{Backend: backend},
			HeartbeatInt:   10 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		})
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- c.Run(ctx) }()
		waitFor(t, func() bool { return c.Linked() }, time.Second, display+" linked")
		return func() { cancel(); <-errCh }
	}

	cleanup := sync.WaitGroup{}
	cleanAll := []func(){}
	addDaemon := func(token, display string, b agentbackend.Backend) {
		cleanup.Add(1)
		cl := mkDaemon(token, display, b)
		cleanAll = append(cleanAll, func() { cl(); cleanup.Done() })
	}
	addDaemon("tok-AW", "alice-W", &tbBackend{})
	addDaemon("tok-AW2", "alice-W2", &tbBackend{})
	addDaemon("tok-BW", "bob-W", &tbBackend{})
	addDaemon("tok-BW2", "bob-W2", &tbBackend{})
	defer func() { for _, c := range cleanAll { c() }; cleanup.Wait() }()

	bearerGet := func(token, path string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	// alice/W sees exactly her W daemon, not her W2 daemon, not bob's.
	resp, body := bearerGet("tok-AW", "/api/commander/daemons")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "alice-W")
	require.NotContains(t, body, "alice-W2") // same user, other workspace
	require.NotContains(t, body, "bob-")     // other user

	// alice/W2 sees only her W2 daemon.
	_, body = bearerGet("tok-AW2", "/api/commander/daemons")
	require.Contains(t, body, "alice-W2")
	require.NotContains(t, body, "alice-W\"") // not the W one (substring guard)

	// bob sees only bob.
	_, body = bearerGet("tok-BW", "/api/commander/daemons")
	require.Contains(t, body, "bob-W")
	require.NotContains(t, body, "alice-")

	// Cross-owner daemon id → 404. Grab bob-W's id via the registry, hit as alice.
	bobW := hub.reg.daemons(owner{"bob", "W"})
	require.Len(t, bobW, 1)
	resp, _ = bearerGet("tok-AW", "/api/commander/daemons/"+bobW[0].DaemonID+"/sessions")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestE2e_TurnSSEBearer(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{
		"tok-AW": {UserID: "alice", WorkspaceID: "W"},
	}}
	hub := NewHub(resolver)
	auth := NewAuthenticator(resolver, "https://unused/", authstore.NewInMemoryStore())
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	mux.Handle("/api/daemon-link", hub)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL: wsURL, ProxyToken: "tok-AW",
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "alice-W"},
		Handler: &commander.Handler{Backend: &tbBackend{
			resumeFn: func(_ context.Context, _ agentbackend.SessionRef, _ string, sink executor.Sink) (executor.Result, error) {
				sink.Write("chunk", "hi from daemon")
				sink.Close()
				return executor.Result{Summary: "done"}, nil
			},
		}},
		HeartbeatInt: 10 * time.Second, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	defer func() { cancel(); <-errCh }()
	waitFor(t, func() bool { return c.Linked() }, time.Second, "linked")

	daemonID := hub.reg.daemons(owner{"alice", "W"})[0].DaemonID
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/turn",
		strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Authorization", "Bearer tok-AW")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))

	var lines []string
	for sc := bufio.NewScanner(resp.Body); sc.Scan(); {
		lines = append(lines, sc.Text())
	}
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "event: chunk")
	require.Contains(t, joined, "hi from daemon")
	require.Contains(t, joined, "event: done")
}
