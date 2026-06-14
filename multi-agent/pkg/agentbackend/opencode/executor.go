package opencode

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

// executor spawns `opencode run` non-interactively. Closest reference
// is pkg/agentbackend/codex/executor.go — both bins read PROMPT from
// stdin (trailing `-` arg), emit nd-JSON events on stdout, and exit
// when stdin closes. The opencode-specific bit is humanloop MCP
// injection: opencode reads its MCP config from a JSON file pointed
// at by OPENCODE_CONFIG (env var), not from a CLI flag.
type executor struct {
	cfg agentbackend.Config
	env []string

	// Tunables; defaults set by newExecutor.
	binSelf          string // slave-agent binary path; default os.Args[0]
	maxQuestions     int    // default 5
	shutdownGraceSec int    // default 10

	// Test hook: when non-nil, invoked with the endpoint arg right after the
	// listener is up, in its own goroutine.
	socketHookForTest func(string)
}

func newExecutor(cfg agentbackend.Config, env []string) *executor {
	return &executor{
		cfg:              cfg,
		env:              env,
		binSelf:          os.Args[0],
		maxQuestions:     5,
		shutdownGraceSec: 10,
	}
}

// opencodeEvent matches the nd-JSON events emitted by `opencode run
// --format json …`. Schema verified against the opencode source
// (packages/opencode/src/cli/cmd/run.ts on sst/opencode dev branch):
// the emit() helper unconditionally merges three top-level fields
// onto every event line:
//
//	{ "type": <name>, "timestamp": <ms>, "sessionID": <sess.id>, ...data }
//
// Event type names (from the run.ts switch arms): step_start,
// step_finish, tool_use, text, reasoning, error.
//
// The `text` event carries the finalised assistant text on
// .part.text (only emitted once part.time.end is set so a single
// emission per text part — no delta concatenation needed). Other
// event types are ignored here; the LLMRunner and Backend.Run
// callers only care about assistant text + session id.
//
// Distinct from codex's snake_case session_id field — opencode
// uses camelCase sessionID.
//
// See pkg/agentbackend/opencode/testdata/opencode_run.ndjson header
// for the pre-flight capture + schema findings.
type opencodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Timestamp int64  `json:"timestamp"`
	Part      struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Text string `json:"text"`
		Time struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
	} `json:"part"`
}

// Run launches `opencode run --format=json --dangerously-skip-permissions`
// with the prompt fed via stdin (trailing `-` sentinel arg). The
// humanloop MCP server is injected by writing a temp opencode.json
// (see writeOpencodeHumanloopConfig) and pointing OPENCODE_CONFIG at it.
func (e *executor) Run(ctx context.Context, t agentbackend.Task, sink agentbackend.Sink) (agentbackend.Result, error) {
	prompt := t.Prompt + agentbackend.CapabilityEpilogue
	if t.SystemContext != "" {
		prompt = t.SystemContext + "\n\n" + prompt
	}
	args := append([]string{
		"run",
		"--format=json",
		"--dangerously-skip-permissions",
	}, e.cfg.ExtraArgs...)
	return e.runWithArgv(ctx, args, prompt, sink)
}

// RunResume re-invokes opencode with `run --session <id> --continue`
// so the model continues the conversation it paused on a humanloop
// ask. Prompt is rendered "User answered: <answer>" — the model sees
// a natural user turn responding to its prior request. Like Run(),
// humanloop MCP is injected so the model can pause AGAIN (multi-round
// Q&A is supported).
func (e *executor) RunResume(ctx context.Context, sessionID, answer string, sink agentbackend.Sink) (agentbackend.Result, error) {
	args := append([]string{
		"run",
		"--session", sessionID,
		"--continue",
		"--format=json",
		"--dangerously-skip-permissions",
	}, e.cfg.ExtraArgs...)
	prompt := "User answered: " + answer
	return e.runWithArgv(ctx, args, prompt, sink)
}

