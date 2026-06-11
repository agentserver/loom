package scriptstest

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPackageDriverSkillsIncludesEverySkillFromGitTree(t *testing.T) {
	fixture := t.TempDir()
	requireRun(t, fixture, "git", "init", "-q")
	requireRun(t, fixture, "git", "config", "user.email", "test@example.com")
	requireRun(t, fixture, "git", "config", "user.name", "Test User")

	for _, skill := range []string{"alpha", "beta-tool", "future-extra"} {
		if err := os.MkdirAll(filepath.Join(fixture, "skills", skill), 0755); err != nil {
			t.Fatal(err)
		}
		requireWriteFile(t, filepath.Join(fixture, "skills", skill, "SKILL.md"), []byte("---\nname: "+skill+"\n---\n"))
	}
	requireRun(t, fixture, "git", "add", "skills")
	requireRun(t, fixture, "git", "commit", "-q", "-m", "add skills")

	out := filepath.Join(t.TempDir(), "driver-skills.tar.gz")
	script := filepath.Join(repoRoot(t), "scripts", "package-driver-skills.sh")
	requireRun(t, repoRoot(t), "bash", script, "--repo-root", fixture, "--tag", "HEAD", "--out", out)

	got := skillDirsInTar(t, out)
	want := []string{"alpha", "beta-tool", "future-extra"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("skill dirs in archive = %v, want %v", got, want)
	}

	for _, entry := range tarEntries(t, out) {
		if strings.HasPrefix(entry, "skills/") {
			t.Fatalf("archive entry %q has extra skills/ prefix", entry)
		}
	}
}

func TestReleaseWorkflowUsesDriverSkillsPackagingScript(t *testing.T) {
	workflow := filepath.Join(repoRoot(t), "..", ".github", "workflows", "release.yml")
	data, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"scripts/package-driver-skills.sh",
		"driver-skills.tar.gz",
		"sha256sum",
		"gh release upload",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q\n%s", want, text)
		}
	}
}

func TestMultiAgentCIRunsForReleaseWorkflowChanges(t *testing.T) {
	workflow := filepath.Join(repoRoot(t), "..", ".github", "workflows", "multi-agent.yml")
	data, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read multi-agent workflow: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, ".github/workflows/release.yml") {
		t.Fatalf("multi-agent CI paths must include release.yml so release workflow tests run on workflow-only changes\n%s", text)
	}
}

func TestDriverCodexPromptAssetUsesLightweightRouter(t *testing.T) {
	out := filepath.Join(t.TempDir(), "driver-codex-prompts.tar.gz")
	promptDir := filepath.Join(repoRoot(t), "deploy", "linux", "driver", "prompts-codex")
	requireRun(t, repoRoot(t), "tar", "-C", promptDir, "-czf", out, ".")

	agents := string(tarFileContent(t, out, "AGENTS.md"))
	for _, want := range []string{
		"# Agentserver Driver Workspace",
		"Use the `multiagent` skill",
		"mcp_servers.driver",
		"Discover agents and resources before acting",
		"Superpower skills",
	} {
		if !strings.Contains(agents, want) {
			t.Fatalf("AGENTS.md missing router prompt fragment %q\n%s", want, agents)
		}
	}

	for _, forbidden := range []string{
		"mcp__driver__",
		"## Core tools",
		"## Permissions skill",
	} {
	if strings.Contains(agents, forbidden) {
			t.Fatalf("AGENTS.md still contains verbose tool documentation %q\n%s", forbidden, agents)
		}
	}
}

func requireRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}

func skillDirsInTar(t *testing.T, path string) []string {
	t.Helper()
	var dirs []string
	for _, entry := range tarEntries(t, path) {
		if strings.Count(entry, "/") == 1 && strings.HasSuffix(entry, "/SKILL.md") {
			dirs = append(dirs, strings.TrimSuffix(entry, "/SKILL.md"))
		}
	}
	sort.Strings(dirs)
	return dirs
}

func tarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var entries []string
	for {
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		entries = append(entries, h.Name)
	}
	sort.Strings(entries)
	return entries
}

func tarFileContent(t *testing.T, path string, name string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				t.Fatalf("archive missing %s", name)
			}
			t.Fatal(err)
		}
		if strings.TrimPrefix(h.Name, "./") != name {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
}
