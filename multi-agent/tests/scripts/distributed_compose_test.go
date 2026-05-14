package scriptstest

import (
	"os"
	"strings"
	"testing"

	agentconfig "github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/driver"
)

func TestDistributedComposeScaffold(t *testing.T) {
	data, err := os.ReadFile("../../dev/compose.distributed.yaml")
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"master:",
		"driver:",
		"slave-a:",
		"slave-b:",
		"observer:",
		"context: ..",
		"ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn",
		"ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}",
		"../:/workspace/multi-agent",
		"./configs/master.yaml:/config/config.yaml",
		"./configs/driver.yaml:/config/config.yaml",
		"./configs/slave-a.yaml:/config/config.yaml",
		"./configs/slave-b.yaml:/config/config.yaml",
		"./configs/observer.yaml:/config/config.yaml",
		"go run ./cmd/driver-agent serve-mcp --config /config/config.yaml",
		"go run ./cmd/observer-server /config/config.yaml",
		"restart: unless-stopped",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compose missing %q", want)
		}
	}
}

func TestDistributedComposeExampleConfigsLoad(t *testing.T) {
	for _, path := range []string{
		"../../dev/configs/master.example.yaml",
		"../../dev/configs/slave-a.example.yaml",
		"../../dev/configs/slave-b.example.yaml",
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
		"workspaces:",
		"id: dev",
		"role: driver",
		"role: master",
		"role: slave",
		"token: driver-token",
		"token: master-token",
		"token: slave-a-token",
		"token: slave-b-token",
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
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Dockerfile missing %q", want)
		}
	}
}
