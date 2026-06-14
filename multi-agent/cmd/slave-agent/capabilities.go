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
		routes["bash"] = executor.NewBashExecutor(bashConfigForRuntime(cfg.Agent.WorkDir, caps.CommandInterfaces))
	}
	if hasSkill(caps.Skills, "powershell") {
		routes["powershell"] = executor.NewPowerShellExecutor(executor.PowerShellConfig{WorkDir: cfg.Agent.WorkDir})
	}
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
