package codex

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/yourorg/multi-agent/pkg/agentbackend"
)

func envValue(env []string, key string) string {
	for _, kv := range env {
		k, v, ok := splitEnv(kv)
		if ok && strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func resolveCodexHome(cfg agentbackend.Config, env []string) string {
	if cfg.CodexHome != "" {
		return cfg.CodexHome
	}
	return envValue(env, "CODEX_HOME")
}

func effectiveCodexHome(cfg agentbackend.Config, env []string) string {
	if home := resolveCodexHome(cfg, env); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func splitEnv(kv string) (string, string, bool) {
	i := strings.IndexByte(kv, '=')
	if i < 0 {
		return "", "", false
	}
	return kv[:i], kv[i+1:], true
}

func mergeEnv(base, override []string) []string {
	out := make([]string, 0, len(base)+len(override))
	indexByKey := make(map[string]int, len(base)+len(override))
	appendOrReplace := func(kv string) {
		k, _, ok := splitEnv(kv)
		if !ok {
			out = append(out, kv)
			return
		}
		key := strings.ToLower(k)
		if idx, ok := indexByKey[key]; ok {
			out[idx] = kv
			return
		}
		indexByKey[key] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		appendOrReplace(kv)
	}
	for _, kv := range override {
		appendOrReplace(kv)
	}
	return out
}
