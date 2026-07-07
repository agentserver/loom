// Copied verbatim (with local renames to unexported names + package rename to `main`)
// from github.com/agentserver/agentserver@v0.69.9 internal/tunnel/{stream,mux,wsconn,registry}.go
// — needed because that package is internal/. Do NOT edit; if the upstream copy
// diverges, refresh this file with a byte-diff.
//
// Original files & sizes (agentserver v0.69.9):
//   stream.go   76 lines
//   mux.go      38 lines
//   wsconn.go   97 lines
//   registry.go 222 lines
//
// Adaptations in this copy:
//   - Package renamed to `main` (stub is a command)
//   - Public helpers renamed to unexported: WriteStreamHeader → writeStreamHeader, etc.
//   - Types stubTunnel / wsConn — smoke variants of tunnel.Tunnel / tunnel.WSConn;
//     terminal / OnAgentInfo / control-stream dispatch dropped (spec §1.2 non-goals).

package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// ---------------------------------------------------------------------------
// Stream header wire format (spec §3.5)
//   1B type + 4B big-endian metadata length + metadata bytes
// ---------------------------------------------------------------------------

const (
	streamTypeHTTP     byte = 0x01
	streamTypeTerminal byte = 0x02 // reserved; not implemented
	streamTypeControl  byte = 0x03 // agent-info control; read + discarded
)

func writeStreamHeader(w io.Writer, streamType byte, metadata []byte) error {
	header := make([]byte, 5)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(metadata)))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("write stream header: %w", err)
	}
	if len(metadata) > 0 {
		if _, err := w.Write(metadata); err != nil {
			return fmt.Errorf("write stream metadata: %w", err)
		}
	}
	return nil
}

func readStreamHeader(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, fmt.Errorf("read stream header: %w", err)
	}
	streamType := header[0]
	metaLen := binary.BigEndian.Uint32(header[1:5])
	if metaLen > 1<<20 {
		return 0, nil, fmt.Errorf("stream metadata too large: %d bytes", metaLen)
	}
	var metadata []byte
	if metaLen > 0 {
		metadata = make([]byte, metaLen)
		if _, err := io.ReadFull(r, metadata); err != nil {
			return 0, nil, fmt.Errorf("read stream metadata: %w", err)
		}
	}
	return streamType, metadata, nil
}

// HTTPStreamMeta is written from server → agent as stream_type=streamTypeHTTP metadata.
type HTTPStreamMeta struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	BodyLen int               `json:"body_len"`
}

// HTTPResponseMeta is written from agent → server as stream_type=streamTypeHTTP metadata.
type HTTPResponseMeta struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// ---------------------------------------------------------------------------
// wsConn — net.Conn wrapper over nhooyr.io/websocket
// (spec §3.4 — copied from agentserver internal/tunnel/wsconn.go)
// ---------------------------------------------------------------------------

type wsConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	ws     *websocket.Conn
	rBuf   []byte
	rMu    sync.Mutex
	wMu    sync.Mutex
	closed bool
	deadR  time.Time
	deadW  time.Time
}

