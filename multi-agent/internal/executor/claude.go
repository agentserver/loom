package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type ClaudeConfig struct {
	Bin     string
	WorkDir string
	Args    []string
	Env     []string // extra env (KEY=VAL)
}

type ClaudeExecutor struct{ cfg ClaudeConfig }

func NewClaudeExecutor(cfg ClaudeConfig) *ClaudeExecutor { return &ClaudeExecutor{cfg: cfg} }

const capEpilogue = "\n\nWhen you finish, append a line `=== CAPABILITY ===` then 1-3 lines describing any persistent capability change to yourself. If none, write `NO_CAPABILITY_CHANGE`."

func (e *ClaudeExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	args := append([]string{"--print", "--output-format=stream-json", "--verbose", "--append-system-prompt", capEpilogue}, e.cfg.Args...)
	cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
	cmd.Dir = e.cfg.WorkDir
	cmd.Env = append(cmd.Environ(), e.cfg.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	go func() {
		defer stdin.Close()
		if t.SystemContext != "" {
			io.WriteString(stdin, t.SystemContext+"\n\n")
		}
		io.WriteString(stdin, t.Prompt)
	}()

	var lastText strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue // garbage line: skip per spec §5.5
		}
		if msg.Type != "assistant" {
			continue
		}
		for _, c := range msg.Message.Content {
			if c.Type != "text" {
				continue
			}
			sink.Write("chunk", c.Text)
			lastText.WriteString(c.Text)
		}
	}

	if err := cmd.Wait(); err != nil {
		defer sink.Close()
		if ctx.Err() == context.DeadlineExceeded {
			return Result{}, fmt.Errorf("timeout")
		}
		tail := stderrBuf.String()
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
		}
		return Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
	}

	full := lastText.String()
	summary, change := splitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	sink.Close()
	return Result{Summary: summary, CapabilityChange: change}, nil
}

func splitCapability(s string) (summary, change string) {
	const sep = "=== CAPABILITY ==="
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return strings.TrimSpace(s), ""
	}
	summary = strings.TrimSpace(s[:i])
	change = strings.TrimSpace(s[i+len(sep):])
	if change == "NO_CAPABILITY_CHANGE" {
		change = ""
	}
	return
}
