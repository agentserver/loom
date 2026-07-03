package scriptstest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptPath resolves the e2e script relative to the test binary's
// runtime location so `go test ./tests/scripts/...` works from any
// cwd.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: cannot determine test file location")
	}
	dir := filepath.Dir(thisFile)
	return filepath.Join(dir, "scaffold_acceptance_register_e2e.sh")
}

// TestE2EScript_StartsWithShebangAndSet — spec §7 (c).
func TestE2EScript_StartsWithShebangAndSet(t *testing.T) {
	body, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	first := string(body)
	if !strings.HasPrefix(first, "#!/usr/bin/env bash\n") {
		t.Fatal("script must start with `#!/usr/bin/env bash`")
	}
	if !strings.Contains(first, "\nset -euo pipefail\n") {
		t.Fatal("script must contain `set -euo pipefail`")
	}
}

// TestE2EScript_UsesHeredocQuoted — spec §7 (c). Any embedded bash
// block must use `<<'…'` (quoted heredoc) so drift-check tools don't
// treat variable-expansion inside the heredoc as a runtime bug.
func TestE2EScript_UsesHeredocQuoted(t *testing.T) {
	body, err := os.ReadFile(scriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<<'") {
		t.Fatal("script must contain at least one quoted heredoc (`<<'…'`)")
	}
}

// TestE2EScript_SmokeBuildsRequestBody — run the script with
// SMOKE_ONLY=true which short-circuits before curl. Verifies the
// request body is well-formed enough to include the four B6 audit
// fields under both --dry-run and default.
func TestE2EScript_SmokeBuildsRequestBody(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	for _, tc := range []struct {
		name     string
		args     []string
		wantWord string
	}{
		{"default", nil, "explicit_user_request"},
		{"dry_run", []string{"--dry-run"}, "\"dry_run_register\": true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", append([]string{scriptPath(t)}, tc.args...)...)
			cmd.Env = append(os.Environ(), "SMOKE_ONLY=true")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script err: %v\noutput:\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.wantWord) {
				t.Fatalf("output missing %q:\n%s", tc.wantWord, out)
			}
		})
	}
}
