package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// dialTunnel connects a client-side yamux session to the stub tunnel endpoint.
func dialTunnel(t *testing.T, srv *httptest.Server, sid, token string) (*yamux.Session, *websocket.Conn, error) {
	t.Helper()
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + sid + "?token=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, nil, err
	}
	conn := newWSConn(context.Background(), ws)
	session, err := yamux.Client(conn, yamuxCfg())
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return session, ws, nil
}

func TestTunnelUpgrade_MissingToken_401(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + creds.SandboxID
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("want dial error; got nil")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 in failure response; got resp=%v err=%v", resp, err)
	}
}

func TestTunnelUpgrade_TokenSandboxMismatch_401(t *testing.T) {
	srv := newTestServer(t)
	credsA := registerAgent(t, srv, "slave", "slv-a", "")
	credsB := registerAgent(t, srv, "slave", "slv-b", "")
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + credsA.SandboxID + "?token=" + credsB.TunnelToken
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		t.Fatal("want dial error; got nil")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401; got resp=%v err=%v", resp, err)
	}
}

func TestTunnelUpgrade_AcceptsAndOpensYamuxSession(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	session, ws, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "test")
	defer session.Close()
	if session.NumStreams() != 0 {
		t.Fatalf("num streams: want 0 got %d", session.NumStreams())
	}
}

func TestTunnelReconnect_OldSessionClosed_NewSessionServed(t *testing.T) {
	srv := newTestServer(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	s1, ws1, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	s2, ws2, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { s2.Close(); ws2.Close(websocket.StatusNormalClosure, "test") }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s1.IsClosed() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !s1.IsClosed() {
		t.Fatal("old session should be closed after reconnect; still open")
	}
	_ = ws1.Close(websocket.StatusNormalClosure, "test")
	if s2.IsClosed() {
		t.Fatal("new session should be open; already closed")
	}
}

func TestTunnelUnregister_DoesNotDeleteNewAfterReconnect(t *testing.T) {
	// fix plan review r2 P1-1: verify unregisterTunnel(sid, oldT) is a no-op
	// when tunnels[sid] has already been overwritten by a newer session.
	srv, stubSrv := newTestServerWithStub(t)
	creds := registerAgent(t, srv, "slave", "slv-001", "")
	s1, ws1, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	s2, ws2, err := dialTunnel(t, srv, creds.SandboxID, creds.TunnelToken)
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	defer func() { s2.Close(); ws2.Close(websocket.StatusNormalClosure, "test") }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s1.IsClosed() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = ws1.Close(websocket.StatusNormalClosure, "test")

	// Poll: over 1s window s2 must remain registered (fix plan r3 P1-2).
	stableDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(stableDeadline) {
		if !stubSrv.hasTunnel(creds.SandboxID) {
			t.Fatal("new tunnel was deleted by old handler's unregister — reconnect race not guarded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !stubSrv.hasTunnel(creds.SandboxID) {
		t.Fatal("new tunnel gone by end of stability window")
	}
}
