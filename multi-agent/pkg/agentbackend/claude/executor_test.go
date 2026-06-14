package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
	mu     sync.Mutex
	events []struct{ Type, Data string }
	closed bool
}

func (c *captureSink) Write(t, d string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, struct{ Type, Data string }{t, d})
}
func (c *captureSink) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func fakeClaudePath(t *testing.T) string {
	t.Helper()
	return buildFakeClaude(t, `package main
import (
	"fmt"
	"os"
	"strconv"
	"time"
)
func emit(s string) { fmt.Println(s) }
func main() {
	mode := os.Getenv("FAKE_CLAUDE_MODE")
	if mode == "" {
		mode = "nochange"
	}
	switch mode {
	case "normal":
		emit(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}`+"`"+`)
		emit(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}`+"`"+`)
		emit(`+"`"+`{"type":"result","subtype":"success"}`+"`"+`)
	case "capability":
		emit(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"installed foo\n=== CAPABILITY ===\nfoo CLI now available"}]}}`+"`"+`)
		emit(`+"`"+`{"type":"result","subtype":"success"}`+"`"+`)
	case "nochange":
		emit(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"answer\n=== CAPABILITY ===\nNO_CAPABILITY_CHANGE"}]}}`+"`"+`)
		emit(`+"`"+`{"type":"result","subtype":"success"}`+"`"+`)
	case "exit1":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(1)
	case "sleep":
		seconds, _ := strconv.Atoi(os.Getenv("FAKE_CLAUDE_SLEEP"))
		if seconds <= 0 {
			seconds = 30
		}
		time.Sleep(time.Duration(seconds) * time.Second)
	case "garbage":
		emit("not json at all")
		emit(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`+"`"+`)
		emit(`+"`"+`{"type":"result","subtype":"success"}`+"`"+`)
	default:
		fmt.Fprintln(os.Stderr, "unknown FAKE_CLAUDE_MODE: "+mode)
		os.Exit(2)
	}
}
`)
}

