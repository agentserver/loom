package commandiface

import (
	"os/exec"
	"runtime"
	"strings"
)

type Detector struct {
	GOOS         string
	GOARCH       string
	LookPath     func(string) (string, error)
	WSLHasDistro func() bool
}

func Detect(skills []string) Capabilities {
	return Detector{}.Detect(skills)
}

func (d Detector) Detect(skills []string) Capabilities {
	d = d.withDefaults()
	uniqueSkills := dedupeSkills(skills)
	caps := Capabilities{
		Platform: Platform{OS: d.GOOS, Arch: d.GOARCH},
		Skills:   uniqueSkills,
	}

	switch d.GOOS {
	case "windows":
		caps = d.detectWindows(caps)
	default:
		caps = d.detectUnix(caps)
	}
	ensureSingleDefault(caps.CommandInterfaces)
	return caps
}

func (d Detector) withDefaults() Detector {
	if d.GOOS == "" {
		d.GOOS = runtime.GOOS
	}
	if d.GOARCH == "" {
		d.GOARCH = runtime.GOARCH
	}
	if d.LookPath == nil {
		d.LookPath = exec.LookPath
	}
	if d.WSLHasDistro == nil {
		d.WSLHasDistro = defaultWSLHasDistro
	}
	return d
}

func (d Detector) detectWindows(caps Capabilities) Capabilities {
	filteredSkills := make([]string, 0, len(caps.Skills))
	for _, skill := range caps.Skills {
		switch skill {
		case "powershell":
			filteredSkills = append(filteredSkills, skill)
			command := "powershell.exe"
			if path, err := d.LookPath("powershell.exe"); err == nil && path != "" {
				command = path
			}
			caps.CommandInterfaces = append(caps.CommandInterfaces, CommandInterface{
				Skill:   "powershell",
				Kind:    "powershell",
				Command: command,
				Default: true,
			})
		case "bash":
			if path, err := d.LookPath("bash.exe"); err == nil && path != "" {
				filteredSkills = append(filteredSkills, skill)
				caps.CommandInterfaces = append(caps.CommandInterfaces, CommandInterface{
					Skill:   "bash",
					Kind:    "bash",
					Command: path,
					Default: false,
				})
			} else if d.WSLHasDistro() {
				filteredSkills = append(filteredSkills, skill)
				caps.CommandInterfaces = append(caps.CommandInterfaces, CommandInterface{
					Skill:   "bash",
					Kind:    "bash",
					Command: "wsl.exe -- bash -lc",
					Default: false,
				})
			}
		default:
			filteredSkills = append(filteredSkills, skill)
		}
	}
	caps.Skills = filteredSkills
	return caps
}

func (d Detector) detectUnix(caps Capabilities) Capabilities {
	filteredSkills := make([]string, 0, len(caps.Skills))
	for _, skill := range caps.Skills {
		switch skill {
		case "bash":
			filteredSkills = append(filteredSkills, skill)
			command := "/bin/bash"
			if path, err := d.LookPath("bash"); err == nil && path != "" {
				command = path
			}
			caps.CommandInterfaces = append(caps.CommandInterfaces, CommandInterface{
				Skill:   "bash",
				Kind:    "bash",
				Command: command,
				Default: true,
			})
		case "powershell":
			if path, ok := d.findPowerShellUnix(); ok {
				filteredSkills = append(filteredSkills, skill)
				caps.CommandInterfaces = append(caps.CommandInterfaces, CommandInterface{
					Skill:   "powershell",
					Kind:    "powershell",
					Command: path,
					Default: false,
				})
			}
		default:
			filteredSkills = append(filteredSkills, skill)
		}
	}
	caps.Skills = filteredSkills
	return caps
}

func (d Detector) findPowerShellUnix() (string, bool) {
	for _, name := range []string{"pwsh", "powershell"} {
		if path, err := d.LookPath(name); err == nil && path != "" {
			return path, true
		}
	}
	return "", false
}

func dedupeSkills(skills []string) []string {
	seen := make(map[string]struct{}, len(skills))
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		if _, ok := seen[skill]; ok {
			continue
		}
		seen[skill] = struct{}{}
		out = append(out, skill)
	}
	return out
}

func ensureSingleDefault(interfaces []CommandInterface) {
	if len(interfaces) == 0 {
		return
	}
	defaultIndex := -1
	for i := range interfaces {
		if interfaces[i].Default && defaultIndex == -1 {
			defaultIndex = i
			continue
		}
		interfaces[i].Default = false
	}
	if defaultIndex == -1 {
		defaultIndex = 0
	}
	interfaces[defaultIndex].Default = true
}

func wslListOutputHasDistro(out []byte) bool {
	text := strings.ReplaceAll(string(out), "\x00", "")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "install") || strings.Contains(lower, "no installed") {
			continue
		}
		return true
	}
	return false
}
