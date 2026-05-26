package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	executorpkg "github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type executor struct {
	cfg agentbackend.ClaudeConfig
	env []string

	// Tunables; defaults set by newExecutor.
	binSelf          string // slave-agent binary path; default os.Args[0]
	maxQuestions    int    // default 5
	shutdownGraceSec int    // default 10

	// Test hook: when non-nil, invoked with the unix socket path right after
	// the listener is up, in its own goroutine.
	socketHookForTest func(string)
}

func newExecutor(cfg agentbackend.ClaudeConfig, env []string) *executor {
	return &executor{
		cfg:              cfg,
		env:              env,
		binSelf:          os.Args[0],
		maxQuestions:     5,
		shutdownGraceSec: 10,
	}
}

func newExecutorWithSocketHook(cfg agentbackend.ClaudeConfig, env []string, hook func(string)) *executor {
	e := newExecutor(cfg, env)
	e.socketHookForTest = hook
	return e
}

func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	sockDir, err := os.MkdirTemp("", "humanloop-")
	if err != nil {
		return agentbackend.Result{}, err
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "hl.sock")

	srv, err := humanloop.ListenIPC(sockPath)
	if err != nil {
		return agentbackend.Result{}, err
	}
	defer srv.Close()
	if e.socketHookForTest != nil {
		go e.socketHookForTest(sockPath)
	}

	mcpConfigPath := filepath.Join(sockDir, "mcp.json")
	if err := writeHumanloopMCPConfig(mcpConfigPath, e.binSelf, sockPath, e.maxQuestions); err != nil {
		return agentbackend.Result{}, err
	}

	args := append([]string{
		"--print",
		"--output-format=stream-json",
		"--verbose",
		"--append-system-prompt", agentbackend.CapabilityEpilogue,
		"--mcp-config", mcpConfigPath,
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

	// Send the prompt; signal completion so the pause goroutine doesn't close
	// stdin mid-write.
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		if t.SystemContext != "" {
			_, _ = io.WriteString(stdin, t.SystemContext+"\n\n")
		}
		_, _ = io.WriteString(stdin, t.Prompt)
	}()

	var awaiting *executorpkg.AskUserPayload
	pauseCh := make(chan struct{})
	go func() {
		defer close(pauseCh)
		p, err := srv.Receive()
		if err != nil {
			return
		}
		awaiting = &executorpkg.AskUserPayload{
			Kind:     p.Kind,
			Question: p.Question,
			Options:  p.Options,
			Context:  p.Context,
			Intent:   p.Intent,
			Target:   p.Target,
			Reason:   p.Reason,
		}
		<-promptDone
		_ = stdin.Close()
	}()

	var lastText strings.Builder
	var sessionID string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		var meta struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
			Message   struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &meta); err != nil {
			continue
		}
		if meta.Type == "system" && sessionID == "" && meta.SessionID != "" {
			sessionID = meta.SessionID
		}
		if meta.Type == "assistant" {
			for _, c := range meta.Message.Content {
				if c.Type == "text" {
					sink.Write("chunk", c.Text)
					lastText.WriteString(c.Text)
				}
			}
		}
	}

	// Wait with shutdown grace.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if awaiting == nil && err != nil {
			sink.Close()
			tail := stderrBuf.String()
			if len(tail) > 4096 {
				tail = tail[len(tail)-4096:]
			}
			if ctx.Err() == context.DeadlineExceeded {
				return agentbackend.Result{}, fmt.Errorf("timeout")
			}
			return agentbackend.Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
		}
	case <-time.After(time.Duration(e.shutdownGraceSec) * time.Second):
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}

	// Synchronize with the pause goroutine: if it produced a payload (pauseCh
	// closed), wait for it so the write to `awaiting` happens-before our read.
	// If the listener is still blocked in Accept, close it so the goroutine
	// exits cleanly. Either way, by the time we fall through, pauseCh is
	// closed -> awaiting is safe to read.
	select {
	case <-pauseCh:
		// pause goroutine finished (either with a payload or because the
		// listener was closed earlier).
	default:
		// pause goroutine still blocked in Receive(); the deferred srv.Close()
		// below would unblock it, but we want the happens-before now.
		_ = srv.Close()
		<-pauseCh
	}

	full := lastText.String()
	summary, change := agentbackend.SplitCapability(full)
	if change != "" {
		sink.Write("capability", change)
	}
	if awaiting != nil && sessionID == "" {
		sink.Close()
		return agentbackend.Result{}, fmt.Errorf("backend never emitted session_id; cannot resume")
	}
	sink.Close()
	return agentbackend.Result{
		Summary:          summary,
		CapabilityChange: change,
		SessionID:        sessionID,
		AwaitingUser:     awaiting,
	}, nil
}

func writeHumanloopMCPConfig(path, binSelf, sockPath string, max int) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"loom_humanloop": map[string]any{
				"command": binSelf,
				"args":    []string{"humanloop-mcp", sockPath, strconv.Itoa(max)},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
