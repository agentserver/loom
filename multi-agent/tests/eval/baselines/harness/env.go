package harness

import "strings"

// alwaysAllowedEnvKeys are inherited from the parent env unconditionally
// by BOTH the agent subprocess and the oracle subprocess. Anything not
// on one of the allowlists here is dropped, per spec §7(a).
//
// Keep this list tight — adding a key invites it into every baseline's
// child forever; per-workload / per-baseline allowlists are the right
// place for anything not universally required.
var alwaysAllowedEnvKeys = []string{
	"PATH",
	"HOME",
	"LANG",
	"LC_ALL",
	"TZ",
	"USER",
}

// alwaysAllowedIfSetEnvKeys propagate to both subprocesses only when
// present in the parent env. commit_meta and its collectors consume
// these; the baseline oracle path never touches them but forwarding is
// harmless and keeps the two whitelist functions symmetric where possible.
var alwaysAllowedIfSetEnvKeys = []string{
	"AGENTSERVER_ROOT",
	"MODELSERVER_ROOT",
	"APP_ROOT",
	"MOCK_MODEL_URL",
}

// perWorkloadAllowedEnvKeys is the workload-scoped allow-list; adding
// a row requires a code change (spec §7(a) — a workload's spec.yaml
// cannot expand the allowlist). The single entry mirrors the runner's
// table for the credential-bound-model workload.
var perWorkloadAllowedEnvKeys = map[string][]string{
	"credential-bound-model": {"EXPECTED_MODEL_ALIAS"},
}

// AgentForwards is the caller-supplied set of per-baseline credential
// keys the operator has EXPLICITLY opted into forwarding to the agent
// subprocess (via --forward-anthropic-api-key etc.). The harness never
// maintains an implicit "if baseline == X then forward Y" table — spec
// §7(a) requires the opt-in be a separate CLI flag, decoupled from the
// choice of baseline. An empty slice means no per-baseline credential
// forwarding; that is the default.
type AgentForwards []string

// WhitelistEnvForAgent returns the env to hand to the per-baseline
// AGENT subprocess (e.g. bash for manual_ssh, `claude` for
// single_machine; cloud_sandbox has no agent subprocess). The returned
// slice is the complete env — callers pass it directly to exec.Cmd.Env
// without merging os.Environ().
//
// `forwards` is the opt-in credential key list; passing nil / empty
// yields exactly the same env as WhitelistEnvForOracle. Only keys
// listed in `forwards` are pulled from parent env for the agent.
//
// injected keys always win over parent-inherited ones (mirrors the
// runner's contract so `--stub-listen`-derived URLs override anything a
// parent may have set).
func WhitelistEnvForAgent(parent []string, workloadID string, forwards AgentForwards, injected map[string]string) []string {
	wanted := baseWantedSet(workloadID)
	for _, k := range forwards {
		wanted[k] = struct{}{}
	}
	return filterAndInject(parent, wanted, injected)
}

// WhitelistEnvForOracle returns the env to hand to the ORACLE
// subprocess. The set of keys is byte-identical to the WT-1-eval-runner
// whitelist (multi-agent/tools/eval/runner/subprocess.go); per-baseline
// creds are NEVER forwarded here regardless of what the operator opted
// in for the agent.
func WhitelistEnvForOracle(parent []string, workloadID string, injected map[string]string) []string {
	return filterAndInject(parent, baseWantedSet(workloadID), injected)
}

// baseWantedSet is the shared always/if-set/per-workload key set used
// by both whitelist functions. Extracted so
// TestWhitelistEnvForOracle_MatchesRunnerContract can round-trip the
// key list without duplicating literals.
func baseWantedSet(workloadID string) map[string]struct{} {
	wanted := map[string]struct{}{}
	for _, k := range alwaysAllowedEnvKeys {
		wanted[k] = struct{}{}
	}
	for _, k := range alwaysAllowedIfSetEnvKeys {
		wanted[k] = struct{}{}
	}
	for _, k := range perWorkloadAllowedEnvKeys[workloadID] {
		wanted[k] = struct{}{}
	}
	return wanted
}

// filterAndInject is the shared body of the two Whitelist* functions.
// LOOM_* prefix passthrough matches the runner's convention (per-team
// config namespace + test seam); the exact-length check prevents a
// stray `LOOM_=val` from slipping through.
func filterAndInject(parent []string, wanted map[string]struct{}, injected map[string]string) []string {
	out := make([]string, 0, len(wanted)+len(injected))
	seen := map[string]bool{}
	for _, kv := range parent {
		k, _, ok := splitEnv(kv)
		if !ok {
			continue
		}
		_, named := wanted[k]
		isLoomNS := len(k) > len("LOOM_") && strings.HasPrefix(k, "LOOM_")
		if !named && !isLoomNS {
			continue
		}
		out = append(out, kv)
		seen[k] = true
	}
	for k, v := range injected {
		if seen[k] {
			// Injected wins: rewrite in place rather than duplicate.
			for i, kv := range out {
				if pk, _, ok := splitEnv(kv); ok && pk == k {
					out[i] = k + "=" + v
				}
			}
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func splitEnv(kv string) (string, string, bool) {
	i := strings.IndexByte(kv, '=')
	if i <= 0 {
		return "", "", false
	}
	return kv[:i], kv[i+1:], true
}