func TestExecutorParsesAssistantText(t *testing.T) {
	b := New(agentbackend.Config{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=normal"})
	sink := &captureSink{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := b.Run(ctx, agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "hello world", res.Summary)
	require.Equal(t, "", res.CapabilityChange)
	require.True(t, sink.closed)

	var got string
	for _, ev := range sink.events {
		if ev.Type == "chunk" {
			got += ev.Data
		}
	}
	require.Equal(t, "hello world", got)
}

func TestExecutor_CapabilityParsed(t *testing.T) {
	b := New(agentbackend.Config{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=capability"})
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "installed foo", res.Summary)
	require.Equal(t, "foo CLI now available", res.CapabilityChange)

	var sawCap bool
	for _, ev := range sink.events {
		if ev.Type == "capability" && ev.Data == "foo CLI now available" {
			sawCap = true
		}
	}
	require.True(t, sawCap, "expected capability event before close")
}

func TestExecutor_NoCapabilityChange(t *testing.T) {
	b := New(agentbackend.Config{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=nochange"})
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "answer", res.Summary)
	require.Equal(t, "", res.CapabilityChange)
}

func TestExecutor_Exit1(t *testing.T) {
	b := New(agentbackend.Config{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=exit1"})
	sink := &captureSink{}
	_, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestExecutor_Timeout(t *testing.T) {
	b := New(agentbackend.Config{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=sleep", "FAKE_CLAUDE_SLEEP=10"})
	sink := &captureSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := b.Run(ctx, agentbackend.Task{Prompt: "ignored"}, sink)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout")
}

func TestExecutor_GarbageLines_StillCompletes(t *testing.T) {
	b := New(agentbackend.Config{
		Bin:     fakeClaudePath(t),
		WorkDir: t.TempDir(),
	}, []string{"FAKE_CLAUDE_MODE=garbage"})
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	require.NoError(t, err)
	require.Equal(t, "ok", res.Summary)
}

// writeFakeClaude builds a one-shot fake claude binary that emits the given
// stream-json frames (one per line) and exits 0.
func writeFakeClaude(t *testing.T, frames []string) string {
	t.Helper()
	body := "package main\nimport \"fmt\"\nfunc main() {\n"
	for _, f := range frames {
		body += fmt.Sprintf("fmt.Println(%q)\n", f)
	}
	body += "}\n"
	return buildFakeClaude(t, body)
}

// writeFakeClaudeReadsStdinThenExits emits a system frame, reads stdin until
// the parent closes it (the writer goroutine closes stdin right after writing
// the prompt — real claude reads stdin until EOF, then runs the conversation),
// then sleeps a beat so the IPC hook has time to dial in and trigger a pause,
// then emits a final assistant frame and exits 0.
func writeFakeClaudeReadsStdinThenExits(t *testing.T, sessionID string) string {
	t.Helper()
	return buildFakeClaude(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
	"time"
)
func main() {
	fmt.Println(%q)
	_, _ = io.Copy(io.Discard, os.Stdin)
	time.Sleep(500 * time.Millisecond)
	fmt.Println(%q)
}
`, `{"type":"system","session_id":"`+sessionID+`"}`, `{"type":"assistant","message":{"content":[{"type":"text","text":"bye"}]}}`))
}

// TestExecutorCapturesSessionID checks that the first {"type":"system","session_id":...}
// frame in the stream-json transcript is stored on Result.SessionID.
func TestExecutorCapturesSessionID(t *testing.T) {
	bin := writeFakeClaude(t, []string{
		`{"type":"system","session_id":"sess-abc"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil)
	res, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	require.NoError(t, err)
	require.Equal(t, "sess-abc", res.SessionID)
	require.Nil(t, res.AwaitingUser)
}

// TestExecutorPausesOnHumanloopIPC simulates the humanloop subprocess sending
// an ask_user payload while the chat is running; the executor should close
// stdin, wait for the fake claude to exit, and return Result.AwaitingUser set.
func TestExecutorPausesOnHumanloopIPC(t *testing.T) {
	bin := writeFakeClaudeReadsStdinThenExits(t, "sess-pause")
	sockHook := func(arg string) {
		time.Sleep(200 * time.Millisecond)
		ep, err := humanloop.ParseEndpointArg(arg)
		if err != nil {
			t.Logf("ParseEndpointArg: %v", err)
			return
		}
		c, err := humanloop.DialIPC(ep)
		if err != nil {
			t.Logf("DialIPC: %v", err)
			return
		}
		defer c.Close()
		_ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "approve?"})
	}
	ex := newExecutorWithSocketHook(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil, sockHook)
	res, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	require.NoError(t, err)
	require.NotNil(t, res.AwaitingUser)
	require.Equal(t, "approve?", res.AwaitingUser.Question)
	require.Equal(t, "sess-pause", res.SessionID)
}

// TestExecutorFailsWhenPauseWithoutSessionID — if the model invokes ask_user
// but the backend never emitted a system frame with session_id, we cannot
// resume; treat as failure even though AwaitingUser is set. Spec §Boundaries A.
func TestExecutorFailsWhenPauseWithoutSessionID(t *testing.T) {
	script := buildFakeClaude(t, `package main
import (
	"fmt"
	"io"
	"os"
	"time"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	time.Sleep(500 * time.Millisecond)
	fmt.Println(`+"`"+`{"type":"assistant","message":{"content":[{"type":"text","text":"bye"}]}}`+"`"+`)
}
`)

	sockHook := func(arg string) {
		time.Sleep(200 * time.Millisecond)
		ep, err := humanloop.ParseEndpointArg(arg)
		if err != nil {
			t.Logf("ParseEndpointArg: %v", err)
			return
		}
		c, err := humanloop.DialIPC(ep)
		if err != nil {
			t.Logf("DialIPC: %v", err)
			return
		}
		defer c.Close()
		_ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "doomed"})
	}
	ex := newExecutorWithSocketHook(agentbackend.Config{Bin: script, WorkDir: t.TempDir()}, nil, sockHook)
	_, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	if err == nil {
		t.Fatal("expected error when AwaitingUser set but SessionID empty")
	}
	if !strings.Contains(err.Error(), "session_id") {
		t.Errorf("expected session_id in error, got %v", err)
	}
}

// TestExecutorFailsWhenGraceWindowExceeded — if claude doesn't exit after
// stdin close within the grace window, we SIGTERM/SIGKILL it and report the
// task as failed. Spec §Boundaries A row 3.
func TestExecutorFailsWhenGraceWindowExceeded(t *testing.T) {
	script := buildFakeClaude(t, `package main
import (
	"fmt"
	"os"
	"time"
)
func main() {
	fmt.Println(`+"`"+`{"type":"system","session_id":"sess-stuck"}`+"`"+`)
	_ = os.Stdout.Close()
	time.Sleep(30 * time.Second)
}
`)

	sockHook := func(arg string) {
		time.Sleep(50 * time.Millisecond)
		ep, err := humanloop.ParseEndpointArg(arg)
		if err != nil {
			t.Logf("ParseEndpointArg: %v", err)
			return
		}
		c, err := humanloop.DialIPC(ep)
		if err != nil {
			t.Logf("DialIPC: %v", err)
			return
		}
		defer c.Close()
		_ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "stuck"})
	}
	ex := newExecutorWithSocketHook(agentbackend.Config{Bin: script, WorkDir: t.TempDir()}, nil, sockHook)
	ex.shutdownGraceSec = 1 // shrink to 1s for fast test

	start := time.Now()
	_, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when grace window exceeded")
	}
	if !strings.Contains(err.Error(), "grace window") {
		t.Errorf("expected 'grace window' in error, got %v", err)
	}
	// Total should be ≈ 1s grace + ≤ 5s SIGTERM grace; the fake exits cleanly
	// on SIGTERM since `trap '' PIPE` only ignores PIPE, not TERM.
	if elapsed > 7*time.Second {
		t.Errorf("test took too long (%s); SIGTERM didn't end it", elapsed)
	}
}

// TestExecutorRunResumeFeedsAnswer — RunResume should:
// 1. invoke the binary with --resume <sessionID> somewhere in argv
// 2. write "User answered: <answer>" as the prompt to stdin
// 3. return a normal Result (no AwaitingUser if model didn't call ask_user)
func TestExecutorRunResumeFeedsAnswer(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "args.txt")
	script := buildFakeClaude(t, fmt.Sprintf(`package main
import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	_ = os.WriteFile(%q, []byte(strings.Join(os.Args[1:], " ")), 0600)
	fmt.Println(%q)
	input, _ := io.ReadAll(os.Stdin)
	text, _ := json.Marshal(string(input))
	fmt.Printf("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":%%s}]}}\n", text)
}
`, sentinel, `{"type":"system","session_id":"sess-1-resumed"}`))

	ex := newExecutor(agentbackend.Config{Bin: script, WorkDir: t.TempDir()}, nil)
	res, err := ex.RunResume(context.Background(), "sess-1", "the user's answer", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if !strings.Contains(string(args), "--resume sess-1") {
		t.Errorf("expected '--resume sess-1' in argv, got %q", string(args))
	}
	if !strings.Contains(res.Summary, "User answered: the user's answer") {
		t.Errorf("expected \"User answered: the user's answer\" in summary, got %q", res.Summary)
	}
	if res.AwaitingUser != nil {
		t.Errorf("AwaitingUser should be nil for a non-pausing resume")
	}
}

func buildFakeClaude(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "claude")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude: %v\n%s", err, out)
	}
	return exe
}
