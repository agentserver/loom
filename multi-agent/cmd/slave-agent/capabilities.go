package main

import (
	"strings"

	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/executor"
)

func normalizeDiscoveryForRuntime(cfg *config.Config, detector commandiface.Detector) commandiface.Capabilities {
	if cfg == nil {
		return detector.Detect(nil)
	}
	return detector.Detect(cfg.Discovery.Skills)
}

func applyRuntimeCapabilities(cfg *config.Config, caps commandiface.Capabilities) {
	if cfg == nil {
		return
	}
	cfg.Discovery.Skills = append([]string{}, caps.Skills...)
}

func registerRuntimeShellRoutes(routes map[string]executor.Executor, cfg *config.Config, caps commandiface.Capabilities) {
	if cfg == nil {
		return
	}
	if hasSkill(caps.Skills, "bash") {
		routes["bash"] = executor.NewBashExecutor(bashConfigForRuntime(bashExecutorWorkDir(cfg), caps.CommandInterfaces))
	}
	if hasSkill(caps.Skills, "powershell") {
		routes["powershell"] = executor.NewPowerShellExecutor(executor.PowerShellConfig{WorkDir: powerShellExecutorWorkDir(cfg)})
	}
}

// bashExecutorWorkDir returns the working directory for the bash
// executor. Reads cfg.Agent.WorkDir — previously hardcoded
// cfg.Claude.WorkDir, which was wrong on codex slaves where
// cfg.Claude.WorkDir was empty and the executor silently ran from
// process cwd. Fixes one of the bugs surfaced by issue #15.
func bashExecutorWorkDir(cfg *config.Config) string {
	return cfg.Agent.WorkDir
}

// powerShellExecutorWorkDir same as bash, for the PowerShell capability.
func powerShellExecutorWorkDir(cfg *config.Config) string {
	return cfg.Agent.WorkDir
}

func bashConfigForRuntime(workDir string, interfaces []commandiface.CommandInterface) executor.BashConfig {
	cfg := executor.BashConfig{WorkDir: workDir}
	for _, iface := range interfaces {
		if iface.Kind != "bash" || iface.Command == "" {
			continue
		}
		if strings.EqualFold(iface.Command, "wsl.exe -- bash -lc") {
			cfg.Bin = "wsl.exe"
			cfg.Args = []string{"--", "bash", "-lc"}
			return cfg
		}
		cfg.Bin = iface.Command
		cfg.Args = []string{"-lc"}
		return cfg
	}
	return cfg
}
