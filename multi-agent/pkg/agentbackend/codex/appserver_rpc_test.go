package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

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
	if err := <-serverErrCh; err != nil {
		t.Fatal(err)
	}
	if err := <-readErrCh; err != nil {
		t.Fatal(err)
	}
}

func TestAppServerRPCDispatchesNotifications(t *testing.T) {
	clientReader := strings.NewReader(`{"method":"item/agentMessage/delta","params":{"threadId":"thr","turnId":"turn","itemId":"item","delta":"hi"}}` + "\n")
	c := newAppServerRPC(clientReader, io.Discard)
	ch := make(chan appServerRPCMessage, 1)
	c.onNotification = func(msg appServerRPCMessage) { ch <- msg }

	if err := c.readLoop(context.Background()); err != nil {
		t.Fatal(err)
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

func TestAppServerRPCDropsUnknownResponses(t *testing.T) {
	clientReader := strings.NewReader(`{"id":99,"result":{"ignored":true}}` + "\n")
	c := newAppServerRPC(clientReader, io.Discard)
	called := false
	c.onNotification = func(appServerRPCMessage) { called = true }

	if err := c.readLoop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("unknown response was dispatched as notification")
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
