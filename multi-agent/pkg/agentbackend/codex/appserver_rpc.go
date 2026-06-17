package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

type appServerRPC struct {
	r io.Reader
	w io.Writer

	nextID atomic.Int64
	mu     sync.Mutex
	wait   map[int64]chan appServerRPCMessage

	writeMu        sync.Mutex
	onNotification func(appServerRPCMessage)
}

type appServerRPCMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *appServerError  `json:"error,omitempty"`
}

type appServerError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newAppServerRPC(r io.Reader, w io.Writer) *appServerRPC {
	return &appServerRPC{
		r:    r,
		w:    w,
		wait: make(map[int64]chan appServerRPCMessage),
	}
}

func (c *appServerRPC) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	rawID := json.RawMessage(strconv.FormatInt(id, 10))
	req := appServerRPCMessage{ID: &rawID, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}

	ch := make(chan appServerRPCMessage, 1)
	c.mu.Lock()
	c.wait[id] = ch
	c.mu.Unlock()

	if err := c.writeMessage(req); err != nil {
		c.mu.Lock()
		delete(c.wait, id)
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.wait, id)
		c.mu.Unlock()
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("app-server %s: %s", method, resp.Error.Message)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
}

func (c *appServerRPC) notify(method string, params any) error {
	msg := appServerRPCMessage{Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = b
	}
	return c.writeMessage(msg)
}

func (c *appServerRPC) readLoop(ctx context.Context) error {
	sc := bufio.NewScanner(c.r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var msg appServerRPCMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			return err
		}

		if msg.ID != nil {
			if id, ok := msg.numericID(); ok {
				c.dispatchResponse(id, msg)
			}
			continue
		}

		if msg.Method != "" && c.onNotification != nil {
			c.onNotification(msg)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return nil
}

func (c *appServerRPC) writeMessage(msg appServerRPCMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(b)
	return err
}

func (c *appServerRPC) dispatchResponse(id int64, msg appServerRPCMessage) {
	c.mu.Lock()
	ch := c.wait[id]
	delete(c.wait, id)
	c.mu.Unlock()

	if ch != nil {
		ch <- msg
	}
}

func (msg appServerRPCMessage) numericID() (int64, bool) {
	if msg.ID == nil {
		return 0, false
	}
	var id int64
	if err := json.Unmarshal(*msg.ID, &id); err != nil {
		return 0, false
	}
	return id, true
}
