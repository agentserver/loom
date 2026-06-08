package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/yourorg/multi-agent/internal/humanloop"
	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
	chunks []string
	closed bool
}

func (c *captureSink) Write(_, text string) { c.chunks = append(c.chunks, text) }
func (c *captureSink) Close()               { c.closed = true }

func TestExecutorReplaysFixture(t *testing.T) {
	fix, err := os.ReadFile("testdata/codex_exec.ndjson")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "codex")
	script := "#!/usr/bin/env bash\ncat >/dev/null\ncat <<'EOF'\n" + string(fix) + "\nEOF\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	b := New(agentbackend.CodexConfig{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	if res.Summary == "" {
		t.Fatal("empty summary")
	}
	if !strings.Contains(res.Summary, "pong") {
		t.Fatalf("summary %q does not contain pong", res.Summary)
	}
}

// writeFakeCodex builds a one-shot fake codex binary that emits the given
// stream-json frames (one per line) and exits 0.
func writeFakeCodex(t *testing.T, frames []string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := "#!/bin/bash\n"
	for _, f := range frames {
		body += "echo '" + f + "'\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// writeFakeCodexReadsStdinThenExits emits a thread.started event, drains
// stdin to EOF (the prompt-writer closes stdin after writing), then sleeps
// briefly to simulate the model "thinking" — this is the window during which
// the humanloop MCP server (in real codex) would call its IPC tool. Once the
// sleep elapses the script emits a final agent_message and exits 0.
func writeFakeCodexReadsStdinThenExits(t *testing.T, threadID string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := fmt.Sprintf(`#!/bin/bash
echo '{"type":"thread.started","thread_id":"%s"}'
cat > /dev/null
# Simulate model processing time — gives the test's IPC hook (50ms delay)
# time to land before we exit and tear down the listener.
sleep 0.5
echo '{"type":"item.completed","item":{"type":"agent_message","text":"bye"}}'
`, threadID)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

// TestCodexExecutorCapturesThreadID — first thread.started event's thread_id
// is stored on Result.SessionID.
func TestCodexExecutorCapturesThreadID(t *testing.T) {
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-abc"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
	})
	ex := newExecutor(agentbackend.CodexConfig{Bin: bin, WorkDir: t.TempDir()}, nil)
	res, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "thr-abc" {
		t.Errorf("SessionID = %q, want thr-abc", res.SessionID)
	}
	if res.AwaitingUser != nil {
		t.Errorf("AwaitingUser should be nil")
	}
}

func TestCodexExecutorPausesOnHumanloopIPC(t *testing.T) {
	bin := writeFakeCodexReadsStdinThenExits(t, "thr-pause")
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
		_ = c.Send(humanloop.Payload{Kind: "request_permission", Intent: "run_bash", Target: "rm -rf /tmp/x"})
	}
	ex := newExecutorWithSocketHook(agentbackend.CodexConfig{Bin: bin, WorkDir: t.TempDir()}, nil, sockHook)
	res, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	if err != nil {
		t.Fatal(err)
	}
	if res.AwaitingUser == nil {
		t.Fatal("AwaitingUser nil")
	}
	if res.AwaitingUser.Kind != "request_permission" || res.AwaitingUser.Target != "rm -rf /tmp/x" {
		t.Errorf("unexpected AwaitingUser: %+v", res.AwaitingUser)
	}
	if res.SessionID != "thr-pause" {
		t.Errorf("SessionID = %q", res.SessionID)
	}
}

