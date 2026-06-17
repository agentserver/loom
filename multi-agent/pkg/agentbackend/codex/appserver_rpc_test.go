package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

const appServerRPCTestTimeout = 2 * time.Second

func TestAppServerRPCRequestResponse(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()
	defer serverReader.Close()
	defer clientWriter.Close()

	c := newAppServerRPC(clientReader, clientWriter)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- c.readLoop(ctx)
	}()

	serverErrCh := make(chan error, 1)
	go func() {
		defer serverWriter.Close()

		sc := bufio.NewScanner(serverReader)
		if !sc.Scan() {
			serverErrCh <- sc.Err()
			return
		}
		var req appServerRPCMessage
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			serverErrCh <- err
			return
		}
		if req.Method != "thread/resume" {
			serverErrCh <- fmt.Errorf("method=%q", req.Method)
			return
		}
		if req.ID == nil {
			serverErrCh <- fmt.Errorf("missing id")
			return
		}
		var id int64
		if err := json.Unmarshal(*req.ID, &id); err != nil {
			serverErrCh <- fmt.Errorf("id is not numeric: %w", err)
			return
		}
		var params struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			serverErrCh <- err
			return
		}
		if params.ThreadID != "thr-1" {
			serverErrCh <- fmt.Errorf("threadId=%q", params.ThreadID)
			return
		}

		_, err := serverWriter.Write([]byte(fmt.Sprintf(`{"id":%d,"result":{"ok":true}}`+"\n", id)))
		serverErrCh <- err
	}()

	var result struct {
		OK bool `json:"ok"`
	}
	if err := c.call(ctx, "thread/resume", map[string]string{"threadId": "thr-1"}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("result=%+v", result)
	}
	if err := receiveWithin(t, serverErrCh, "server result"); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithin(t, readErrCh, "read loop result"); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}
}

func TestAppServerRPCDispatchesNotifications(t *testing.T) {
	clientReader := strings.NewReader(`{"method":"item/agentMessage/delta","params":{"threadId":"thr","turnId":"turn","itemId":"item","delta":"hi"}}` + "\n")
	c := newAppServerRPC(clientReader, io.Discard)
	ch := make(chan appServerRPCMessage, 1)
	c.onNotification = func(msg appServerRPCMessage) { ch <- msg }

	if err := c.readLoop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}

	select {
	case msg := <-ch:
		if msg.Method != "item/agentMessage/delta" {
			t.Fatalf("method=%q", msg.Method)
		}
	default:
		t.Fatal("notification was not dispatched")
	}
}

func TestAppServerRPCDispatchesInboundServerRequestToHook(t *testing.T) {
	clientReader := strings.NewReader(`{"id":7,"method":"approval/request","params":{"threadId":"thr-1","turnId":"turn-1"}}` + "\n")
	c := newAppServerRPC(clientReader, io.Discard)
	requestCh := make(chan appServerRPCMessage, 1)
	c.onRequest = func(msg appServerRPCMessage) { requestCh <- msg }

	if err := c.readLoop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}

	msg := receiveWithin(t, requestCh, "server request")
	if msg.Method != "approval/request" {
		t.Fatalf("method=%q, want approval/request", msg.Method)
	}
	if msg.ID == nil || strings.TrimSpace(string(*msg.ID)) != "7" {
		t.Fatalf("id=%v, want 7", msg.ID)
	}
}

func TestAppServerRPCServerRequestHookDoesNotBlockReadLoop(t *testing.T) {
	clientReader := strings.NewReader(
		`{"id":7,"method":"approval/request","params":{"threadId":"thr-1","turnId":"turn-1"}}` + "\n" +
			`{"method":"item/agentMessage/delta","params":{"threadId":"thr-1","turnId":"turn-1","itemId":"item","delta":"hi"}}` + "\n",
	)
	c := newAppServerRPC(clientReader, io.Discard)
	blockRequest := make(chan struct{})
	requestStarted := make(chan struct{}, 1)
	notificationCh := make(chan appServerRPCMessage, 1)
	c.onRequest = func(appServerRPCMessage) {
		requestStarted <- struct{}{}
		<-blockRequest
	}
	c.onNotification = func(msg appServerRPCMessage) { notificationCh <- msg }
	defer close(blockRequest)

	if err := c.readLoop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}

	receiveWithin(t, requestStarted, "server request hook start")
	msg := receiveWithin(t, notificationCh, "notification after blocked request hook")
	if msg.Method != "item/agentMessage/delta" {
		t.Fatalf("notification method=%q, want item/agentMessage/delta", msg.Method)
	}
}

