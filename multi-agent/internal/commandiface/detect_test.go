package commandiface

import (
	"errors"
	"reflect"
	"testing"
)

func TestDetectWindowsPowerShellDefaultAndRemovesUnavailableBash(t *testing.T) {
	d := Detector{
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

	got := d.Detect([]string{"chat", "bash", "powershell"})

	wantSkills := []string{"chat", "powershell"}
	if !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("skills = %v, want %v", got.Skills, wantSkills)
	}
	wantInterfaces := []CommandInterface{{
		Skill:   "powershell",
		Kind:    "powershell",
		Command: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
		Default: true,
	}}
	if !reflect.DeepEqual(got.CommandInterfaces, wantInterfaces) {
		t.Fatalf("command interfaces = %#v, want %#v", got.CommandInterfaces, wantInterfaces)
	}
	if got.Platform != (Platform{OS: "windows", Arch: "amd64"}) {
		t.Fatalf("platform = %#v", got.Platform)
	}
}

func TestDetectWindowsGitBashPresent(t *testing.T) {
	d := Detector{
		GOOS:   "windows",
		GOARCH: "arm64",
		LookPath: func(name string) (string, error) {
			switch name {
			case "powershell.exe":
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			case "bash.exe":
				return `C:\Program Files\Git\bin\bash.exe`, nil
			default:
				return "", errors.New("not found")
			}
		},
		WSLHasDistro: func() bool { return false },
	}

	got := d.Detect([]string{"bash", "powershell"})

	wantSkills := []string{"bash", "powershell"}
	if !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("skills = %v, want %v", got.Skills, wantSkills)
	}
	wantInterfaces := []CommandInterface{
		{Skill: "bash", Kind: "bash", Command: `C:\Program Files\Git\bin\bash.exe`, Default: false},
		{Skill: "powershell", Kind: "powershell", Command: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Default: true},
	}
	if !reflect.DeepEqual(got.CommandInterfaces, wantInterfaces) {
		t.Fatalf("command interfaces = %#v, want %#v", got.CommandInterfaces, wantInterfaces)
	}
}

func TestDetectUnixBashDefault(t *testing.T) {
	d := Detector{
		GOOS:   "linux",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			if name == "bash" {
				return "/usr/bin/bash", nil
			}
			return "", errors.New("not found")
		},
	}

	got := d.Detect([]string{"chat", "bash", "powershell"})

	wantSkills := []string{"chat", "bash", "powershell"}
	if !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("skills = %v, want %v", got.Skills, wantSkills)
	}
	wantInterfaces := []CommandInterface{
		{Skill: "bash", Kind: "bash", Command: "/usr/bin/bash", Default: true},
	}
	if !reflect.DeepEqual(got.CommandInterfaces, wantInterfaces) {
		t.Fatalf("command interfaces = %#v, want %#v", got.CommandInterfaces, wantInterfaces)
	}
}

func TestDetectDeduplicatesSkillsAndKeepsOneDefault(t *testing.T) {
	d := Detector{
		GOOS:   "linux",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			switch name {
			case "bash":
				return "/bin/bash", nil
			case "pwsh":
				return "/usr/bin/pwsh", nil
			default:
				return "", errors.New("not found")
			}
		},
	}

	got := d.Detect([]string{"chat", "bash", "chat", "powershell", "bash"})

	wantSkills := []string{"chat", "bash", "powershell"}
	if !reflect.DeepEqual(got.Skills, wantSkills) {
		t.Fatalf("skills = %v, want %v", got.Skills, wantSkills)
	}
	defaults := 0
	for _, ci := range got.CommandInterfaces {
		if ci.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("default count = %d, command interfaces = %#v", defaults, got.CommandInterfaces)
	}
}
