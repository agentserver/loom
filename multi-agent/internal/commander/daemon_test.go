package commander

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

// TestDaemon_BothTransportsServeListSessions pins that a running daemon
// exposes the same backend data through HTTP and WS.
func TestDaemon_BothTransportsServeListSessions(t *testing.T) {
	fo := newFakeObserver(t)
	wsSrv := httptest.NewServer(fo.handler())
	defer wsSrv.Close()

	d := NewDaemon(DaemonConfig{
		Handler: &Handler{Backend: &fakeBackend{
			listFn: func(_ context.Context) ([]agentbackend.Session, error) {
				return []agentbackend.Session{{ID: "alpha"}}, nil
			},
		}},
		ListenAddr: "127.0.0.1:0",
		WS: WSConfig{
			URL:            observerWSURL(wsSrv),
			ProxyToken:     "t",
			Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     50 * time.Millisecond,
			HeartbeatInt:   10 * time.Second,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	waitFor(t, func() bool { return d.HTTPAddr() != "" }, 2*time.Second)
	waitFor(t, func() bool { return fo.registerCount() >= 1 }, 2*time.Second)

	req, _ := http.NewRequest(http.MethodGet, "http://"+d.HTTPAddr()+"/sessions", nil)
	req.Header.Set("Authorization", "Bearer t")
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer httpResp.Body.Close()
	body, _ := io.ReadAll(httpResp.Body)
	if !strings.Contains(string(body), `"alpha"`) {
		t.Errorf("HTTP body missing alpha: %s", body)
	}

	if err := fo.Send(Envelope{
		Type:    "command",
		ID:      "cmd-1",
		Payload: jsonRaw(t, CommandPayload{Command: "list_sessions"}),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		for _, env := range fo.frames() {
			if env.Type != "command_result" || env.ID != "cmd-1" {
				continue
			}
			var payload struct {
				Sessions []agentbackend.Session `json:"sessions"`
			}
			if err := json.Unmarshal(env.Payload, &payload); err != nil {
				return false
			}
			return len(payload.Sessions) == 1 && payload.Sessions[0].ID == "alpha"
		}
		return false
	}, 2*time.Second)
}

func TestDaemon_ReadyClosesAfterHTTPListen(t *testing.T) {
	fo := newFakeObserver(t)
	wsSrv := httptest.NewServer(fo.handler())
	defer wsSrv.Close()

	d := NewDaemon(DaemonConfig{
		Handler:    &Handler{Backend: &fakeBackend{}},
		ListenAddr: "127.0.0.1:0",
		WS: WSConfig{
			URL:            observerWSURL(wsSrv),
			ProxyToken:     "t",
			Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     50 * time.Millisecond,
			HeartbeatInt:   10 * time.Second,
		},
	})

	select {
	case <-d.Ready():
		t.Fatal("Ready closed before Run listened")
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	defer func() {
		cancel()
		fo.closeAll()
		<-errCh
	}()

	select {
	case <-d.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("Ready did not close after HTTP listen")
	}
	if d.HTTPAddr() == "" {
		t.Fatal("HTTPAddr empty after Ready closed")
	}
}

func TestNewHTTPServerConfiguresReadHeaderTimeout(t *testing.T) {
	srv := newHTTPServer(&Handler{Backend: &fakeBackend{}}, LinkStatusFunc(func() bool { return true }), "t")
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout=%v want 5s", srv.ReadHeaderTimeout)
	}
}

// TestDaemon_GracefulShutdownClosesBothTransports pins that context cancel
// stops Run and closes the HTTP listener.
func TestDaemon_GracefulShutdownClosesBothTransports(t *testing.T) {
	fo := newFakeObserver(t)
	wsSrv := httptest.NewServer(fo.handler())
	defer wsSrv.Close()

	d := NewDaemon(DaemonConfig{
		Handler:    &Handler{Backend: &fakeBackend{}},
		ListenAddr: "127.0.0.1:0",
		WS: WSConfig{
			URL:            observerWSURL(wsSrv),
			ProxyToken:     "t",
			Register:       RegisterPayload{SchemaVersion: SchemaVersion, Kind: "claude"},
			InitialBackoff: 10 * time.Millisecond,
			MaxBackoff:     50 * time.Millisecond,
			HeartbeatInt:   10 * time.Second,
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()

	waitFor(t, func() bool { return d.HTTPAddr() != "" }, 2*time.Second)
	addr := d.HTTPAddr()
	cancel()
	fo.closeAll()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s of cancel")
	}

	client := &http.Client{Timeout: 200 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err == nil {
		resp.Body.Close()
		t.Error("HTTP still serving after shutdown")
	}
}