func TestCodexExecutorFailsWhenPauseWithoutSessionID(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := `#!/bin/bash
# No thread.started event.
exec sleep 30 < /dev/null > /dev/null 2>&1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

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
		_ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "doomed"})
	}
	ex := newExecutorWithSocketHook(agentbackend.CodexConfig{Bin: script, WorkDir: t.TempDir()}, nil, sockHook)
	ex.shutdownGraceSec = 1
	_, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	if err == nil {
		t.Fatal("expected error when AwaitingUser without thread_id")
	}
	if !strings.Contains(err.Error(), "session_id") && !strings.Contains(err.Error(), "thread_id") {
		t.Errorf("expected session_id/thread_id in error, got %v", err)
	}
}

func TestCodexExecutorFailsWhenGraceWindowExceeded(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	body := `#!/bin/bash
echo '{"type":"thread.started","thread_id":"thr-stuck"}'
trap '' PIPE
exec sleep 30 < /dev/null > /dev/null 2>&1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

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
	ex := newExecutorWithSocketHook(agentbackend.CodexConfig{Bin: script, WorkDir: t.TempDir()}, nil, sockHook)
	ex.shutdownGraceSec = 1
	start := time.Now()
	_, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error when grace window exceeded")
	}
	if !strings.Contains(err.Error(), "grace window") {
		t.Errorf("expected 'grace window' in error, got %v", err)
	}
	if elapsed > 7*time.Second {
		t.Errorf("test took too long (%s)", elapsed)
	}
}

// TestCodexExecutorRunResumeFeedsAnswer — RunResume invokes `codex exec resume
// <sessionID>` and feeds "User answered: <answer>" as stdin (codex reads
// prompt from stdin when the trailing arg is `-`).
func TestCodexExecutorRunResumeFeedsAnswer(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	sentinel := filepath.Join(dir, "args.txt")
	body := fmt.Sprintf(`#!/bin/bash
echo "$@" > %q
echo '{"type":"thread.started","thread_id":"thr-1-resumed"}'
sleep 0.2
INPUT=$(dd bs=4096 count=1 iflag=nonblock 2>/dev/null || true)
ESCAPED=$(printf '%%s' "$INPUT" | sed 's/"/\\"/g')
echo "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"$ESCAPED\"}}"
`, sentinel)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	ex := newExecutor(agentbackend.CodexConfig{Bin: script, WorkDir: t.TempDir()}, nil)
	res, err := ex.RunResume(context.Background(), "thr-1", "the user's answer", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}

	args, _ := os.ReadFile(sentinel)
	if !strings.Contains(string(args), "exec resume thr-1") {
		t.Errorf("expected 'exec resume thr-1' in argv, got %q", string(args))
	}
	if !strings.Contains(res.Summary, "User answered: the user's answer") {
		t.Errorf("expected 'User answered: …' in summary, got %q", res.Summary)
	}
}

func TestHumanloopMCPArgsAreTOMLSafe(t *testing.T) {
	binSelf := `C:\Program Files\Loom "Agent"\slave-agent.exe`
	ep := humanloop.Endpoint{Network: "unix", Address: `C:\Users\Loom "Agent"\hl.sock`}
	args := humanloopMCPArgs(binSelf, ep, 7)
	if len(args) != 4 || args[0] != "-c" || args[2] != "-c" {
		t.Fatalf("unexpected mcp args shape: %#v", args)
	}

	var cfg struct {
		MCPServers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(args[1]+"\n"+args[3], &cfg); err != nil {
		t.Fatalf("decode TOML overrides: %v\n%s\n%s", err, args[1], args[3])
	}
	got := cfg.MCPServers["loom_humanloop"]
	if got.Command != binSelf {
		t.Fatalf("command = %q, want %q", got.Command, binSelf)
	}
	wantArgs := []string{"humanloop-mcp", humanloop.EndpointArg(ep), "7"}
	if len(got.Args) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", got.Args, wantArgs)
	}
	for i := range wantArgs {
		if got.Args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, got.Args[i], wantArgs[i])
		}
	}
	parsed, err := humanloop.ParseEndpointArg(got.Args[1])
	if err != nil {
		t.Fatalf("ParseEndpointArg(%q): %v", got.Args[1], err)
	}
	if parsed != ep {
		t.Fatalf("endpoint = %+v, want %+v", parsed, ep)
	}
}
