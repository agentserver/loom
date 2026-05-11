package executor

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/yourorg/multi-agent/internal/capability"
)

// SmokeLaunchPython spawns `python3 path`, sends one tools/list JSON-RPC
// request to its stdin, and waits up to timeout for a single line of stdout
// that decodes as a valid response. Returns the tool descriptors. Always
// kills the subprocess before returning.
func SmokeLaunchPython(ctx context.Context, path string, timeout time.Duration) ([]capability.MCPToolDescriptor, error) {
	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(subCtx, "python3", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	if _, err := stdin.Write(req); err != nil {
		return nil, fmt.Errorf("smoke write: %w", err)
	}
	_ = stdin.Close()

	type doneT struct {
		line []byte
		err  error
	}
	ch := make(chan doneT, 1)
	go func() {
		r := bufio.NewReaderSize(stdout, 1<<20)
		line, err := r.ReadBytes('\n')
		ch <- doneT{line, err}
	}()
	select {
	case <-subCtx.Done():
		return nil, fmt.Errorf("smoke timeout after %s", timeout)
	case d := <-ch:
		if d.err != nil {
			return nil, fmt.Errorf("smoke read: %w", d.err)
		}
		out, rpcErr, err := parseMCPToolListResponse(d.line, "")
		if err != nil {
			return nil, fmt.Errorf("smoke parse: %w (body=%q)", err, string(d.line))
		}
		if rpcErr != nil {
			return nil, fmt.Errorf("smoke server error: %s", *rpcErr)
		}
		return out, nil
	}
}
