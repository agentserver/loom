package scriptstest

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runNamedScript(t *testing.T, script string, args ...string) string {
	t.Helper()
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", script, args, err, out)
	}
	return string(out)
}

func runScript(t *testing.T, args ...string) string {
	t.Helper()
	return runNamedScript(t, "scripts/agents.sh", args...)
}

func TestAgentsScriptDryRunStartBuildsRegistersAndStartsAgents(t *testing.T) {
	out := runScript(t, "--dry-run", "start")

	for _, want := range []string{
		"go build -o bin/master-agent ./cmd/master-agent",
		"go build -o bin/slave-agent ./cmd/slave-agent",
		"go build -o bin/driver-agent ./cmd/driver-agent",
		"bin/driver-agent register --config cmd/driver-agent/config.yaml",
		"(cd cmd/master-agent && ../../bin/master-agent config.yaml)",
		"(cd cmd/slave-agent && ../../bin/slave-agent config.yaml)",
		"bin/driver-agent serve-mcp --config cmd/driver-agent/config.yaml",
		".run/agents/master-agent.pid",
		".run/agents/slave-agent.pid",
		".run/agents/driver-agent.pid",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run start missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestAgentsScriptDryRunStopUsesPidFilesAndScopedFallback(t *testing.T) {
	out := runScript(t, "--dry-run", "stop")

	for _, want := range []string{
		".run/agents/master-agent.pid",
		".run/agents/slave-agent.pid",
		".run/agents/driver-agent.pid",
		"pkill -f",
		"multi-agent/.claude/worktrees/http-task-observer/multi-agent",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run stop missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestAgentsScriptDryRunStatusMentionsLogsAndPids(t *testing.T) {
	out := runScript(t, "--dry-run", "status")

	for _, want := range []string{
		"master-agent",
		"slave-agent",
		"driver-agent",
		".run/agents/master-agent.log",
		".run/agents/slave-agent.log",
		".run/agents/driver-agent.log",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run status missing %q\noutput:\n%s", want, out)
		}
	}
}
