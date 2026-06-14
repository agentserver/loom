package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

type captureSink struct {
	chunks []string
	closed bool
}

func (c *captureSink) Write(_, text string) { c.chunks = append(c.chunks, text) }
func (c *captureSink) Close()               { c.closed = true }

// TestExecutor_ReplaysCapturedFixture exercises Run() against the
// opencode event stream captured in pre-flight (Step 4.0). A fake bin
// echoes the fixture; the executor parses it and emits at least one
// sink chunk + a non-empty Result.Summary. The fixture contains the
// finalised `text` event whose `part.text` is the assistant message,
// per the opencode source (see testdata/opencode_run.ndjson header).
func TestExecutor_ReplaysCapturedFixture(t *testing.T) {
	fix, err := os.ReadFile("testdata/opencode_run.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	framesPath := filepath.Join(dir, "frames.ndjson")
	if err := os.WriteFile(framesPath, fix, 0o600); err != nil {
		t.Fatal(err)
	}

	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
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
}
`, framesPath), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	res, err := b.Run(context.Background(), agentbackend.Task{Prompt: "ignored — fake bin replays fixture"}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.closed {
		t.Fatal("sink not closed")
	}
	if res.Summary == "" {
		t.Fatalf("empty summary; sink chunks=%v", sink.chunks)
	}
	if !strings.Contains(res.Summary, "Hi") {
		t.Fatalf("summary missing expected text from fixture: %q", res.Summary)
	}
	if res.SessionID == "" {
		t.Fatalf("session id not captured from event sessionID field")
	}
	if len(sink.chunks) == 0 {
		t.Fatalf("no sink chunks emitted")
	}
}

// TestExecutor_InjectsHumanloopMCPViaTempConfig pins how opencode gets
// the humanloop MCP server: a temp opencode.json is written, the file
// lists loom_humanloop as a local MCP server with our binSelf as the
// command, and OPENCODE_CONFIG env is set to the file path. Different
// from claude's --mcp-config flag and codex's `-c mcp_servers.X`
// inline overrides — opencode uses a config-file-only mechanism.
func TestExecutor_InjectsHumanloopMCPViaTempConfig(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "captured-mcp-config.json")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	cfgPath := os.Getenv("OPENCODE_CONFIG")
	if cfgPath == "" {
		os.Exit(2)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(%q, data, 0o600); err != nil {
		os.Exit(4)
	}
}
`, sentinel), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	_, _ = b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("opencode bin did not capture OPENCODE_CONFIG contents: %v", err)
	}
	var cfg struct {
		Schema string `json:"$schema"`
		MCP    map[string]struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			Enabled bool     `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("captured config not JSON: %v\n%s", err, data)
	}
	if cfg.Schema == "" {
		t.Errorf("$schema missing from captured config: %s", data)
	}
	hl, ok := cfg.MCP["loom_humanloop"]
	if !ok {
		t.Fatalf("loom_humanloop not in captured MCP map: %v", cfg.MCP)
	}
	if hl.Type != "local" {
		t.Errorf("loom_humanloop.type=%q want local", hl.Type)
	}
	if !hl.Enabled {
		t.Errorf("loom_humanloop.enabled=false want true")
	}
	if len(hl.Command) < 3 {
		t.Fatalf("loom_humanloop.command too short (want >=3 [binSelf, humanloop-mcp, endpoint, max]): %v", hl.Command)
	}
	if hl.Command[0] == "" {
		t.Errorf("command[0] empty (want binSelf path)")
	}
	if hl.Command[1] != "humanloop-mcp" {
		t.Errorf("command[1]=%q want humanloop-mcp", hl.Command[1])
	}
}

// TestExecutor_RunResume_UsesSessionFlag pins the resume protocol:
// `opencode run --session <id> --continue` with the new answer as the
// prompt (rendered as "User answered: <answer>" for clarity). Mirrors
// codex/executor_test.go for `exec resume`.
func TestExecutor_RunResume_UsesSessionFlag(t *testing.T) {
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	stdinPath := filepath.Join(dir, "stdin.txt")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
	"strings"
)
func main() {
	argv := strings.Join(os.Args[1:], "|")
	_ = os.WriteFile(%q, []byte(argv), 0o600)
	body, _ := io.ReadAll(os.Stdin)
	_ = os.WriteFile(%q, body, 0o600)
}
`, argvPath, stdinPath), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	_, _ = b.RunResume(context.Background(), "sess-abc", "yes please proceed", sink)

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--session|sess-abc") {
		t.Errorf("argv missing --session sess-abc: %s", argv)
	}
	if !strings.Contains(string(argv), "--continue") {
		t.Errorf("argv missing --continue: %s", argv)
	}

	stdinBody, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdinBody), "User answered: yes please proceed") {
		t.Errorf("stdin missing user-answered prompt: %s", stdinBody)
	}
}

