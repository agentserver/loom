package commanderhub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestHTTP_TreeGroupsAndSortsSessions(t *testing.T) {
	now := time.Now()
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, _, _, _, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{
				{ID: "old-session", Kind: agentbackend.KindCodex, Title: "old prompt", UpdatedAt: now.Add(-time.Hour)},
				{ID: "new-session", Kind: agentbackend.KindCodex, Title: "new prompt", UpdatedAt: now},
			}, nil
		},
	})
	defer cleanup()

	tree := getCommanderTree(t, srv, cookie)

	require.Len(t, tree.Daemons, 1)
	require.Equal(t, "ok", tree.Daemons[0].Status)
	require.Equal(t, 2, tree.Daemons[0].SessionCount)
	require.Len(t, tree.Daemons[0].Sessions, 2)
	require.Equal(t, "new-session", tree.Daemons[0].Sessions[0].SessionID)
	require.Equal(t, "old-session", tree.Daemons[0].Sessions[1].SessionID)
	require.Equal(t, "new prompt", tree.Daemons[0].Sessions[0].Title)
}

func TestHTTP_TreeUsesSessionCacheWithinTTL(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	var mu sync.Mutex
	var calls int
	srv, _, _, _, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return []agentbackend.Session{{ID: "cached-session", Kind: agentbackend.KindClaude, Title: "cached prompt"}}, nil
		},
	})
	defer cleanup()

	first := getCommanderTree(t, srv, cookie)
	second := getCommanderTree(t, srv, cookie)

	require.Equal(t, "cached-session", first.Daemons[0].Sessions[0].SessionID)
	require.Equal(t, "cached-session", second.Daemons[0].Sessions[0].SessionID)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls)
}

func TestHTTP_TreeInvalidatesDaemonCacheAfterTurnCompletes(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	var mu sync.Mutex
	var calls int
	completed := false
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			title := "before turn"
			if completed {
				title = "after turn"
			}
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, Title: title}}, nil
		},
		resumeFn: func(context.Context, string, string, executor.Sink) (executor.Result, error) {
			mu.Lock()
			completed = true
			mu.Unlock()
			return executor.Result{Summary: "done"}, nil
		},
	})
	defer cleanup()

	first := getCommanderTree(t, srv, cookie)
	require.Equal(t, "before turn", first.Daemons[0].Sessions[0].Title)

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/"+dis[0].DaemonID+"/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "event: done")

	second := getCommanderTree(t, srv, cookie)
	require.Equal(t, "after turn", second.Daemons[0].Sessions[0].Title)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, calls)
}

func TestHTTP_TreeMergesTurnState(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		listFn: func(context.Context) ([]agentbackend.Session, error) {
			return []agentbackend.Session{{ID: "s1", Kind: agentbackend.KindClaude, Title: "needs approval"}}, nil
		},
	})
	defer cleanup()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	key := turnKey{owner: o, daemonID: dis[0].DaemonID, sessionID: "s1"}
	hub.turns.mu.Lock()
	hub.turns.m[key] = turnSnapshot{
		State:            turnStateAnswering,
		ActiveWorker:     true,
		AwaitingApproval: true,
		updatedAt:        time.Now(),
	}
	hub.turns.mu.Unlock()

	tree := getCommanderTree(t, srv, cookie)

	require.Len(t, tree.Daemons, 1)
	require.Equal(t, 1, tree.Daemons[0].ActiveCount)
	require.Equal(t, 1, tree.Daemons[0].TurnCount)
	require.Len(t, tree.Daemons[0].Sessions, 1)
	row := tree.Daemons[0].Sessions[0]
	require.Equal(t, "answering", row.TurnState)
	require.True(t, row.ActiveWorker)
	require.True(t, row.AwaitingApproval)
}

func TestHTTP_FileRoutesProxyToDaemon(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0644))
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	})
	defer cleanup()

	daemonID := hub.reg.daemons(o)[0].DaemonID
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/files?path=.", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "go.mod")

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/files/content?path=go.mod", nil)
	req2.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body2, _ := io.ReadAll(resp2.Body)
	require.Contains(t, string(body2), "module x")
}

func TestWriteSendCmdErrorInvalidRequestMaps400(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/commander/daemons/d1/sessions/s1/files", nil)

	writeSendCmdError(rec, req, &DaemonError{Code: commander.ErrCodeInvalidRequest, Message: "bad path"})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "bad path")
}

func TestHTTP_FileRouteInvalidRequestMaps400(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "inside.txt"), []byte("ok\n"), 0644))
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		getFn: func(context.Context, string) (agentbackend.Session, []agentbackend.SessionMessage, error) {
			return agentbackend.Session{ID: "s1", WorkingDir: root}, nil, nil
		},
	})
	defer cleanup()

	daemonID := hub.reg.daemons(o)[0].DaemonID
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/daemons/"+daemonID+"/sessions/s1/files/content?path=../secret.txt", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "outside session root")
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

