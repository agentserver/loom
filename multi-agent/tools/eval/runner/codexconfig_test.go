package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCodexConfigFixture drops a config file with the requested
// auth-field contents into t.TempDir(). Files under /tmp/ satisfy
// the §7.b sandbox arm, so tests do not need to poke at repoRoot.
func writeCodexConfigFixture(t *testing.T, name string, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

// fakeEnv builds a GetEnv closure that returns a fixed value for
// OPENAI_API_KEY and empty for everything else. Keeps tests parallel-
// safe (no os.Setenv).
func fakeEnv(openAIKey string) func(string) string {
	return func(k string) string {
		if k == "OPENAI_API_KEY" {
			return openAIKey
		}
		return ""
	}
}

const bearerConfigTOML = `
model_provider = "modelserver"
model = "glm-5.2"

[model_providers.modelserver]
name = "modelserver"
base_url = "http://127.0.0.1:53452/v1"
experimental_bearer_token = "sk-real1234567890abcdef"
wire_api = "responses"
`

const envKeyConfigTOML = `
model_provider = "openai-direct"
model = "gpt-5.5"

[model_providers.openai-direct]
name = "openai-direct"
base_url = "https://code.ai.cs.ac.cn/v1"
env_key = "OPENAI_API_KEY"
wire_api = "responses"
`

const bothFieldsTOML = `
[model_providers.hybrid]
experimental_bearer_token = "sk-real"
env_key = "OPENAI_API_KEY"
`

const noFieldTOML = `
[model_providers.broken]
name = "broken"
base_url = "http://127.0.0.1:53452/v1"
wire_api = "responses"
`

// TestValidateCodexConfig — the 8-row matrix documented in
// wt2-credential-workload.plan.md §5.
func TestValidateCodexConfig(t *testing.T) {
	t.Parallel()

	type tc struct {
		name       string
		mode       string
		configBody string        // empty → no path passed
		pathOverride string      // when non-empty overrides the tempfile path
		openAIKey  string
		wantErr    error         // nil → expect success
	}

	rows := []tc{
		{
			name:       "mode_a_bearer_ok",
			mode:       "a",
			configBody: bearerConfigTOML,
			openAIKey:  "",
			wantErr:    nil,
		},
		{
			name:       "mode_a_envkey_reject",
			mode:       "a",
			configBody: envKeyConfigTOML,
			openAIKey:  "",
			wantErr:    ErrCodexConfigMismatch,
		},
		{
			name:       "mode_b_envkey_ok",
			mode:       "b",
			configBody: envKeyConfigTOML,
			openAIKey:  "sk-real1234567890abcdef",
			wantErr:    nil,
		},
		{
			name:       "mode_b_bearer_reject",
			mode:       "b",
			configBody: bearerConfigTOML,
			openAIKey:  "sk-real1234567890abcdef",
			wantErr:    ErrCodexConfigMismatch,
		},
		{
			name:       "mode_b_env_sentinel",
			mode:       "b",
			configBody: envKeyConfigTOML,
			openAIKey:  "test-key",
			wantErr:    ErrOpenAIKeyMissing,
		},
		{
			name:       "mode_b_env_empty",
			mode:       "b",
			configBody: envKeyConfigTOML,
			openAIKey:  "",
			wantErr:    ErrOpenAIKeyMissing,
		},
		{
			name:         "path_traversal_reject",
			mode:         "a",
			configBody:   bearerConfigTOML,     // ignored; pathOverride wins
			pathOverride: "/etc/shadow",
			openAIKey:    "",
			wantErr:      ErrCodexConfigPathForbidden,
		},
		{
			name:       "mode_invalid",
			mode:       "c",
			configBody: bearerConfigTOML,
			openAIKey:  "",
			wantErr:    ErrCodexConfigModeInvalid,
		},
	}

	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			path := ""
			if row.pathOverride != "" {
				path = row.pathOverride
			} else if row.configBody != "" {
				path = writeCodexConfigFixture(t, "config.toml", row.configBody)
			}

			err := validateCodexConfig(codexConfigInputs{
				Path:     path,
				Mode:     row.mode,
				RepoRoot: "", // /tmp/ allowlist covers all fixture paths
				GetEnv:   fakeEnv(row.openAIKey),
			})

			if row.wantErr == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, row.wantErr) {
				t.Fatalf("want errors.Is(%v), got %v", row.wantErr, err)
			}
		})
	}
}

