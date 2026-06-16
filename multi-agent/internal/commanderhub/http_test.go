package commanderhub

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/commander"
	"github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/identity"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// commanderSetup wires Hub+Auth on a shared mux with one real daemon (WSClient)
// and returns the server, hub, auth, the daemon's owner, and a ready session
// cookie for that owner's token.
func commanderSetup(t *testing.T, resolver *fakeResolver, token string, backend agentbackend.Backend) (*httptest.Server, *Hub, *Authenticator, owner, *http.Cookie, func()) {
	t.Helper()
	hub := NewHub(resolver)
	auth := newAuthenticatorWithFlow(resolver, newFakeDeviceFlow(token))
	mux := http.NewServeMux()
	mux.Handle("/api/daemon-link", hub) // hub.ServeHTTP upgrades the daemon WS
	Mount(mux, hub, auth)
	srv := httptest.NewServer(mux)

	wsURL := "ws" + srv.URL[len("http"):] + "/api/daemon-link"
	c := commander.NewWSClient(commander.WSConfig{
		URL:            wsURL,
		ProxyToken:     token,
		Register:       commander.RegisterPayload{SchemaVersion: commander.SchemaVersion, Kind: "claude", DisplayName: "tester"},
		Handler:        &commander.Handler{Backend: backend},
		HeartbeatInt:   10 * time.Second,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	ident, _ := resolver.Resolve(ctx, token)
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}
	waitFor(t, func() bool { return c.Linked() }, time.Second, "daemon linked")

	sessID := auth.putSession(token, ident)
	cookie := &http.Cookie{Name: sessionCookieName, Value: sessID}
	cleanup := func() { cancel(); <-errCh; srv.Close() }
	return srv, hub, auth, o, cookie, cleanup
}

// TestHTTP_DaemonsUnauthorizedWithoutCookie: no cookie/bearer → 401.
func TestHTTP_DaemonsUnauthorizedWithoutCookie(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, _, _, _, _, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{})
	defer cleanup()

	resp, err := http.Get(srv.URL + "/api/commander/daemons")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestHTTP_DaemonsReturnsOwn: with the session cookie, /daemons lists this
// owner's daemon.
func TestHTTP_DaemonsReturnsOwn(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, _, _, _, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{})
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "tester") // DisplayName from register
}

// TestHTTP_TurnStreamsSSE: POST .../turn returns text/event-stream with chunk
// events and a terminal done event. daemonID is read from the hub registry.
func TestHTTP_TurnStreamsSSE(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(_ context.Context, _, _ string, sink executor.Sink) (executor.Result, error) {
			sink.Write("chunk", "hello")
			sink.Close()
			return executor.Result{Summary: "done"}, nil
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"), resp.Header.Get("Content-Type"))

	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	joined := strings.Join(lines, "\n")
	require.Contains(t, joined, "event: status")
	require.Contains(t, joined, "event: chunk")
	require.Contains(t, joined, "hello")
	require.Contains(t, joined, "event: done")

	snap := hub.turns.get(turnKey{owner: o, daemonID: daemonID, sessionID: "s1"})
	require.Equal(t, turnStateDone, snap.State)
	require.False(t, snap.InFlight)
}

func TestHTTP_TurnRejectsConcurrentSameSession(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(ctx context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			startedOnce.Do(func() { close(started) })
			select {
			case <-block:
				return executor.Result{Summary: "done"}, nil
			case <-ctx.Done():
				return executor.Result{}, ctx.Err()
			}
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID
	url := srv.URL + "/api/commander/daemons/" + daemonID + "/sessions/s1/turn"

	req1, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"prompt":"one"}`))
	req1.Header.Set("Content-Type", "application/json")
	req1.AddCookie(cookie)
	type responseResult struct {
		resp *http.Response
		err  error
	}
	respCh := make(chan responseResult, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req1)
		respCh <- responseResult{resp: resp, err: err}
	}()
	defer func() {
		close(block)
		select {
		case res := <-respCh:
			require.NoError(t, res.err)
			if res.resp != nil {
				_, _ = io.Copy(io.Discard, res.resp.Body)
				res.resp.Body.Close()
			}
		case <-time.After(2 * time.Second):
			t.Fatal("first turn did not finish")
		}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"prompt":"two"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusConflict, resp2.StatusCode)
}

func TestHTTP_TurnErrorFrameLeavesStoreError(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			return executor.Result{}, errors.New("backend exploded")
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "event: error")

	snap := hub.turns.get(turnKey{owner: o, daemonID: daemonID, sessionID: "s1"})
	require.Equal(t, turnStateError, snap.State)
	require.False(t, snap.InFlight)
	require.Contains(t, snap.Message, "backend exploded")
}

func TestHTTP_TurnAwaitingUserLeavesStoreAwaitingApproval(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			return executor.Result{
				AwaitingUser: &executor.AskUserPayload{
					Kind:   "request_permission",
					Target: "rm -rf /tmp/x",
				},
			}, nil
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "event: done")

	snap := hub.turns.get(turnKey{owner: o, daemonID: daemonID, sessionID: "s1"})
	require.Equal(t, turnStateAwaitingApproval, snap.State)
	require.False(t, snap.InFlight)
	require.True(t, snap.AwaitingApproval)
}

// TestHTTP_SessionListJSONCasing: the session list serializes agentbackend.Session
// with Go field names (no json tags) — app.js depends on ID/Preview/WorkingDir.
// This pins the casing so it can't silently regress.
func TestHTTP_SessionListJSONCasing(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, WorkingDir: "/p", Preview: "hello"}}, nil
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	// agentbackend.Session has no json tags → wire shape is Go field names.
	require.Contains(t, s, `"ID":"s1"`)
	require.Contains(t, s, `"Preview":"hello"`)
	require.Contains(t, s, `"WorkingDir":"/p"`)
}

// TestHTTP_GetSessionNotFoundMaps404: a daemon get_session that returns
// agentbackend.ErrSessionNotFound surfaces through SendCommand as a
// *DaemonError{Code: session_not_found} and the HTTP handler maps it to 404
// (not the previous generic 502). Matches PR-2's local HTTP behavior.
func TestHTTP_GetSessionNotFoundMaps404(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		getFn: func(_ context.Context, _ string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{}, nil, agentbackend.ErrSessionNotFound
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/stale", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestHTTP_ListSessionsGenericErrorMaps502: a non-session_not_found daemon
// error (e.g. a generic backend error on list_sessions) still maps to 502.
func TestHTTP_ListSessionsGenericErrorMaps502(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return nil, errors.New("backend exploded")
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}
