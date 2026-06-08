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
	"time"

	executorpkg "github.com/yourorg/multi-agent/internal/executor"
	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/internal/platform"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type executor struct {
	cfg agentbackend.ClaudeConfig
	env []string

	// Tunables; defaults set by newExecutor.
	binSelf          string // slave-agent binary path; default os.Args[0]
	maxQuestions     int    // default 5
	shutdownGraceSec int    // default 10

	// Test hook: when non-nil, invoked with the endpoint arg right after the
	// listener is up, in its own goroutine.
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
	srv, ep, err := humanloop.ListenIPC(sockDir)
	if err != nil {
		return agentbackend.Result{}, err
	}
	defer srv.Close()
	if e.socketHookForTest != nil {
		go e.socketHookForTest(humanloop.EndpointArg(ep))
	}

	mcpConfigPath := filepath.Join(sockDir, "mcp.json")
	if err := writeHumanloopMCPConfig(mcpConfigPath, e.binSelf, ep, e.maxQuestions); err != nil {
		return agentbackend.Result{}, err
	}

	args := append([]string{
		"--print",
		"--output-format=stream-json",
		"--verbose",
		"--append-system-prompt", agentbackend.CapabilityEpilogue,
		"--mcp-config", mcpConfigPath,
		// Allow humanloop tools without prompting (default permission mode
		// would reject the MCP tool call and end the turn with
		// permission_denials, never pausing the chat). The two tools are the
		// whole point of the injection — they must be callable.
		"--allowedTools", "mcp__loom_humanloop__ask_user,mcp__loom_humanloop__request_permission",
		// Disable the built-in AskUserQuestion: in --print mode no human is
		// attached to answer it, so claude returns a synthetic "cancel" and
		// the model degrades to plain-text questions, bypassing humanloop.
		// Force the model onto mcp__loom_humanloop__ask_user instead.
		"--disallowedTools", "AskUserQuestion",
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
	// stdin mid-write. We MUST close stdin after writing because `claude --print`
	// reads stdin until EOF to know the prompt is complete — without the close
	// it hangs indefinitely on the happy path. The pause goroutine's later
	// stdin.Close is a no-op double-close (harmless, ignored error).
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		defer stdin.Close()
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
			return agentbackend.Result{}, fmt.Errorf("claude exit: %v: %s", err, tail)
		}
	case <-time.After(time.Duration(e.shutdownGraceSec) * time.Second):
		killed = true
		_ = platform.TerminateProcess(cmd.Process)
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
	if killed {
		sink.Close()
		return agentbackend.Result{}, fmt.Errorf("claude did not exit within %ds grace window after stdin close; graceful termination/forced kill applied", e.shutdownGraceSec)
	}
	sink.Close()
	return agentbackend.Result{
		Summary:          summary,
		CapabilityChange: change,
		SessionID:        sessionID,
		AwaitingUser:     awaiting,
	}, nil
}

// RunResume re-invokes the claude backend with --resume <sessionID> so the
// model continues the conversation it paused. The prompt is rendered as
// "User answered: <answer>" so the model sees a natural user turn responding
// to its prior ask_user / request_permission call.
//
// Like Run, RunResume injects the humanloop MCP server so the model can pause
// AGAIN (multi-round questions are explicitly supported).
func (e *executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	// Build a sub-executor with --resume <sessionID> prepended to ExtraArgs,
	// then delegate to Run. The "User answered:" prefix is the contract
	// between us and the model: the spec's CapabilityEpilogue paragraph (Task
	// 15) explains that any user turn after an ask_user/request_permission
	// tool call carries this prefix.
	cfg := e.cfg
	cfg.ExtraArgs = append([]string{"--resume", sessionID}, cfg.ExtraArgs...)
	sub := *e
	sub.cfg = cfg
	return sub.Run(ctx, agentbackend.Task{Prompt: "User answered: " + answer}, sink)
}

func writeHumanloopMCPConfig(path, binSelf string, ep humanloop.Endpoint, max int) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"loom_humanloop": map[string]any{
				"command": binSelf,
				"args":    []string{"humanloop-mcp", humanloop.EndpointArg(ep), strconv.Itoa(max)},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
