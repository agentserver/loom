package scriptstest

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	agentconfig "github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/driver"
	// Register backend kinds so driver.LoadConfig's whitelist
	// recognises "claude"/"codex" when validating the example yaml.
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/claude"
	_ "github.com/yourorg/multi-agent/pkg/agentbackend/codex"
)

func TestDistributedComposeScaffold(t *testing.T) {
	data, err := os.ReadFile("../../dev/compose.distributed.yaml")
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"context: ..",
		"ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn",
		"ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}",
		"POSTGRES_DB: agentserver",
		"go run github.com/agentserver/agentserver@v0.48.1 serve",
		"../:/workspace/multi-agent",
		"./configs/master.yaml:/config/config.yaml",
		"./configs/driver.yaml:/config/config.yaml",
		"./configs/slave-a.yaml:/config/config.yaml",
		"./configs/slave-b.yaml:/config/config.yaml",
		"./configs/slave-cloud.yaml:/config/config.yaml",
		"./configs/observer.yaml:/config/config.yaml",
		"go run ./cmd/driver-agent serve-mcp --config /config/config.yaml",
		"go run ./cmd/observer-server --config /config/config.yaml",
		"restart: unless-stopped",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compose missing %q", want)
		}
	}

	// Service-name presence is asserted structurally so a future
	// refactor that deletes a service block but leaves a same-named
	// comment or mount path cannot pass the test (the previous bare
	// substring check would have).
	var parsed struct {
		Services map[string]yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	for _, svc := range []string{
		"postgres",
		"agentserver",
		"master",
		"driver",
		"slave-a",
		"slave-b",
		"slave-cloud",
		"observer",
	} {
		if _, ok := parsed.Services[svc]; !ok {
			t.Fatalf("compose services missing %q", svc)
		}
	}
}

func TestDistributedComposeExampleConfigsLoad(t *testing.T) {
	for _, path := range []string{
		"../../dev/configs/master.example.yaml",
		"../../dev/configs/slave-a.example.yaml",
		"../../dev/configs/slave-b.example.yaml",
		"../../dev/configs/slave-cloud.example.yaml",
	} {
		if _, err := agentconfig.Load(path); err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
	}

	if _, err := driver.LoadConfig("../../dev/configs/driver.example.yaml"); err != nil {
		t.Fatalf("load driver example: %v", err)
	}

	data, err := os.ReadFile("../../dev/configs/observer.example.yaml")
	if err != nil {
		t.Fatalf("read observer example: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"listen_addr:",
		"db_path:",
		"api_keys:",
		"id: ak-dev",
		"key: ak_dev_shared_secret",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("observer example missing %q", want)
		}
	}
}

func TestAgentRuntimeInstallsClaudeCodeAndSkipsOnboarding(t *testing.T) {
	data, err := os.ReadFile("../../dev/agent-runtime/Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"npm install -g @anthropic-ai/claude-code",
		"ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn",
		`"hasCompletedOnboarding":true`,
		"/root/.zshrc",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}

func TestGitignoreProtectsRuntimeSecrets(t *testing.T) {
	data, err := os.ReadFile("../../.gitignore")
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"/dev/.env",
		"/dev/*.env",
		"/dev/agent-runtime/.zshrc",
		"/tests/runtime/**/*.env",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf(".gitignore missing %q", want)
		}
	}
}

func TestRuntimeReadmeDocumentsOnlineReuseContract(t *testing.T) {
	data, err := os.ReadFile("../../tests/runtime/README.md")
	if err != nil {
		t.Fatalf("read runtime README: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"https://agent.cs.ac.cn",
		"/tmp/multi-agent-driver-first-e2e/driver/",
		"/tmp/multi-agent-driver-first-e2e/master/",
		"/tmp/multi-agent-driver-first-e2e/slave-a/",
		"/tmp/multi-agent-driver-first-e2e/slave-b/",
		"ma-e2e-driver",
		"ma-e2e-master",
		"ma-e2e-slave-a",
		"ma-e2e-slave-b",
		"test runner must print the login URL",
		"reuse the same authorization",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("runtime README missing %q", want)
		}
	}
}