func newWSConn(ctx context.Context, ws *websocket.Conn) *wsConn {
	cctx, cancel := context.WithCancel(ctx)
	// Match upstream agentserver internal/tunnel.NewWSConn wsconn.go:33 —
	// disable nhooyr/websocket's default 32KiB read limit so yamux can
	// carry frames larger than that without dropping the session
	// (fix plan review r2 P0).
	ws.SetReadLimit(-1)
	return &wsConn{ctx: cctx, cancel: cancel, ws: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	c.rMu.Lock()
	defer c.rMu.Unlock()
	if len(c.rBuf) > 0 {
		n := copy(p, c.rBuf)
		c.rBuf = c.rBuf[n:]
		return n, nil
	}
	rctx := c.ctx
	if !c.deadR.IsZero() {
		var cancel context.CancelFunc
		rctx, cancel = context.WithDeadline(c.ctx, c.deadR)
		defer cancel()
	}
	_, data, err := c.ws.Read(rctx)
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	if n < len(data) {
		c.rBuf = data[n:]
	}
	return n, nil
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wMu.Lock()
	defer c.wMu.Unlock()
	wctx := c.ctx
	if !c.deadW.IsZero() {
		var cancel context.CancelFunc
		wctx, cancel = context.WithDeadline(c.ctx, c.deadW)
		defer cancel()
	}
	if err := c.ws.Write(wctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "close")
}

func (c *wsConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (c *wsConn) RemoteAddr() net.Addr               { return dummyAddr{} }
func (c *wsConn) SetDeadline(t time.Time) error      { c.deadR = t; c.deadW = t; return nil }
func (c *wsConn) SetReadDeadline(t time.Time) error  { c.deadR = t; return nil }
func (c *wsConn) SetWriteDeadline(t time.Time) error { c.deadW = t; return nil }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "ws" }
func (dummyAddr) String() string  { return "ws://tunnel" }

// ---------------------------------------------------------------------------
// yamux config + factories (spec §3.4)
// ---------------------------------------------------------------------------

func yamuxCfg() *yamux.Config {
	c := yamux.DefaultConfig()
	// EnableKeepAlive off — heartbeat handled at WS layer (Ping) per §4.3.
	c.EnableKeepAlive = false
	c.LogOutput = io.Discard
	return c
}

func serverMux(conn net.Conn) (*yamux.Session, error) {
	return yamux.Server(conn, yamuxCfg())
}

// ---------------------------------------------------------------------------
// stubTunnel — server-side representation of one live WS tunnel
// (spec §3.4 — copied from agentserver internal/tunnel/registry.go)
// ---------------------------------------------------------------------------

type stubTunnel struct {
	sandboxID string
	mux       *yamux.Session
	wsConn    *wsConn
	done      chan struct{}
	closeOnce sync.Once
}

func newStubTunnel(ctx context.Context, sid string, ws *websocket.Conn) (*stubTunnel, error) {
	conn := newWSConn(ctx, ws)
	session, err := serverMux(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("yamux server: %w", err)
	}
	t := &stubTunnel{sandboxID: sid, mux: session, wsConn: conn, done: make(chan struct{})}
	go t.watch()
	// Accept + discard agent-opened control streams (fix code review r1 P1-1).
	// Real agentsdk clients open a StreamTypeControl stream on connect + every
	// heartbeat (see agentserver@v0.69.9 pkg/agentsdk/client.go:sendHeartbeat).
	// Without an Accept loop, yamux's accept backlog fills up and eventually
	// blocks the peer, breaking long-lived tunnels. We do not process the
	// control payload (agent info) — smoke does not need agent-side heartbeats
	// (spec §1.2 non-goal).
	go t.acceptAndDiscard()
	return t, nil
}

// acceptAndDiscard runs a background loop draining any agent-opened streams
// (typically StreamTypeControl heartbeats). Each accepted stream is fully
// drained and closed so yamux flow-control credit is returned promptly.
func (t *stubTunnel) acceptAndDiscard() {
	for {
		stream, err := t.mux.Accept()
		if err != nil {
			// Session closed or shutdown; stop.
			return
		}
		go func(s io.ReadWriteCloser) {
			// Drain best-effort. writeStreamHeader/readStreamHeader is 5+N bytes;
			// even without parsing we discard the payload up to session close.
			_, _ = io.Copy(io.Discard, s)
			_ = s.Close()
		}(stream)
	}
}

// watch closes t (through Close/closeOnce) when the yamux session dies.
// Never closes done directly — spec §3.4 note on double-close panic.
func (t *stubTunnel) watch() {
	<-t.mux.CloseChan()
	t.Close()
}

func (t *stubTunnel) OpenHTTPStream(ctx context.Context, meta HTTPStreamMeta, body []byte) (HTTPResponseMeta, io.ReadCloser, error) {
	if t.mux == nil {
		return HTTPResponseMeta{}, nil, errors.New("nil yamux session")
	}
	stream, err := t.mux.Open()
	if err != nil {
		return HTTPResponseMeta{}, nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(dl)
	}
	meta.BodyLen = len(body)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	if err := writeStreamHeader(stream, streamTypeHTTP, metaJSON); err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	if len(body) > 0 {
		if _, err := stream.Write(body); err != nil {
			stream.Close()
			return HTTPResponseMeta{}, nil, err
		}
	}
	_, respMetaJSON, err := readStreamHeader(stream)
	if err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	var respMeta HTTPResponseMeta
	if err := json.Unmarshal(respMetaJSON, &respMeta); err != nil {
		stream.Close()
		return HTTPResponseMeta{}, nil, err
	}
	return respMeta, stream, nil
}

func (t *stubTunnel) Close() {
	t.closeOnce.Do(func() {
		if t.mux != nil {
			t.mux.Close()
		}
		if t.wsConn != nil {
			t.wsConn.Close()
		}
		close(t.done)
	})
}

func (t *stubTunnel) Done() <-chan struct{} { return t.done }
