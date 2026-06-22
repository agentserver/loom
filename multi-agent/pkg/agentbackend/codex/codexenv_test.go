package codex

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func TestEnvValueCaseInsensitive(t *testing.T) {
	if got := envValue([]string{"Codex_Home=/case"}, "CODEX_HOME"); got != "/case" {
		t.Fatalf("envValue = %q, want /case", got)
	}
}

func TestSplitEnv(t *testing.T) {
	k, v, ok := splitEnv("CODEX_HOME=/tmp/codex=with-equals")
	if !ok {
		t.Fatal("splitEnv ok = false")
	}
	if k != "CODEX_HOME" || v != "/tmp/codex=with-equals" {
		t.Fatalf("splitEnv = %q, %q; want CODEX_HOME, /tmp/codex=with-equals", k, v)
	}
	if _, _, ok := splitEnv("not-an-env-entry"); ok {
		t.Fatal("splitEnv malformed ok = true, want false")
	}
}

func TestResolveCodexHomeCfgWins(t *testing.T) {
	if got := resolveCodexHome(agentbackend.Config{CodexHome: "/cfg"}, []string{"CODEX_HOME=/env"}); got != "/cfg" {
		t.Fatalf("got %q, want /cfg", got)
	}
}

func TestResolveCodexHomeEnvSliceBeforeProcessEnv(t *testing.T) {
	t.Setenv("CODEX_HOME", "/proc")
	if got := resolveCodexHome(agentbackend.Config{}, []string{"CODEX_HOME=/env"}); got != "/env" {
		t.Fatalf("got %q, want /env", got)
	}
}

func TestResolveCodexHomeIgnoresProcessEnv(t *testing.T) {
	t.Setenv("CODEX_HOME", "/proc-stale")
	if got := resolveCodexHome(agentbackend.Config{}, nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestEffectiveCodexHomeFallbackHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "/proc-stale")
	home := t.TempDir()
	setTestHome(t, home)
	if got := effectiveCodexHome(agentbackend.Config{}, nil); got != filepath.Join(home, ".codex") {
		t.Fatalf("got %q, want %s", got, filepath.Join(home, ".codex"))
	}
}

func TestMergeEnvOverridesCaseInsensitive(t *testing.T) {
	merged := mergeEnv(
		[]string{"CODEX_HOME=/old", "Codex_Home=/old-case", "PATH=/bin"},
		[]string{"CODEX_HOME=/new", "FOO=bar"},
	)
	got := map[string]string{}
	count := 0
	for _, kv := range merged {
		k, v, ok := splitEnv(kv)
		if !ok {
			continue
		}
		if strings.EqualFold(k, "CODEX_HOME") {
			count++
		}
		got[strings.ToLower(k)] = v
	}
	if got["codex_home"] != "/new" {
		t.Fatalf("codex_home = %q, want /new", got["codex_home"])
	}
	if got["path"] != "/bin" {
		t.Fatalf("path = %q, want /bin", got["path"])
	}
	if got["foo"] != "bar" {
		t.Fatalf("foo = %q, want bar", got["foo"])
	}
	if count != 1 {
		t.Fatalf("CODEX_HOME appears %d times, want 1", count)
	}
}
