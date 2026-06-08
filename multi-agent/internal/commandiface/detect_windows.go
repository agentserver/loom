//go:build windows

package commandiface

import (
	"os/exec"
	"strings"
)

func defaultWSLHasDistro() bool {
	out, err := exec.Command("wsl.exe", "-l", "-q").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\x00", "")) != ""
}
