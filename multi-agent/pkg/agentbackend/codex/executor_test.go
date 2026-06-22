package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	framesPath := filepath.Join(dir, "frames.ndjson")
	if err := os.WriteFile(framesPath, fix, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeBin := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	body, err := os.ReadFile(%q)
	if err != nil {
		panic(err)
	}
	fmt.Print(string(body))
	fmt.Println()
}
`, framesPath))
	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
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
	body := "package main\nimport \"fmt\"\nfunc main() {\n"
	for _, f := range frames {
		body += fmt.Sprintf("fmt.Println(%q)\n", f)
	}
	body += "}\n"
	return buildFakeCodex(t, body)
}

// writeFakeCodexReadsStdinThenExits emits a thread.started event, drains
// stdin to EOF (the prompt-writer closes stdin after writing), then sleeps
// briefly to simulate the model "thinking" — this is the window during which
// the humanloop MCP server (in real codex) would call its IPC tool. Once the
// sleep elapses the script emits a final agent_message and exits 0.
func writeFakeCodexReadsStdinThenExits(t *testing.T, threadID string) string {
	t.Helper()
	return buildFakeCodex(t, fmt.Sprintf(`package main
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
`, `{"type":"thread.started","thread_id":"`+threadID+`"}`, `{"type":"item.completed","item":{"type":"agent_message","text":"bye"}}`))
}

// TestCodexExecutorCapturesThreadID — first thread.started event's thread_id
// is stored on Result.SessionID.
func TestCodexExecutorCapturesThreadID(t *testing.T) {
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-abc"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil)
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

func TestCodexExecutorRunWritesLoomMetaSidecar(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-new","timestamp":"2026-06-17T10:00:00Z"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, []string{"CODEX_HOME=" + home})
	res, err := ex.Run(context.Background(), agentbackend.Task{
		Prompt:            "hi",
		ParentSessionID:   "parent-thread",
		ParentAgentID:     "drv-1",
		ParentDisplayName: "prod-driver",
	}, &captureSink{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionID != "thr-new" {
		t.Fatalf("SessionID = %q, want thr-new", res.SessionID)
	}
	m, ok := readLoomMeta(home, "thr-new")
	if !ok {
		t.Fatal("sidecar not written on Run")
	}
	if m.ParentSessionID != "parent-thread" || m.ParentAgentID != "drv-1" || m.ParentDisplayName != "prod-driver" {
		t.Fatalf("sidecar parent mismatch: %+v", m)
	}
	if m.CreatedAt != "2026-06-17T10:00:00Z" {
		t.Fatalf("CreatedAt = %q, want event timestamp", m.CreatedAt)
	}
}

func TestCodexExecutorRunResumeDoesNotWriteSidecar(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-resume","timestamp":"2026-06-17T10:00:00Z"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, []string{"CODEX_HOME=" + home})
	if _, err := ex.RunResume(context.Background(), "thr-resume", "continue", &captureSink{}); err != nil {
		t.Fatalf("RunResume: %v", err)
	}
	if _, ok := readLoomMeta(home, "thr-resume"); ok {
		t.Fatal("RunResume must not write a sidecar")
	}
}

func TestCodexExecutorSidecarCreatedAtFallback(t *testing.T) {
	home := t.TempDir()
	bin := writeFakeCodex(t, []string{
		`{"type":"thread.started","thread_id":"thr-nots"}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	})
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, []string{"CODEX_HOME=" + home})
	if _, err := ex.Run(context.Background(), agentbackend.Task{
		Prompt:          "hi",
		ParentSessionID: "parent-thread",
		ParentAgentID:   "drv",
	}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m, ok := readLoomMeta(home, "thr-nots")
	if !ok {
		t.Fatal("sidecar not written")
	}
	if m.CreatedAt == "" {
		t.Fatal("CreatedAt empty")
	}
	if _, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err != nil {
		t.Fatalf("CreatedAt %q is not RFC3339Nano: %v", m.CreatedAt, err)
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
	ex := newExecutorWithSocketHook(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil, sockHook)
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
	script := buildFakeCodex(t, `package main
import (
	"os"
	"time"
)
func main() {
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
		_ = c.Send(humanloop.Payload{Kind: "ask_user", Question: "doomed"})
	}
	ex := newExecutorWithSocketHook(agentbackend.Config{Bin: script, WorkDir: t.TempDir()}, nil, sockHook)
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
	script := buildFakeCodex(t, `package main
import (
	"fmt"
	"os"
	"time"
)
func main() {
	fmt.Println(`+"`"+`{"type":"thread.started","thread_id":"thr-stuck"}`+"`"+`)
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
	sentinel := filepath.Join(dir, "args.txt")
	script := buildFakeCodex(t, fmt.Sprintf(`package main
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
	fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":%%s}}\n", text)
}
`, sentinel, `{"type":"thread.started","thread_id":"thr-1-resumed"}`))

	ex := newExecutor(agentbackend.Config{Bin: script, WorkDir: t.TempDir()}, nil)
	res, err := ex.RunResume(context.Background(), "thr-1", "the user's answer", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}

	args, _ := os.ReadFile(sentinel)
	if !strings.Contains(string(args), "exec resume thr-1") {
		t.Errorf("expected 'exec resume thr-1' in argv, got %q", string(args))
	}
	if !strings.Contains(string(args), "--skip-git-repo-check") {
		t.Errorf("resume argv missing --skip-git-repo-check: %q", string(args))
	}
	if !strings.Contains(string(args), "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("resume argv missing noninteractive sandbox bypass: %q", string(args))
	}
	if !strings.Contains(res.Summary, "User answered: the user's answer") {
		t.Errorf("expected 'User answered: …' in summary, got %q", res.Summary)
	}
}

func TestCodexExecutorRunResumePreservesExtraArgsEnvAndHumanloopMCP(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.txt")
	envPath := filepath.Join(dir, "env.txt")
	script := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	_ = os.WriteFile(%q, []byte(strings.Join(os.Args[1:], "\n")), 0600)
	_ = os.WriteFile(%q, []byte(os.Getenv("CODEX_HOME")+"|"+os.Getenv("LOOM_TEST_ENV")), 0600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(`+"`"+`{"type":"thread.started","thread_id":"thr-resumed"}`+"`"+`)
	fmt.Println(`+"`"+`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`+"`"+`)
}
`, argsPath, envPath))

	codexHome := t.TempDir()
	ex := newExecutor(agentbackend.Config{
		Bin:       script,
		WorkDir:   t.TempDir(),
		ExtraArgs: []string{"--profile", "loom-test"},
	}, []string{"CODEX_HOME=" + codexHome, "LOOM_TEST_ENV=present"})
	_, err := ex.RunResume(context.Background(), "thr-1", "continue", &captureSink{})
	if err != nil {
		t.Fatal(err)
	}

	argsRaw, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	argsList := strings.Split(string(argsRaw), "\n")
	if len(argsList) < 3 || argsList[0] != "exec" || argsList[1] != "resume" || argsList[2] != "thr-1" {
		t.Fatalf("argv head=%v want [exec resume thr-1]", argsList)
	}
	args := strings.Join(argsList, "\n")
	for _, want := range []string{
		"--profile",
		"loom-test",
		"mcp_servers.loom_humanloop.command",
		"humanloop-mcp",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("argv missing %q:\n%s", want, args)
		}
	}

	envRaw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(envRaw), codexHome+"|present"; got != want {
		t.Fatalf("env=%q want %q", got, want)
	}
}

func TestCodexExecutorSubprocessEnvHasSingleCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "/stale-from-process")
	t.Setenv("Codex_Home", "/stale-case-variant")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	bin := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	var lines []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && strings.EqualFold(k, "CODEX_HOME") {
			lines = append(lines, e)
		}
	}
	_ = os.WriteFile(%q, []byte(strings.Join(lines, "\n")), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(%q)
}
`, envPath, `{"type":"thread.started","thread_id":"thr-env","timestamp":"2026-06-17T10:00:00Z"}`))
	b := New(agentbackend.Config{Bin: bin, WorkDir: t.TempDir(), CodexHome: home}, nil)
	if _, err := b.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(got), "\n")
	count := 0
	for _, line := range lines {
		if line == "Codex_Home=/stale-case-variant" {
			t.Fatalf("case-variant stale CODEX_HOME survived: %q", got)
		}
		if line == "CODEX_HOME="+home {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("subprocess env CODEX_HOME lines = %q, want exactly one CODEX_HOME=%s", got, home)
	}
}

func TestCodexExecutorSubprocessEnvDefaultOverridesStale(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv("CODEX_HOME", "/stale-from-process")
	t.Setenv("Codex_Home", "/stale-case-variant")
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env.txt")
	bin := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
	"strings"
)
func main() {
	var lines []string
	for _, e := range os.Environ() {
		k, _, ok := strings.Cut(e, "=")
		if ok && strings.EqualFold(k, "CODEX_HOME") {
			lines = append(lines, e)
		}
	}
	_ = os.WriteFile(%q, []byte(strings.Join(lines, "\n")), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(%q)
}
`, envPath, `{"type":"thread.started","thread_id":"thr-def","timestamp":"2026-06-17T10:00:00Z"}`))
	b := New(agentbackend.Config{Bin: bin, WorkDir: t.TempDir()}, nil)
	if _, err := b.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "CODEX_HOME=" + filepath.Join(home, ".codex")
	count := 0
	for _, line := range strings.Split(string(got), "\n") {
		switch line {
		case want:
			count++
		case "CODEX_HOME=/stale-from-process", "Codex_Home=/stale-case-variant":
			t.Fatalf("stale CODEX_HOME survived: %q", got)
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one %q, got %q", want, got)
	}
}

func TestCodexExecutorSubprocessPWDMatchesWorkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PWD is Unix-specific")
	}
	workDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "pwd.txt")
	t.Setenv("PWD", "/stale-parent-pwd")
	bin := buildFakeCodex(t, fmt.Sprintf(`package main
import (
	"fmt"
	"io"
	"os"
)
func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	_ = os.WriteFile(%q, []byte(os.Getenv("PWD")+"\n"+cwd), 0o600)
	_, _ = io.Copy(io.Discard, os.Stdin)
	fmt.Println(%q)
	fmt.Println(%q)
}
`, outPath, `{"type":"thread.started","thread_id":"thr-pwd","timestamp":"2026-06-17T10:00:00Z"}`, `{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`))
	ex := newExecutor(agentbackend.Config{Bin: bin, WorkDir: workDir}, nil)
	if _, err := ex.Run(context.Background(), agentbackend.Task{Prompt: "hi"}, &captureSink{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) != 2 {
		t.Fatalf("pwd output = %q, want two lines", raw)
	}
	if lines[1] != workDir {
		t.Fatalf("cwd=%q want %q", lines[1], workDir)
	}
	if lines[0] != lines[1] {
		t.Fatalf("PWD=%q cwd=%q, want PWD to match command working directory", lines[0], lines[1])
	}
}

func buildFakeCodex(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "codex")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake codex: %v\n%s", err, out)
	}
	return exe
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