// runWithArgv is the shared pipeline: write the temp humanloop MCP
// config, spawn opencode with the given argv head (`--dir <workdir>`
// + trailing `-` are appended here) and OPENCODE_CONFIG env, feed
// `prompt` via stdin, parse the nd-JSON event stream, and handle
// pause-via-IPC with grace shutdown.
func (e *executor) runWithArgv(ctx context.Context, argvHead []string, prompt string, sink agentbackend.Sink) (agentbackend.Result, error) {
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

	cfgPath := filepath.Join(sockDir, "opencode.json")
	if err := writeOpencodeHumanloopConfig(cfgPath, e.binSelf, ep, e.maxQuestions); err != nil {
		return agentbackend.Result{}, err
	}

	args := append([]string{}, argvHead...)
	args = append(args, "--dir", e.cfg.WorkDir, "-") // workdir + stdin-prompt sentinel

	cmd := exec.CommandContext(ctx, e.cfg.Bin, args...)
	cmd.Dir = e.cfg.WorkDir
	cmd.Env = append(cmd.Environ(), e.env...)
	cmd.Env = append(cmd.Env, "OPENCODE_CONFIG="+cfgPath)

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
	// stdin mid-write. opencode reads PROMPT from stdin (the trailing `-`
	// arg) and waits for EOF, so we MUST close stdin after writing on the
	// happy path. Mirrors codex pattern.
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
		// Skip blank lines and `#` comments (the latter only matters for
		// fixture replay — real opencode never emits them, but the
		// testdata file has a header).
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		var ev opencodeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.SessionID != "" && sessionID == "" {
			sessionID = ev.SessionID
		}
		if text := extractAssistantText(ev); text != "" {
			sink.Write("chunk", text)
			lastText.WriteString(text)
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
			return agentbackend.Result{}, fmt.Errorf("opencode exit: %v: %s", err, tail)
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

	// Synchronize with the pause goroutine (same shape as codex): if it
	// produced a payload (pauseCh closed), wait for the write-to-`awaiting`
	// to happens-before our read. Otherwise close the listener so the
	// goroutine exits cleanly.
	select {
	case <-pauseCh:
	default:
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
		return agentbackend.Result{}, fmt.Errorf("backend never emitted sessionID; cannot resume")
	}
	if killed {
		sink.Close()
		return agentbackend.Result{}, fmt.Errorf("opencode did not exit within %ds grace window after stdin close; graceful termination/forced kill applied", e.shutdownGraceSec)
	}
	sink.Close()
	return agentbackend.Result{
		Summary:          summary,
		CapabilityChange: change,
		SessionID:        sessionID,
		AwaitingUser:     awaiting,
	}, nil
}

// extractAssistantText pulls finalised assistant text out of an opencode
// event. Schema per the source: only `type:"text"` events with a
// non-empty `part.text` and `part.time.end > 0` carry the final text
// the user should see. Earlier (in-progress) text parts are NOT
// emitted by opencode in --format json mode, so the time.end check is
// defence-in-depth rather than load-bearing — kept so we don't grab
// partial deltas if the schema ever changes.
func extractAssistantText(ev opencodeEvent) string {
	if ev.Type != "text" {
		return ""
	}
	if ev.Part.Type != "text" && ev.Part.Type != "" {
		// Defensive: refuse to pull text out of a part whose declared
		// type isn't `text` (e.g. a reasoning part that happens to
		// have a Text field set by accident). Treat empty Part.Type
		// as legacy / unspecified and allow it through.
		return ""
	}
	if ev.Part.Time.End == 0 {
		return ""
	}
	return ev.Part.Text
}

// writeOpencodeHumanloopConfig writes a temp opencode.json with a single
// loom_humanloop MCP server. opencode reads MCP config from
// OPENCODE_CONFIG env (see https://opencode.ai/docs/mcp-servers/).
// type="local" means opencode spawns the command as a subprocess; we
// point command[0] at binSelf (slave-agent) with the humanloop-mcp
// subcommand + endpoint args — slave-agent already knows how to serve
// that path (see internal/humanloop/server.go).
//
// Why a config file (and not a CLI flag like claude/codex): opencode
// doesn't expose CLI overrides for MCP — only the config file and the
// OPENCODE_CONFIG env. A temp file per Run() is self-contained and
// doesn't pollute the user's ~/.config/opencode/opencode.json.
func writeOpencodeHumanloopConfig(path, binSelf string, ep humanloop.Endpoint, max int) error {
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			"loom_humanloop": map[string]any{
				"type":    "local",
				"command": []string{binSelf, "humanloop-mcp", humanloop.EndpointArg(ep), strconv.Itoa(max)},
				"enabled": true,
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
