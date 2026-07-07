package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// startFakeSlave: register a slave, publish its card, dial WS as slave-side,
// spin up a goroutine that Accept's yamux streams and runs `handler` on each.
// stubSrv is used to deterministically wait for server-side registerTunnel completion
// via s.hasTunnel(sandboxID) polling.
func startFakeSlave(t *testing.T, srv *httptest.Server, stubSrv *Server, wsID, shortID string, handler func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte)) Credentials {
	t.Helper()
	creds := registerAgent(t, srv, "slave", shortID, wsID)
	postCard(t, srv, creds.ProxyToken, map[string]any{
		"display_name": "fake-" + shortID,
		"card":         map[string]any{"short_id": shortID},
	}).Body.Close()
	url := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/tunnel/" + creds.SandboxID + "?token=" + creds.TunnelToken
	dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(dctx, url, nil)
	if err != nil {
		t.Fatalf("slave dial: %v", err)
	}
	conn := newWSConn(context.Background(), ws)
	session, err := yamux.Client(conn, yamuxCfg())
	if err != nil {
		t.Fatalf("yamux client: %v", err)
	}
	t.Cleanup(func() {
		session.Close()
		ws.Close(websocket.StatusNormalClosure, "test done")
	})
	// Accept loop
	go func() {
		for {
			stream, err := session.Accept()
			if err != nil {
				return
			}
			go func(s io.ReadWriteCloser) {
				defer s.Close()
				typ, metaJSON, err := readStreamHeader(s)
				if err != nil || typ != streamTypeHTTP {
					return
				}
				var meta HTTPStreamMeta
				if err := json.Unmarshal(metaJSON, &meta); err != nil {
					return
				}
				body := make([]byte, meta.BodyLen)
				if meta.BodyLen > 0 {
					_, _ = io.ReadFull(s, body)
				}
				respMeta, respBody := handler(meta, body)
				respMetaJSON, _ := json.Marshal(respMeta)
				_ = writeStreamHeader(s, streamTypeHTTP, respMetaJSON)
				_, _ = s.Write(respBody)
			}(stream)
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if stubSrv.hasTunnel(creds.SandboxID) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !stubSrv.hasTunnel(creds.SandboxID) {
		t.Fatalf("server-side registerTunnel did not complete within 2s for %s", creds.SandboxID)
	}
	return creds
}

func peerProxyReq(t *testing.T, srv *httptest.Server, callerToken, targetShortID, path string, body []byte) *http.Response {
	t.Helper()
	url := srv.URL + "/api/agent/peer/" + targetShortID + "/proxy" + path
	req, _ := http.NewRequest("GET", url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+callerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("peer proxy: %v", err)
	}
	return resp
}

func TestPeerProxy_MissingBearer_401(t *testing.T) {
	srv := newTestServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/agent/peer/slv-x/proxy/state", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_UnknownTarget_404(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-does-not-exist", "/state", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_TargetNotInWorkspace_404(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "ws-A")
	_ = startFakeSlave(t, srv, stubSrv, "ws-B", "slv-b", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		return HTTPResponseMeta{Status: 200}, nil
	})
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-b", "/state", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404 (cross-workspace hidden) got %d", resp.StatusCode)
	}
}

func TestPeerProxy_NoActiveTunnel_502(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	target := registerAgent(t, srv, "slave", "slv-001", "")
	postCard(t, srv, target.ProxyToken, map[string]any{"display_name": "eval", "card": map[string]any{"short_id": "slv-001"}}).Body.Close()
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-001", "/state", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("want 502 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_ForwardsPOSTAndBody(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		if meta.Method != "POST" || meta.Path != "/echo" {
			return HTTPResponseMeta{Status: 400}, []byte("bad meta")
		}
		if string(body) != "hello-body" {
			return HTTPResponseMeta{Status: 400}, []byte("bad body")
		}
		return HTTPResponseMeta{Status: 200, Headers: map[string]string{"Content-Type": "text/plain"}}, []byte("hello-back")
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/agent/peer/slv-001/proxy/echo", bytes.NewReader([]byte("hello-body")))
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("want 200 got %d body %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-back" {
		t.Fatalf("want body 'hello-back' got %q", body)
	}
}

func TestPeerProxy_ReturnsResponseFromStream(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		return HTTPResponseMeta{Status: 418, Headers: map[string]string{"X-Test": "abc"}}, []byte("teapot")
	})
	resp := peerProxyReq(t, srv, caller.ProxyToken, "slv-001", "/x", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 418 {
		t.Fatalf("want 418 got %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Test") != "abc" {
		t.Fatalf("want X-Test: abc got %q", resp.Header.Get("X-Test"))
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "teapot" {
		t.Fatalf("want body 'teapot' got %q", body)
	}
}

func TestPeerProxy_StripsPrefixPreservesQuery(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	var gotPath string
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		gotPath = meta.Path
		return HTTPResponseMeta{Status: 200}, nil
	})
	url := srv.URL + "/api/agent/peer/slv-001/proxy/files/dir/tok?recursive=true"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 got %d", resp.StatusCode)
	}
	if gotPath != "/files/dir/tok?recursive=true" {
		t.Fatalf("want inner path '/files/dir/tok?recursive=true' got %q", gotPath)
	}
}

func TestPeerProxy_MethodNotAllowed_405(t *testing.T) {
	srv := newTestServer(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	req, _ := http.NewRequest("TRACE", srv.URL+"/api/agent/peer/slv-x/proxy/y", nil)
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("want 405 got %d", resp.StatusCode)
	}
}

func TestPeerProxy_EmptyRestPath_Slash(t *testing.T) {
	srv, stubSrv := newTestServerWithStub(t)
	caller := registerAgent(t, srv, "driver", "drv-001", "")
	var gotPath string
	_ = startFakeSlave(t, srv, stubSrv, "", "slv-001", func(meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, []byte) {
		gotPath = meta.Path
		return HTTPResponseMeta{Status: 200}, nil
	})
	url := srv.URL + "/api/agent/peer/slv-001/proxy"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+caller.ProxyToken)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if gotPath != "/" {
		t.Fatalf("want inner path '/' got %q", gotPath)
	}
}