func TestUpdateTurnStateIgnoresBackendSpecificStatusText(t *testing.T) {
	hub := NewHub(&fakeResolver{})
	ch := &commanderHandlers{hub: hub}
	key := turnKey{owner: owner{userID: "alice", workspaceID: "W1"}, daemonID: "d1", sessionID: "s1"}
	require.True(t, hub.turns.begin(key))

	payload, err := json.Marshal(commander.EventPayload{EventKind: "status", Text: "codex running"})
	require.NoError(t, err)
	ch.updateTurnStateFromEnvelope(key, commander.Envelope{Type: "event", Payload: payload})

	snap := hub.turns.get(key)
	require.Equal(t, turnStateQueued, snap.State)
	require.True(t, snap.InFlight)

	payload, err = json.Marshal(commander.EventPayload{EventKind: "chunk", Text: "hello"})
	require.NoError(t, err)
	ch.updateTurnStateFromEnvelope(key, commander.Envelope{Type: "event", Payload: payload})

	snap = hub.turns.get(key)
	require.Equal(t, turnStateAnswering, snap.State)
	require.True(t, snap.InFlight)
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

func TestHTTP_TurnPreStreamDaemonGoneLeavesStoreDisconnected(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	hub := NewHub(resolver)
	auth := newAuthenticatorWithFlow(resolver, newFakeDeviceFlow("tok-alice"))
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ident := identity.Identity{UserID: "alice", WorkspaceID: "W1"}
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}
	cookie := &http.Cookie{Name: sessionCookieName, Value: auth.putSession("tok-alice", ident)}
	hub.reg.add(&daemonConn{id: "gone", owner: o, done: closedDone(), pending: map[string]*pendingEntry{}})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/gone/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	snap := hub.turns.get(turnKey{owner: o, daemonID: "gone", sessionID: "s1"})
	require.Equal(t, turnStateDisconnected, snap.State)
	require.False(t, snap.InFlight)
}

func TestHTTP_TurnMissingDaemonDoesNotCreateTurnState(t *testing.T) {
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	hub := NewHub(resolver)
	auth := newAuthenticatorWithFlow(resolver, newFakeDeviceFlow("tok-alice"))
	mux := http.NewServeMux()
	Mount(mux, hub, auth)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ident := identity.Identity{UserID: "alice", WorkspaceID: "W1"}
	o := owner{userID: ident.UserID, workspaceID: ident.WorkspaceID}
	cookie := &http.Cookie{Name: sessionCookieName, Value: auth.putSession("tok-alice", ident)}
	key := turnKey{owner: o, daemonID: "missing", sessionID: "s1"}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/commander/daemons/missing/sessions/s1/turn", strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, turnStateIdle, hub.turns.get(key).State)
	hub.turns.mu.Lock()
	_, exists := hub.turns.m[key]
	hub.turns.mu.Unlock()
	require.False(t, exists, "missing daemon request should not create turn state")
}

func TestHTTP_TurnRequestCanceledKeepsGuardUntilDaemonTerminal(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	var startedOnce sync.Once
	var blockOnce sync.Once
	closeBlock := func() { blockOnce.Do(func() { close(block) }) }
	resolver := &fakeResolver{mu: map[string]identity.Identity{"tok-alice": {UserID: "alice", WorkspaceID: "W1"}}}
	srv, hub, _, o, cookie, cleanup := commanderSetup(t, resolver, "tok-alice", &tbBackend{
		resumeFn: func(_ context.Context, _, _ string, _ executor.Sink) (executor.Result, error) {
			startedOnce.Do(func() { close(started) })
			<-block
			return executor.Result{Summary: "done"}, nil
		},
	})
	defer cleanup()
	defer closeBlock()

	dis := hub.reg.daemons(o)
	require.NotEmpty(t, dis)
	daemonID := dis[0].DaemonID
	url := srv.URL + "/api/commander/daemons/" + daemonID + "/sessions/s1/turn"

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(`{"prompt":"go"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	type responseResult struct {
		resp *http.Response
		err  error
	}
	respCh := make(chan responseResult, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		respCh <- responseResult{resp: resp, err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not start")
	}
	cancel()
	select {
	case res := <-respCh:
		if res.resp != nil {
			_, _ = io.Copy(io.Discard, res.resp.Body)
			res.resp.Body.Close()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not return after cancel")
	}

	key := turnKey{owner: o, daemonID: daemonID, sessionID: "s1"}
	snap := hub.turns.get(key)
	require.True(t, snap.InFlight, "browser cancellation must not clear daemon turn guard: %+v", snap)

	secondCtx, secondCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer secondCancel()
	req2, _ := http.NewRequestWithContext(secondCtx, http.MethodPost, url, strings.NewReader(`{"prompt":"two"}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusConflict, resp2.StatusCode)

	closeBlock()
	waitFor(t, func() bool {
		snap := hub.turns.get(key)
		return snap.State == turnStateDone && !snap.InFlight
	}, 2*time.Second, "turn state did not finish after daemon terminal")
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

func getCommanderTree(t *testing.T, srv *httptest.Server, cookie *http.Cookie) CommanderTree {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/commander/tree", nil)
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tree CommanderTree
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tree))
	return tree
}
