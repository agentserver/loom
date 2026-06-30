//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// setupProcGroup gives the subprocess its own process group so a later
// killGroup(pid) signals every descendant — bash oracles that fork helpers
// (jq, awk, helper scripts) leave no zombies. Security §7(a) lifecycle.
func setupProcGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killGroup sends SIGKILL to the entire process group rooted at pid. The
// negative-PID convention is the POSIX-portable way to express
// "everyone in the group". ESRCH (group already gone) is ignored — that's
// the success case, not an error.
func killGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
