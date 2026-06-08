package main

import (
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/executor"
)

func normalizeDiscoveryForRuntime(cfg *config.Config, detector commandiface.Detector) commandiface.Capabilities {
	if cfg == nil {
		return detector.Detect(nil)
	}
	caps := detector.Detect(cfg.Discovery.Skills)
	cfg.Discovery.Skills = append([]string{}, caps.Skills...)
	return caps
}

func registerRuntimeShellRoutes(routes map[string]executor.Executor, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if hasSkill(cfg.Discovery.Skills, "bash") {
		routes["bash"] = executor.NewBashExecutor(executor.BashConfig{WorkDir: cfg.Claude.WorkDir})
	}
	if hasSkill(cfg.Discovery.Skills, "powershell") {
		routes["powershell"] = executor.NewPowerShellExecutor(executor.PowerShellConfig{WorkDir: cfg.Claude.WorkDir})
	}
}
