//go:build !windows

package platform

import (
	"errors"
	"os"
	"syscall"
)

func TerminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Signal(syscall.SIGTERM)
}

func TerminatePID(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	err := syscall.Kill(pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func KillPID(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	err := syscall.Kill(pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func ProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) != syscall.ESRCH
}