// TestExecutor_PreservesUserOpencodeConfig pins the opencode-specific
// merge-on-inject contract: opencode's only MCP injection mechanism is
// OPENCODE_CONFIG, which is full-file override. If our humanloop temp
// file replaces user provider config the slave's spawned opencode can't
// find any model. Fix: writeOpencodeHumanloopConfig must read the
// user's existing config (OPENCODE_CONFIG env if set, else XDG
// default) and merge loom_humanloop into the existing mcp map.
func TestExecutor_PreservesUserOpencodeConfig(t *testing.T) {
	dir := t.TempDir()

	// User's existing opencode.json with a provider block.
	userCfgPath := filepath.Join(dir, "user-opencode.json")
	userCfg := []byte(`{
		"$schema": "https://opencode.ai/config.json",
		"model": "modelserver/gpt-5.5",
		"provider": {
			"modelserver": {
				"npm": "@ai-sdk/openai",
				"name": "modelserver",
				"options": {
					"baseURL": "https://code.ai.cs.ac.cn/v1",
					"apiKey": "ms-test-key"
				},
				"models": {"gpt-5.5": {"name": "gpt-5.5"}}
			}
		},
		"mcp": {
			"existing_user_server": {
				"type": "local",
				"command": ["/some/other/bin"],
				"enabled": true
			}
		}
	}`)
	if err := os.WriteFile(userCfgPath, userCfg, 0o600); err != nil {
		t.Fatal(err)
	}

	// Fake opencode that captures its OPENCODE_CONFIG file into sentinel.
	sentinel := filepath.Join(dir, "captured-cfg.json")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	cfgPath := os.Getenv("OPENCODE_CONFIG")
	data, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(%q, data, 0o600)
}
`, sentinel), "opencode")

	// Point our slave at the user's opencode.json via OPENCODE_CONFIG in
	// the env passed to newExecutor (mirrors how an operator running
	// slave with `OPENCODE_CONFIG=/.../opencode.json slave-agent ...`
	// would set it).
	b := New(
		agentbackend.Config{Bin: fakeBin, WorkDir: dir},
		[]string{"OPENCODE_CONFIG=" + userCfgPath},
	)
	sink := &captureSink{}
	_, _ = b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("fake opencode never wrote sentinel: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("merged config not JSON: %v\n%s", err, data)
	}

	// Preserved: user provider config
	prov, _ := merged["provider"].(map[string]any)
	if _, ok := prov["modelserver"]; !ok {
		t.Errorf("provider.modelserver lost in merge; merged=%v", merged)
	}
	if model, _ := merged["model"].(string); model != "modelserver/gpt-5.5" {
		t.Errorf("model lost in merge; got %q", model)
	}

	// Preserved: user existing mcp server
	mcp, _ := merged["mcp"].(map[string]any)
	if _, ok := mcp["existing_user_server"]; !ok {
		t.Errorf("existing user mcp server lost in merge; mcp=%v", mcp)
	}

	// Injected: loom_humanloop server
	hl, ok := mcp["loom_humanloop"].(map[string]any)
	if !ok {
		t.Errorf("loom_humanloop not injected into merged mcp; mcp=%v", mcp)
	} else if typ, _ := hl["type"].(string); typ != "local" {
		t.Errorf("loom_humanloop.type=%q want local", typ)
	}
}

// TestExecutor_EmptyUserConfigStillWritesHumanloop pins that an
// operator with no opencode.json at all still gets humanloop —
// the merge function tolerates missing files.
func TestExecutor_EmptyUserConfigStillWritesHumanloop(t *testing.T) {
	dir := t.TempDir()
	// No OPENCODE_CONFIG env; no XDG default file exists either
	// (HOME redirected to ensure none is read).
	t.Setenv("HOME", dir)                         // ensure XDG default $HOME/.config/opencode/opencode.json doesn't exist
	t.Setenv("XDG_CONFIG_HOME", dir+"/no-such")   // redirect XDG too
	t.Setenv("OPENCODE_CONFIG", "")               // make sure no stray parent env leaks in

	sentinel := filepath.Join(dir, "captured-cfg.json")
	fakeBin := goBuildFake(t, fmt.Sprintf(`package main
import (
	"io"
	"os"
)
func main() {
	_, _ = io.Copy(io.Discard, os.Stdin)
	cfgPath := os.Getenv("OPENCODE_CONFIG")
	data, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(%q, data, 0o600)
}
`, sentinel), "opencode")

	b := New(agentbackend.Config{Bin: fakeBin, WorkDir: dir}, nil)
	sink := &captureSink{}
	_, _ = b.Run(context.Background(), agentbackend.Task{Prompt: "ignored"}, sink)

	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel missing: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	mcp, _ := merged["mcp"].(map[string]any)
	if _, ok := mcp["loom_humanloop"]; !ok {
		t.Errorf("loom_humanloop missing in empty-user-config case; merged=%v", merged)
	}
}
