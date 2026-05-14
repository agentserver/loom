package scriptstest

import (
	"os"
	"strings"
	"testing"
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
		"ANTHROPIC_BASE_URL: https://code.ai.cs.ac.cn",
		"ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}",
		"./:/workspace/multi-agent",
		"./dev/configs/master.yaml:/config/config.yaml",
		"./dev/configs/driver.yaml:/config/config.yaml",
		"./dev/configs/slave-a.yaml:/config/config.yaml",
		"./dev/configs/slave-b.yaml:/config/config.yaml",
		"restart: unless-stopped",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("compose missing %q", want)
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
