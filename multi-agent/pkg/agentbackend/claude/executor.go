package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type executor struct {
	cfg agentbackend.ClaudeConfig
	env []string
}

func newExecutor(cfg agentbackend.ClaudeConfig, env []string) *executor {
	return &executor{cfg: cfg, env: env}
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	args := append([]string{
		"--print",
		"--output-format=stream-json",
		"--verbose",
		"--append-system-prompt", agentbackend.CapabilityEpilogue,
	}, e.cfg.ExtraArgs...)

	cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
	cmd.Dir = e.cfg.WorkDir
	cmd.Env = append(cmd.Environ(), e.env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return agentbackend.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return agentbackend.Result{}, err
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return agentbackend.Result{}, err
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
			continue
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
			return agentbackend.Result{}, fmt.Errorf("timeout")
		}
		tail := stderrBuf.String()
		if len(tail) > 4096 {
			tail = tail[len(tail)-4096:]
		}
		return agentbackend.Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
	}

	full := lastText.String()
	summary, change := agentbackend.SplitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	sink.Close()
	return agentbackend.Result{Summary: summary, CapabilityChange: change}, nil
}
