package harness

import (
	"slices"
	"sort"
	"testing"
)

func envHas(env []string, kv string) bool {
	return slices.Contains(env, kv)
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if len(e) > len(prefix) && e[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// TestWhitelistEnvForAgent_OptInForwardsAnthropic — plan #7. When the
// operator explicitly opts in via AgentForwards, the key propagates.
func TestWhitelistEnvForAgent_OptInForwardsAnthropic(t *testing.T) {
	parent := []string{
		"ANTHROPIC_API_KEY=sk-ant-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"PATH=/usr/bin",
	}
	env := WhitelistEnvForAgent(parent, "cross-device-code-mod", AgentForwards{"ANTHROPIC_API_KEY"}, nil)
	if !envHasKey(env, "ANTHROPIC_API_KEY") {
		t.Errorf("opt-in should propagate ANTHROPIC_API_KEY; env=%v", env)
	}
	if !envHasKey(env, "PATH") {
		t.Errorf("PATH missing from agent env: %v", env)
	}
}

// TestWhitelistEnvForAgent_DefaultDropsAnthropicKey — plan #7b. Spec
// §7(a): "opt-in is expressed by a dedicated CLI flag, NOT by the
// choice of --baseline-name". No implicit forwarding.
func TestWhitelistEnvForAgent_DefaultDropsAnthropicKey(t *testing.T) {
	parent := []string{
		"ANTHROPIC_API_KEY=sk-ant-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"PATH=/usr/bin",
	}
	// No forwards → no cred propagation, regardless of workload / baseline.
	env := WhitelistEnvForAgent(parent, "cross-device-code-mod", nil, nil)
	if envHasKey(env, "ANTHROPIC_API_KEY") {
		t.Errorf("default (no forwards) should drop ANTHROPIC_API_KEY; env=%v", env)
	}
}

// TestWhitelistEnvForOracle_MatchesRunnerContract — plan #8. The oracle
// env's key SET must equal the WT-1-eval-runner whitelist. We can't
// directly import from tools/eval/runner (package main), so mirror the
// literal contract here and pin the exact allowed keys — a future
// runner change that adds an oracle-visible key must land a matching
// baseline-harness update.
func TestWhitelistEnvForOracle_MatchesRunnerContract(t *testing.T) {
	// The runner's `AlwaysAllowedEnvKeys` + `AlwaysAllowedIfSetEnvKeys`
	// + `PerWorkloadAllowedEnvKeys[workload]` + LOOM_* prefix. This
	// literal is the byte-identical mirror.
	//
	// If this list drifts from tools/eval/runner/subprocess.go the two
	// pipelines will silently disagree on what the oracle sees; the
	// TODO is to eventually lift the two lists into a shared internal
	// package (spec §6 called this out).
	wantAlways := []string{"PATH", "HOME", "LANG", "LC_ALL", "TZ", "USER"}
	wantAlwaysIfSet := []string{"AGENTSERVER_ROOT", "MODELSERVER_ROOT", "APP_ROOT", "MOCK_MODEL_URL"}
	wantPerWorkload := []string{"EXPECTED_MODEL_ALIAS"}

	// Assert our shared table matches the mirror exactly (locks drift).
	if got := sortedCopy(alwaysAllowedEnvKeys); !slices.Equal(got, sortedCopy(wantAlways)) {
		t.Errorf("alwaysAllowedEnvKeys drift: got %v want %v", got, wantAlways)
	}
	if got := sortedCopy(alwaysAllowedIfSetEnvKeys); !slices.Equal(got, sortedCopy(wantAlwaysIfSet)) {
		t.Errorf("alwaysAllowedIfSetEnvKeys drift: got %v want %v", got, wantAlwaysIfSet)
	}
	if got := sortedCopy(perWorkloadAllowedEnvKeys["credential-bound-model"]); !slices.Equal(got, wantPerWorkload) {
		t.Errorf("perWorkloadAllowedEnvKeys[credential-bound-model] drift: got %v want %v", got, wantPerWorkload)
	}
}

// TestWhitelistEnvForOracle_DropsAgentCreds_WhenOptedIn — plan #9.
// Even when the operator opted in for the AGENT, oracle env is clean.
func TestWhitelistEnvForOracle_DropsAgentCreds_WhenOptedIn(t *testing.T) {
	parent := []string{
		"ANTHROPIC_API_KEY=sk-ant-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"E2B_API_KEY=e2b-testkey-abcdefghijklmnop",
		"PATH=/usr/bin",
	}
	// Simulate a run where the operator forwarded ANTHROPIC to the agent.
	_ = WhitelistEnvForAgent(parent, "cross-device-code-mod", AgentForwards{"ANTHROPIC_API_KEY"}, nil)
	env := WhitelistEnvForOracle(parent, "cross-device-code-mod", nil)
	if envHasKey(env, "ANTHROPIC_API_KEY") {
		t.Errorf("ANTHROPIC_API_KEY leaked into oracle env: %v", env)
	}
	if envHasKey(env, "E2B_API_KEY") {
		t.Errorf("E2B_API_KEY leaked into oracle env: %v", env)
	}
}

// TestWhitelistEnvForAgent_DropsGenericSecrets — plan #10. Third-party
// API keys never propagate — opt-in is irrelevant here (the caller
// simply never lists them).
func TestWhitelistEnvForAgent_DropsGenericSecrets(t *testing.T) {
	parent := []string{
		"OPENAI_API_KEY=sk-openai-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"AWS_ACCESS_KEY_ID=AKIAABCDEFGHIJKLMNOP",
		"AWS_SECRET_ACCESS_KEY=dontleakme",
		"GH_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"PATH=/usr/bin",
	}
	env := WhitelistEnvForAgent(parent, "cross-device-code-mod", AgentForwards{"ANTHROPIC_API_KEY"}, nil)
	for _, k := range []string{"OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "GH_TOKEN"} {
		if envHasKey(env, k) {
			t.Errorf("%s should never propagate; env: %v", k, env)
		}
	}
}

// Injected wins over parent for keys the parent also set — mirrors the
// runner's contract.
func TestWhitelistEnvForAgent_InjectedWinsOverParent(t *testing.T) {
	parent := []string{"PATH=/parent/path"}
	env := WhitelistEnvForAgent(parent, "cross-device-code-mod", nil, map[string]string{
		"PATH": "/inject/path",
	})
	if !envHas(env, "PATH=/inject/path") {
		t.Errorf("injected PATH lost, got %v", env)
	}
	if envHas(env, "PATH=/parent/path") {
		t.Errorf("both PATH entries present; expected inject to replace parent: %v", env)
	}
}

// LOOM_* prefix passthrough — test seam per spec §7(a).
func TestWhitelistEnvForAgent_LoomPrefixAllowed(t *testing.T) {
	parent := []string{"LOOM_TEST_MODE=on", "LOOM_=bogus", "PATH=/usr/bin"}
	env := WhitelistEnvForAgent(parent, "cross-device-code-mod", nil, nil)
	if !envHas(env, "LOOM_TEST_MODE=on") {
		t.Errorf("LOOM_TEST_MODE should propagate: %v", env)
	}
	if envHasKey(env, "LOOM_") {
		t.Errorf("bare LOOM_=... should be rejected: %v", env)
	}
}

// Per-workload key EXPECTED_MODEL_ALIAS only for credential-bound-model.
func TestWhitelistEnvForAgent_PerWorkloadKey(t *testing.T) {
	parent := []string{"EXPECTED_MODEL_ALIAS=acme-bound-model-v1", "PATH=/usr/bin"}

	env := WhitelistEnvForAgent(parent, "credential-bound-model", nil, nil)
	if !envHasKey(env, "EXPECTED_MODEL_ALIAS") {
		t.Errorf("EXPECTED_MODEL_ALIAS missing for credential-bound-model: %v", env)
	}

	env = WhitelistEnvForAgent(parent, "cross-device-code-mod", nil, nil)
	if envHasKey(env, "EXPECTED_MODEL_ALIAS") {
		t.Errorf("EXPECTED_MODEL_ALIAS leaked into cross-device-code-mod: %v", env)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
