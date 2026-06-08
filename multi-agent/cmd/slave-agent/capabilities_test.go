package main

import (
	"errors"
	"testing"

	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/executor"
)

func TestNormalizeDiscoveryForRuntimeWindowsRemovesUnavailableBashAndKeepsPowerShell(t *testing.T) {
	cfg := &config.Config{
		Discovery: config.Discovery{
			Skills: []string{"chat", "bash", "powershell", "bash"},
		},
	}
	detector := commandiface.Detector{
		GOOS:   "windows",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			if name == "powershell.exe" {
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			}
			return "", errors.New("not found")
		},
		WSLHasDistro: func() bool { return false },
	}

	caps := normalizeDiscoveryForRuntime(cfg, detector)

	wantSkills := []string{"chat", "powershell"}
	if !equalStrings(cfg.Discovery.Skills, wantSkills) {
		t.Fatalf("cfg.Discovery.Skills = %v, want %v", cfg.Discovery.Skills, wantSkills)
	}
	if !equalStrings(caps.Skills, wantSkills) {
		t.Fatalf("caps.Skills = %v, want %v", caps.Skills, wantSkills)
	}
	wantInterfaces := []commandiface.CommandInterface{{
		Skill:   "powershell",
		Kind:    "powershell",
		Command: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		Default: true,
	}}
	if !equalCommandInterfaces(caps.CommandInterfaces, wantInterfaces) {
		t.Fatalf("command interfaces = %#v, want %#v", caps.CommandInterfaces, wantInterfaces)
	}
	if caps.Platform != (commandiface.Platform{OS: "windows", Arch: "amd64"}) {
		t.Fatalf("platform = %#v, want windows/amd64", caps.Platform)
	}
}

func TestRegisterRuntimeShellRoutesUsesNormalizedSkills(t *testing.T) {
	cfg := &config.Config{
		Discovery: config.Discovery{Skills: []string{"chat", "powershell"}},
		Claude:    config.Claude{WorkDir: t.TempDir()},
	}
	routes := map[string]executor.Executor{}

	registerRuntimeShellRoutes(routes, cfg)

	if _, ok := routes["powershell"]; !ok {
		t.Fatal("powershell route was not registered")
	}
	if _, ok := routes["bash"]; ok {
		t.Fatal("bash route was registered without normalized bash skill")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalCommandInterfaces(a, b []commandiface.CommandInterface) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