func TestAppServerRPCRejectsUnhandledServerRequest(t *testing.T) {
	clientReader := strings.NewReader(`{"id":7,"method":"approval/request","params":{"threadId":"thr-1","turnId":"turn-1"}}` + "\n")
	writeCh := make(chan []byte, 1)
	c := newAppServerRPC(clientReader, writerFunc(func(p []byte) (int, error) {
		writeCh <- append([]byte(nil), p...)
		return len(p), nil
	}))

	if err := c.readLoop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}

	var resp appServerRPCMessage
	if err := json.Unmarshal(receiveWithin(t, writeCh, "server request error response"), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == nil || strings.TrimSpace(string(*resp.ID)) != "7" {
		t.Fatalf("response id=%v, want 7", resp.ID)
	}
	if resp.Method != "" {
		t.Fatalf("response method=%q, want empty", resp.Method)
	}
	if resp.Error == nil || resp.Error.Code != -32601 || !strings.Contains(resp.Error.Message, "approval/request") {
		t.Fatalf("response error=%+v, want unsupported approval/request error", resp.Error)
	}
}

func TestAppServerRPCDropsUnknownResponses(t *testing.T) {
	clientReader := strings.NewReader(`{"id":99,"result":{"ignored":true}}` + "\n")
	c := newAppServerRPC(clientReader, io.Discard)
	called := false
	c.onNotification = func(appServerRPCMessage) { called = true }

	if err := c.readLoop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}
	if called {
		t.Fatal("unknown response was dispatched as notification")
	}
}

func TestAppServerRPCPendingCallReturnsErrorOnEOF(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()

	requestWritten := make(chan struct{}, 1)
	c := newAppServerRPC(clientReader, writerFunc(func(p []byte) (int, error) {
		requestWritten <- struct{}{}
		return len(p), nil
	}))

	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- c.readLoop(context.Background())
	}()

	callErrCh := make(chan error, 1)
	go func() {
		callErrCh <- c.call(context.Background(), "thread/resume", nil, nil)
	}()

	receiveWithin(t, requestWritten, "request write")
	if err := serverWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithin(t, callErrCh, "pending call error"); err == nil {
		t.Fatal("call returned nil error, want terminal read error")
	}
	if err := receiveWithin(t, readErrCh, "read loop result"); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}

	laterCallErrCh := make(chan error, 1)
	go func() {
		laterCallErrCh <- c.call(context.Background(), "thread/resume", nil, nil)
	}()
	if err := receiveWithin(t, laterCallErrCh, "later call error"); err == nil {
		t.Fatal("later call returned nil error, want terminal read error")
	}
	select {
	case <-requestWritten:
		t.Fatal("later call wrote a request after terminal read error")
	default:
	}
}

func TestAppServerRPCPendingCallReturnsErrorOnMalformedJSON(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()

	requestWritten := make(chan struct{}, 1)
	c := newAppServerRPC(clientReader, writerFunc(func(p []byte) (int, error) {
		requestWritten <- struct{}{}
		return len(p), nil
	}))

	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- c.readLoop(context.Background())
	}()

	callErrCh := make(chan error, 1)
	go func() {
		callErrCh <- c.call(context.Background(), "thread/resume", nil, nil)
	}()

	receiveWithin(t, requestWritten, "request write")
	if _, err := serverWriter.Write([]byte("{not-json}\n")); err != nil {
		t.Fatal(err)
	}
	readErr := receiveWithin(t, readErrCh, "read loop result")
	if readErr == nil {
		t.Fatal("readLoop returned nil error, want JSON error")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(readErr, &syntaxErr) {
		t.Fatalf("readLoop error = %T %v, want *json.SyntaxError", readErr, readErr)
	}
	if err := receiveWithin(t, callErrCh, "pending call error"); err == nil {
		t.Fatal("call returned nil error, want terminal read error")
	}
}

func TestAppServerRPCCallReturnsTypedProtocolError(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()

	c := newAppServerRPC(clientReader, writerFunc(func(p []byte) (int, error) {
		var req appServerRPCMessage
		if err := json.Unmarshal(p, &req); err != nil {
			return 0, err
		}
		writeFakeAppServerErrorCode(t, serverWriter, *req.ID, -32601, "Method not found")
		return len(p), nil
	}))

	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- c.readLoop(context.Background())
	}()

	err := c.call(context.Background(), "turn/start", nil, nil)
	var rpcErr *appServerRPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("call error = %T %v, want *appServerRPCError", err, err)
	}
	if rpcErr.Method != "turn/start" || rpcErr.Code != -32601 || rpcErr.Message != "Method not found" {
		t.Fatalf("rpc error=%+v, want turn/start -32601 Method not found", rpcErr)
	}

	if err := serverWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithin(t, readErrCh, "read loop result"); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}
}

