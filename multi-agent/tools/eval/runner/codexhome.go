package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type codexSourceConfig struct {
	ModelProvider        string                       `toml:"model_provider"`
	Model                string                       `toml:"model"`
	ModelReasoningEffort string                       `toml:"model_reasoning_effort"`
	ModelProviders       map[string]map[string]any    `toml:"model_providers"`
	Projects             map[string]codexProjectTrust `toml:"projects,omitempty"`
}

type codexBenchmarkConfig struct {
	ModelProvider        string                       `toml:"model_provider,omitempty"`
	Model                string                       `toml:"model,omitempty"`
	ModelReasoningEffort string                       `toml:"model_reasoning_effort,omitempty"`
	ModelProviders       map[string]map[string]any    `toml:"model_providers,omitempty"`
	Projects             map[string]codexProjectTrust `toml:"projects,omitempty"`
}

type codexProjectTrust struct {
	TrustLevel string `toml:"trust_level"`
}

func prepareBenchmarkCodexHome(workspaceRoot string, opts Opts) (string, func(), error) {
	srcConfig := resolveSourceCodexConfigPath(opts)
	var src codexSourceConfig
	if _, err := toml.DecodeFile(srcConfig, &src); err != nil {
		return "", nil, fmt.Errorf("benchmark codex home: read source config: %w", err)
	}
	if src.ModelProvider == "" {
		return "", nil, fmt.Errorf("benchmark codex home: source config missing model_provider")
	}
	provider, ok := src.ModelProviders[src.ModelProvider]
	if !ok {
		return "", nil, fmt.Errorf("benchmark codex home: source config missing model provider %q", src.ModelProvider)
	}

	tmp, err := os.MkdirTemp("", "evalrun-codex-home-")
	if err != nil {
		return "", nil, fmt.Errorf("benchmark codex home: mktemp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	workspaceAbs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("benchmark codex home: abs(workspace): %w", err)
	}
	cfg := codexBenchmarkConfig{
		ModelProvider:        src.ModelProvider,
		Model:                src.Model,
		ModelReasoningEffort: src.ModelReasoningEffort,
		ModelProviders:       map[string]map[string]any{src.ModelProvider: provider},
		Projects: map[string]codexProjectTrust{
			workspaceAbs: {TrustLevel: "trusted"},
			"/tmp":       {TrustLevel: "trusted"},
		},
	}
	configPath := filepath.Join(tmp, "config.toml")
	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("benchmark codex home: create config: %w", err)
	}
	encErr := toml.NewEncoder(f).Encode(cfg)
	closeErr := f.Close()
	if encErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("benchmark codex home: encode config: %w", encErr)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("benchmark codex home: close config: %w", closeErr)
	}
	return tmp, cleanup, nil
}

func resolveSourceCodexConfigPath(opts Opts) string {
	if opts.CodexConfigPath != "" {
		return opts.CodexConfigPath
	}
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex", "config.toml")
	}
	return filepath.Join(".codex", "config.toml")
}

func withEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			if !replaced {
				out = append(out, prefix+value)
				replaced = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, prefix+value)
	}
	return out
}