// TestValidateCodexConfig_NoFlagsIsPassThrough — neither flag → nil.
// Guards PR #53's backward compatibility.
func TestValidateCodexConfig_NoFlagsIsPassThrough(t *testing.T) {
	t.Parallel()
	if err := validateCodexConfig(codexConfigInputs{
		Path:   "",
		Mode:   "",
		GetEnv: fakeEnv(""),
	}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestValidateCodexConfig_AmbiguousBothFields — both fields present is
// rejected under either mode (spec §7.a matrix).
func TestValidateCodexConfig_AmbiguousBothFields(t *testing.T) {
	t.Parallel()
	p := writeCodexConfigFixture(t, "config.toml", bothFieldsTOML)
	for _, mode := range []string{"a", "b", ""} {
		mode := mode
		t.Run("mode_"+mode, func(t *testing.T) {
			t.Parallel()
			err := validateCodexConfig(codexConfigInputs{
				Path:   p,
				Mode:   mode,
				GetEnv: fakeEnv("sk-real1234567890abcdef"),
			})
			if !errors.Is(err, ErrCodexConfigMismatch) {
				t.Fatalf("want ErrCodexConfigMismatch, got %v", err)
			}
		})
	}
}

// TestValidateCodexConfig_NeitherFieldRejected — neither field present
// is rejected too; a config with no auth field is broken.
func TestValidateCodexConfig_NeitherFieldRejected(t *testing.T) {
	t.Parallel()
	p := writeCodexConfigFixture(t, "config.toml", noFieldTOML)
	for _, mode := range []string{"a", "b", ""} {
		mode := mode
		t.Run("mode_"+mode, func(t *testing.T) {
			t.Parallel()
			err := validateCodexConfig(codexConfigInputs{
				Path:   p,
				Mode:   mode,
				GetEnv: fakeEnv("sk-real1234567890abcdef"),
			})
			if !errors.Is(err, ErrCodexConfigMismatch) {
				t.Fatalf("want ErrCodexConfigMismatch, got %v", err)
			}
		})
	}
}

// TestValidateCodexConfig_ConfigContentsNeverLogged — the config body
// carries a bearer-shaped token; the resulting error message must
// NOT contain that token substring (spec §7.c).
func TestValidateCodexConfig_ConfigContentsNeverLogged(t *testing.T) {
	t.Parallel()
	// Trigger a mismatch by passing --codex-config-mode b against a
	// bearer config; the returned error stringifies the path but not
	// the file contents.
	secret := "sk-secret9999999998888888"
	body := strings.ReplaceAll(bearerConfigTOML, "sk-real1234567890abcdef", secret)
	p := writeCodexConfigFixture(t, "config.toml", body)

	err := validateCodexConfig(codexConfigInputs{
		Path:   p,
		Mode:   "b",
		GetEnv: fakeEnv("sk-real1234567890abcdef"),
	})
	if err == nil {
		t.Fatalf("want mismatch error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked token substring: %q", err.Error())
	}
}

// TestValidateCodexConfig_InlineCommentsIgnored — a `#` comment on a
// line MUST NOT count as an auth-field match.
func TestValidateCodexConfig_InlineCommentsIgnored(t *testing.T) {
	t.Parallel()
	body := `
# experimental_bearer_token = "would-be-bearer"
env_key = "OPENAI_API_KEY"   # this line is legit
`
	p := writeCodexConfigFixture(t, "config.toml", body)
	err := validateCodexConfig(codexConfigInputs{
		Path:   p,
		Mode:   "b",
		GetEnv: fakeEnv("sk-real1234567890abcdef"),
	})
	if err != nil {
		t.Fatalf("comment-only bearer line should not trigger mismatch: %v", err)
	}
}

// TestValidateCodexConfig_SentinelPlaceholderKey — `sk-XXXXXX` pattern
// is rejected (spec §7.d).
func TestValidateCodexConfig_SentinelPlaceholderKey(t *testing.T) {
	t.Parallel()
	p := writeCodexConfigFixture(t, "config.toml", envKeyConfigTOML)
	for _, key := range []string{"sk-XXXXXX", "sk-XXXXXXXXXX", "changeme", "x"} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			err := validateCodexConfig(codexConfigInputs{
				Path:   p,
				Mode:   "b",
				GetEnv: fakeEnv(key),
			})
			if !errors.Is(err, ErrOpenAIKeyMissing) {
				t.Fatalf("want ErrOpenAIKeyMissing for %q, got %v", key, err)
			}
		})
	}
}

