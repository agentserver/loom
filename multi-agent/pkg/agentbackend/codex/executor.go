package codex

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
	cfg agentbackend.CodexConfig
	env []string
}

func newExecutor(cfg agentbackend.CodexConfig, env []string) *executor {
	return &executor{cfg: cfg, env: env}
}

// codexEvent mirrors the events emitted by `codex exec --json` on codex 0.130.0.
// The assistant text we care about appears on lines with:
//
//	type == "item.completed" AND item.type == "agent_message"
//
// with the response in item.text (flat string, NOT item.content[].text).
type codexEvent struct {
	Type string `json:"type"`
	Item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	args := []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-"}
	if len(e.cfg.ExtraArgs) > 0 {
		args = append(args, e.cfg.ExtraArgs...)
	}

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
		prompt := t.Prompt + agentbackend.CapabilityEpilogue
		if t.SystemContext != "" {
			io.WriteString(stdin, t.SystemContext+"\n\n")
		}
		io.WriteString(stdin, prompt)
	}()

	var lastText strings.Builder
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "item.completed" {
			continue
		}
		if ev.Item.Type != "agent_message" {
			continue
		}
		if ev.Item.Text == "" {
			continue
		}
		sink.Write("chunk", ev.Item.Text)
		lastText.WriteString(ev.Item.Text)
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
		return agentbackend.Result{}, fmt.Errorf("codex exit: %v: %s", err, tail)
	}

	full := lastText.String()
	summary, change := agentbackend.SplitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	sink.Close()
	return agentbackend.Result{Summary: summary, CapabilityChange: change}, nil
}
