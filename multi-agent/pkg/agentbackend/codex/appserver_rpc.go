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

	nextID      atomic.Int64
	mu          sync.Mutex
	wait        map[int64]chan appServerRPCResult
	terminalErr error

	writeMu        sync.Mutex
	onNotification func(appServerRPCMessage)
}

type appServerRPCResult struct {
	msg appServerRPCMessage
	err error
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
		wait: make(map[int64]chan appServerRPCResult),
	}
}

func (c *appServerRPC) call(ctx context.Context, method string, params any, out any) error {
	if err := c.terminalError(); err != nil {
		return err
	}

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

	ch := make(chan appServerRPCResult, 1)
	c.mu.Lock()
	if c.terminalErr != nil {
		err := c.terminalErr
		c.mu.Unlock()
		return err
	}
	c.wait[id] = ch
	c.mu.Unlock()

	if err := c.writeMessage(req); err != nil {
		c.mu.Lock()
		if c.wait[id] == ch {
			delete(c.wait, id)
		}
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		if c.wait[id] == ch {
			delete(c.wait, id)
		}
		c.mu.Unlock()
		return ctx.Err()
	case result := <-ch:
		if result.err != nil {
			return result.err
		}
		resp := result.msg
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
	// Scanner.Scan blocks until the reader yields data or is closed. The
	// owner must close the reader when canceling a blocked readLoop.
	sc := bufio.NewScanner(c.r)
	// Keep individual line-delimited JSON-RPC messages bounded.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		select {
		case <-ctx.Done():
			c.finishReadLoop(ctx.Err())
			return ctx.Err()
		default:
		}

		var msg appServerRPCMessage
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			c.finishReadLoop(err)
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
		c.finishReadLoop(err)
		return err
	}
	select {
	case <-ctx.Done():
		c.finishReadLoop(ctx.Err())
		return ctx.Err()
	default:
		c.finishReadLoop(io.EOF)
		return io.EOF
	}
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
		ch <- appServerRPCResult{msg: msg}
	}
}

func (c *appServerRPC) finishReadLoop(err error) {
	if err == nil {
		err = io.EOF
	}

	c.mu.Lock()
	if c.terminalErr != nil {
		c.mu.Unlock()
		return
	}
	c.terminalErr = err
	wait := c.wait
	c.wait = make(map[int64]chan appServerRPCResult)
	c.mu.Unlock()

	for _, ch := range wait {
		ch <- appServerRPCResult{err: err}
	}
}

func (c *appServerRPC) terminalError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalErr
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
