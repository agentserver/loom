//go:build windows

package commandiface

import (
	"os/exec"
)

func defaultWSLHasDistro() bool {
	out, err := exec.Command("wsl.exe", "-l", "-q").Output()
	if err != nil {
		return false
	}
	return wslListOutputHasDistro(out)
}