// TestValidateCodexConfig_ModeAOnlyDoesNotAssertEnv — mode=a should
// NOT read OPENAI_API_KEY; a missing key is fine.
func TestValidateCodexConfig_ModeAOnlyDoesNotAssertEnv(t *testing.T) {
	t.Parallel()
	p := writeCodexConfigFixture(t, "config.toml", bearerConfigTOML)
	err := validateCodexConfig(codexConfigInputs{
		Path:   p,
		Mode:   "a",
		GetEnv: fakeEnv(""), // deliberately empty
	})
	if err != nil {
		t.Fatalf("mode=a should not require OPENAI_API_KEY: %v", err)
	}
}

// TestValidateCodexConfig_EnvKeyConfigNoModeRejectsSentinel — Codex
// review round 3 P0: an env_key config passed WITHOUT --codex-config-mode
// must still trigger the sentinel-key rejection.  Otherwise
// `--codex-config-path <env_key.toml>` alone would silently accept
// OPENAI_API_KEY=test-key and produce fake latency data.
func TestValidateCodexConfig_EnvKeyConfigNoModeRejectsSentinel(t *testing.T) {
	t.Parallel()
	p := writeCodexConfigFixture(t, "config.toml", envKeyConfigTOML)
	for _, key := range []string{"", "sk-fake", "test-key", "changeme", "x", "sk-XXXXXX"} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			err := validateCodexConfig(codexConfigInputs{
				Path:   p,
				Mode:   "", // no mode passed
				GetEnv: fakeEnv(key),
			})
			if !errors.Is(err, ErrOpenAIKeyMissing) {
				t.Fatalf("want ErrOpenAIKeyMissing for env-key config + sentinel %q (no mode), got %v", key, err)
			}
		})
	}
}

// TestValidateCodexConfig_EnvKeyConfigNoModePassesRealKey — the
// counterpart to the above: a real OPENAI_API_KEY value under an
// env_key config, with no --codex-config-mode set, should pass.
func TestValidateCodexConfig_EnvKeyConfigNoModePassesRealKey(t *testing.T) {
	t.Parallel()
	p := writeCodexConfigFixture(t, "config.toml", envKeyConfigTOML)
	err := validateCodexConfig(codexConfigInputs{
		Path:   p,
		Mode:   "",
		GetEnv: fakeEnv("sk-real1234567890abcdef"),
	})
	if err != nil {
		t.Fatalf("env-key config + real key (no mode) should pass, got: %v", err)
	}
}

// TestValidateCodexConfig_SymlinkEscapeRejected — Codex review round 3
// P0: `/tmp/<link>` pointing at `/etc/shadow` (or any file outside the
// allowlist) must fail the sandbox check.  Without EvalSymlinks the
// lexical Abs-check would pass since /tmp/... is on the allowlist.
func TestValidateCodexConfig_SymlinkEscapeRejected(t *testing.T) {
	t.Parallel()
	// Create a benign target file OUTSIDE the allowlist (specifically
	// outside /tmp and outside any tempdir root the test can express);
	// we can't use /etc/shadow in the test, so use a target under the
	// user's HOME.  Skip if HOME isn't usable.  This still exercises
	// the EvalSymlinks path — the allowlist doesn't include $HOME so
	// the resolved path fails confinement.
	homeTarget := filepath.Join(t.TempDir(), "..", "escape-target")
	// Move the target one directory ABOVE t.TempDir() so it's still on
	// /tmp/ (t.TempDir() itself is under /tmp/); a resolved path of
	// /tmp/... would satisfy the allowlist and defeat the test.
	// Instead, use a target under /etc/ which the test process can't
	// create, and rely on EvalSymlinks failing OR resolving to a
	// forbidden path.  Simplest: point the link at /etc/hostname (world-
	// readable) and confirm the check rejects.
	target := "/etc/hostname"
	if _, err := os.Stat(target); err != nil {
		t.Skipf("test needs a readable /etc/hostname to build a symlink victim: %v", err)
	}
	_ = homeTarget // silences unused-var warning if the skip path is not taken
	link := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink under tempdir: %v", err)
	}
	err := validateCodexConfig(codexConfigInputs{
		Path:   link,
		Mode:   "a",
		GetEnv: fakeEnv(""),
	})
	if !errors.Is(err, ErrCodexConfigPathForbidden) {
		t.Fatalf("want ErrCodexConfigPathForbidden for symlink escape, got %v", err)
	}
}