func TestAppServerRPCCallWithCanceledContextDoesNotWriteRequest(t *testing.T) {
	wroteRequest := make(chan struct{}, 1)
	c := newAppServerRPC(strings.NewReader(""), writerFunc(func(p []byte) (int, error) {
		wroteRequest <- struct{}{}
		return len(p), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.call(ctx, "thread/resume", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("call error = %v, want context canceled", err)
	}
	select {
	case <-wroteRequest:
		t.Fatal("call wrote a request with canceled context")
	default:
	}
}

func TestAppServerRPCCallCanceledBeforeWriteDoesNotWriteRequest(t *testing.T) {
	wroteRequest := make(chan struct{}, 1)
	c := newAppServerRPC(strings.NewReader(""), writerFunc(func(p []byte) (int, error) {
		wroteRequest <- struct{}{}
		return len(p), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.writeMu.Lock()
	callErrCh := make(chan error, 1)
	go func() {
		callErrCh <- c.call(ctx, "thread/resume", nil, nil)
	}()

	waitUntil(t, "registered waiter", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.wait) == 1
	})
	cancel()
	c.writeMu.Unlock()

	if err := receiveWithin(t, callErrCh, "call error"); !errors.Is(err, context.Canceled) {
		t.Fatalf("call error = %v, want context canceled", err)
	}
	select {
	case <-wroteRequest:
		t.Fatal("call wrote a request after context canceled before write")
	default:
	}
	c.mu.Lock()
	waiters := len(c.wait)
	c.mu.Unlock()
	if waiters != 0 {
		t.Fatalf("waiters = %d, want 0", waiters)
	}
}

func TestAppServerRPCCallQueuedBeforeTerminalReadDoesNotWriteRequest(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	defer clientReader.Close()
	defer serverWriter.Close()

	wroteRequest := make(chan struct{}, 1)
	c := newAppServerRPC(clientReader, writerFunc(func(p []byte) (int, error) {
		wroteRequest <- struct{}{}
		return len(p), nil
	}))

	readErrCh := make(chan error, 1)
	go func() {
		readErrCh <- c.readLoop(context.Background())
	}()

	c.writeMu.Lock()
	callErrCh := make(chan error, 1)
	go func() {
		callErrCh <- c.call(context.Background(), "thread/resume", nil, nil)
	}()

	waitUntil(t, "registered waiter", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.wait) == 1
	})
	if err := serverWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := receiveWithin(t, readErrCh, "read loop result"); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}
	c.writeMu.Unlock()

	if err := receiveWithin(t, callErrCh, "call error"); !errors.Is(err, io.EOF) {
		t.Fatalf("call error = %v, want EOF", err)
	}
	select {
	case <-wroteRequest:
		t.Fatal("call wrote a request after terminal read error")
	default:
	}
}

func TestAppServerRPCNotifyWritesNotification(t *testing.T) {
	var out strings.Builder
	c := newAppServerRPC(strings.NewReader(""), &out)

	if err := c.notify("thread/interrupt", map[string]string{"threadId": "thr-1"}); err != nil {
		t.Fatal(err)
	}

	var msg appServerRPCMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.ID != nil {
		t.Fatalf("id=%s, want omitted", string(*msg.ID))
	}
	if msg.Method != "thread/interrupt" {
		t.Fatalf("method=%q", msg.Method)
	}
	var params struct {
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "thr-1" {
		t.Fatalf("threadId=%q", params.ThreadID)
	}
}

func TestAppServerRPCNotifyAfterTerminalReadErrorDoesNotWrite(t *testing.T) {
	wroteRequest := make(chan struct{}, 1)
	c := newAppServerRPC(strings.NewReader(""), writerFunc(func(p []byte) (int, error) {
		wroteRequest <- struct{}{}
		return len(p), nil
	}))

	if err := c.readLoop(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("readLoop error = %v, want EOF", err)
	}
	if err := c.notify("thread/interrupt", nil); !errors.Is(err, io.EOF) {
		t.Fatalf("notify error = %v, want EOF", err)
	}
	select {
	case <-wroteRequest:
		t.Fatal("notify wrote after terminal read error")
	default:
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) {
	return f(p)
}

func receiveWithin[T any](t *testing.T, ch <-chan T, name string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(appServerRPCTestTimeout):
		t.Fatalf("timed out waiting for %s", name)
		var zero T
		return zero
	}
}

func waitUntil(t *testing.T, name string, fn func() bool) {
	t.Helper()
	deadline := time.After(appServerRPCTestTimeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", name)
		case <-tick.C:
		}
	}
}
