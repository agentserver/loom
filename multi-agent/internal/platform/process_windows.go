//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

const stillActive uint32 = 259

func TerminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}

func TerminatePID(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func KillPID(pid int) error {
	return TerminatePID(pid)
}

func ProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
