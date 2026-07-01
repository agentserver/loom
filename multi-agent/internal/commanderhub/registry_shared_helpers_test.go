package commanderhub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newOwnershipTestDaemonConn returns a daemonConn whose `conn` is a
// real server-side *websocket.Conn over a localhost loopback connection,
// so dc.conn.Close() is observable via ownershipTestConnIsClosed.
//
// The server-side conn is what runHeartbeat will Close(); the client-side
// conn is held by the cleanup so it doesn't get GC'd mid-test.
func newOwnershipTestDaemonConn(t *testing.T, connID, shortID string, o owner) *daemonConn {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	serverCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("server upgrade: %v", err)
			return
		}
		serverCh <- c
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case sc := <-serverCh:
		return &daemonConn{
			id: connID, shortID: shortID, owner: o, conn: sc,
			pending: make(map[string]*pendingEntry),
			done:    make(chan struct{}),
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server upgrade timeout")
		return nil
	}
}

func ownershipTestConnIsClosed(dc *daemonConn) bool {
	// Probe with a 100ms write deadline; gorilla returns websocket.ErrCloseSent
	// or net.OpError on closed conn.
	_ = dc.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	err := dc.conn.WriteMessage(websocket.PingMessage, nil)
	return err != nil
}
