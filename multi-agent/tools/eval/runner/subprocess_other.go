//go:build !linux

package main

import "os/exec"

// setupProcGroup is a no-op on non-Linux platforms. The runner falls back to
// CommandContext-driven leaf-only kills; spec §6 + plan.md risk section
// document the gap.
func setupProcGroup(_ *exec.Cmd) {}

// killGroup on non-Linux drops to the no-op fallback; the deferred
// cmd.Process.Kill triggered by CommandContext already covers the leaf.
// Tests that assert process-group semantics are tagged !windows.
func killGroup(_ int) {}
