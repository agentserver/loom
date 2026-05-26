package codex

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
	cfg agentbackend.CodexConfig
	env []string

	// Tunables; defaults set by newExecutor.
	binSelf          string // slave-agent binary path; default os.Args[0]
	maxQuestions     int    // default 5
	shutdownGraceSec int    // default 10

	// Test hook: when non-nil, invoked with the unix socket path right after
	// the listener is up, in its own goroutine.
	socketHookForTest func(string)
}

func newExecutor(cfg agentbackend.CodexConfig, env []string) *executor {
	return &executor{
		cfg:              cfg,
		env:              env,
		binSelf:          os.Args[0],
		maxQuestions:     5,
		shutdownGraceSec: 10,
	}
}

func newExecutorWithSocketHook(cfg agentbackend.CodexConfig, env []string, hook func(string)) *executor {
	e := newExecutor(cfg, env)
	e.socketHookForTest = hook
	return e
}

// codexEvent mirrors the events emitted by `codex exec --json` on codex 0.130.0.
//
//   - `{"type":"thread.started","thread_id":"…"}` — first event; we capture
//     thread_id as the session id used by RunResume.
//   - `{"type":"item.completed","item":{"type":"agent_message","text":"…"}}`
//     — assistant text (item.text is a flat string, NOT item.content[]).
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

// Run launches `codex exec --json …` with the humanloop MCP server injected
// inline via `-c mcp_servers.loom_humanloop.command=…` overrides. The prompt
// (Task.Prompt + CapabilityEpilogue) is fed via stdin because the trailing `-`
// arg tells codex to read PROMPT from stdin.
func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	prompt := t.Prompt + agentbackend.CapabilityEpilogue
	if t.SystemContext != "" {
		prompt = t.SystemContext + "\n\n" + prompt
	}
	args := append([]string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
	}, e.cfg.ExtraArgs...)
	return e.runWithArgv(ctx, args, prompt, sink)
}

// RunResume re-invokes the codex backend with `exec resume <sessionID>` so the
// model continues the conversation it paused. The prompt is rendered as
// "User answered: <answer>" so the model sees a natural user turn responding
// to its prior ask_user / request_permission call.
//
// Like Run, RunResume injects the humanloop MCP server so the model can pause
// AGAIN (multi-round questions are explicitly supported).
func (e *executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	args := append([]string{
		"exec",
		"resume",
		sessionID,
		"--json",
	}, e.cfg.ExtraArgs...)
	prompt := "User answered: " + answer
	return e.runWithArgv(ctx, args, prompt, sink)
}

// runWithArgv is the shared pipeline: spawn codex with the given argv head
// (mcp injection + trailing `-` are appended here), feed `prompt` via stdin,
// parse stream-json, and handle pause-via-IPC with grace shutdown.
func (e *executor) runWithArgv(ctx context.Context, argvHead []string, prompt string, sink agentbackend.Sink) (agentbackend.Result, error) {
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

	// Inline `-c` overrides for humanloop MCP. codex parses each value as TOML,
	// falling back to literal string. Wrapping command in double-quotes makes
	// it a TOML string; args is a TOML array of strings.
	mcpArgs := []string{
		"-c", fmt.Sprintf(`mcp_servers.loom_humanloop.command="%s"`, e.binSelf),
		"-c", fmt.Sprintf(`mcp_servers.loom_humanloop.args=["humanloop-mcp","%s","%s"]`,
			sockPath, strconv.Itoa(e.maxQuestions)),
	}
	args := append([]string{}, argvHead...)
	args = append(args, mcpArgs...)
	args = append(args, "-") // PROMPT from stdin

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
	// stdin mid-write. Codex reads PROMPT from stdin (the trailing `-` arg)
	// and waits for EOF, so we MUST close stdin after writing on the happy
	// path. The pause goroutine sequences itself behind promptDone before
	// re-closing, so the double-close (the second one becomes a no-op error
	// we ignore) is safe.
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		_, _ = io.WriteString(stdin, prompt)
		_ = stdin.Close()
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
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type == "thread.started" && sessionID == "" && ev.ThreadID != "" {
			sessionID = ev.ThreadID
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

	// Wait with shutdown grace.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	killed := false
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
			return agentbackend.Result{}, fmt.Errorf("codex exit: %v: %s", err, tail)
		}
	case <-time.After(time.Duration(e.shutdownGraceSec) * time.Second):
		killed = true
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
		// pause goroutine still blocked in Receive(); close the listener so it
		// exits cleanly and the write to `awaiting` happens-before our read.
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
		return agentbackend.Result{}, fmt.Errorf("backend never emitted thread_id; cannot resume (no session_id)")
	}
	if killed {
		sink.Close()
		return agentbackend.Result{}, fmt.Errorf("codex did not exit within %ds grace window after stdin close; SIGTERM/SIGKILL applied", e.shutdownGraceSec)
	}
	sink.Close()
	return agentbackend.Result{
		Summary:          summary,
		CapabilityChange: change,
		SessionID:        sessionID,
		AwaitingUser:     awaiting,
	}, nil
}
