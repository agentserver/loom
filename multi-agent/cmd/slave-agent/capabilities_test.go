package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
	"github.com/yourorg/multi-agent/internal/executor"
)

type testSink struct{}

func (testSink) Write(string, string) {}
func (testSink) Close()               {}

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
	wantConfiguredSkills := []string{"chat", "bash", "powershell", "bash"}
	if !equalStrings(cfg.Discovery.Skills, wantConfiguredSkills) {
		t.Fatalf("cfg.Discovery.Skills = %v, want original configured skills %v", cfg.Discovery.Skills, wantConfiguredSkills)
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
	caps := commandiface.Capabilities{Skills: []string{"chat", "powershell"}}
	routes := map[string]executor.Executor{}

	registerRuntimeShellRoutes(routes, cfg, caps)

	if _, ok := routes["powershell"]; !ok {
		t.Fatal("powershell route was not registered")
	}
	if _, ok := routes["bash"]; ok {
		t.Fatal("bash route was registered without normalized bash skill")
	}
}

func TestRegisterRuntimeShellRoutesUsesDetectedBashCommand(t *testing.T) {
	workdir := t.TempDir()
	fakeBash := filepath.Join(t.TempDir(), "fake-bash")
	if err := os.WriteFile(fakeBash, []byte("#!/bin/sh\nprintf 'fake-bash:%s:%s\\n' \"$1\" \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Claude: config.Claude{WorkDir: workdir}}
	caps := commandiface.Capabilities{
		Skills: []string{"bash"},
		CommandInterfaces: []commandiface.CommandInterface{{
			Skill: "bash", Kind: "bash", Command: fakeBash, Default: true,
		}},
	}
	routes := map[string]executor.Executor{}

	registerRuntimeShellRoutes(routes, cfg, caps)
	bashRoute, ok := routes["bash"]
	if !ok {
		t.Fatal("bash route was not registered")
	}
	res, err := bashRoute.Run(context.Background(), executor.Task{Prompt: `{"script":"echo ok"}`}, testSink{})
	if err != nil {
		t.Fatalf("bash route returned error: %v", err)
	}
	var got executor.BashResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stdout != "fake-bash:-lc:echo ok\n" {
		t.Fatalf("stdout = %q, want fake bash command to receive -lc and script", got.Stdout)
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
