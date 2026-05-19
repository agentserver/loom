// Package e2eworkspace centralizes the side-effects of the fixed
// /tmp/multi-agent-driver-first-e2e workspace so several dev/tmp tools can
// share one source of truth for binary builds, runtime config migration, and
// slave container lifecycle.
//
// Design contract:
//   - RuntimeRoot is intentionally fixed: it corresponds to one persistent
//     agentserver workspace (registered credentials, sqlite, journals).
//     Branches share it serially.
//   - Builds always come from the invoking process's module root (resolved via
//     `go env GOMOD`), so switching worktrees and re-running picks up the new
//     code automatically.
package e2eworkspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	RuntimeRoot     = "/tmp/multi-agent-driver-first-e2e"
	RuntimeImage    = "multi-agent-e2e-runtime:latest"
	SlaveContainerA = "ma-e2e-slave-a"
	SlaveContainerB = "ma-e2e-slave-b"
	SlaveWorkdirA   = "slave-a"
	SlaveWorkdirB   = "slave-b"
)

// SlaveDirs returns the workspace subdirectory names of all known slaves.
// Keep in sync with the (slave_a, slave_b) pair the configs were registered
// under; adding a third slave means updating both this list and the
// registered display names.
func SlaveDirs() []string { return []string{SlaveWorkdirA, SlaveWorkdirB} }

// SlaveContainers returns the docker container names this package manages,
// paired with the workspace subdirectory each one mounts as cwd inside /e2e.
func SlaveContainers() []struct{ Name, Workdir string } {
	return []struct{ Name, Workdir string }{
		{SlaveContainerA, SlaveWorkdirA},
		{SlaveContainerB, SlaveWorkdirB},
	}
}

// FindModuleRoot returns the directory containing the current go.mod via
// `go env GOMOD`. Reflects the invoker's CWD, not a path baked into a stale
// binary built from another worktree.
func FindModuleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	goMod := strings.TrimSpace(string(out))
	if goMod == "" || goMod == "/dev/null" {
		return "", fmt.Errorf("not inside a Go module")
	}
	return filepath.Dir(goMod), nil
}

// BuildBinaries rebuilds the slave and driver binaries from moduleRoot into
// RuntimeRoot/bin/. Idempotent — overwrites the existing files.
func BuildBinaries(ctx context.Context, moduleRoot string, out io.Writer) error {
	binDir := filepath.Join(RuntimeRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("mkdir bin: %w", err)
	}
	targets := []struct{ Name, Pkg string }{
		{"driver-agent", "./cmd/driver-agent"},
		{"slave-agent", "./cmd/slave-agent"},
	}
	for _, t := range targets {
		dst := filepath.Join(binDir, t.Name)
		cmd := exec.CommandContext(ctx, "go", "build", "-o", dst, t.Pkg)
		cmd.Dir = moduleRoot
		cmd.Stderr = out
		cmd.Stdout = out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", t.Name, err)
		}
	}
	return nil
}

// MigrateRuntimeConfigs rewrites any `- build_mcp` skill line in a slave's
// persistent config.yaml to `- register_mcp`. Safe to run unconditionally:
// no-op when the line is already migrated or absent.
//
// Why this exists: configs under RuntimeRoot/<slave>/config.yaml are
// agentserver workspace state, not template files. They were generated
// before the build_mcp → register_mcp refactor and survive across branches,
// so a tool entering a freshly-checked-out worktree must reconcile them
// before the slave-agent (which no longer routes build_mcp) starts.
func MigrateRuntimeConfigs(out io.Writer) error {
	for _, dir := range SlaveDirs() {
		path := filepath.Join(RuntimeRoot, dir, "config.yaml")
		b, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		orig := string(b)
		if !strings.Contains(orig, "- build_mcp") {
			continue
		}
		var updated string
		if strings.Contains(orig, "- register_mcp") {
			// Both present — drop only the build_mcp line so we don't
			// double-register the skill.
			updated = dropLineContaining(orig, "- build_mcp")
		} else {
			updated = strings.Replace(orig, "- build_mcp", "- register_mcp", 1)
		}
		if updated == orig {
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(updated), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return fmt.Errorf("rename %s: %w", tmp, err)
		}
		fmt.Fprintln(out, "MIGRATED_CONFIG="+path)
	}
	return nil
}

func dropLineContaining(s, substr string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.Contains(line, substr) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// StopSlaveContainer removes the named container if it exists. Idempotent:
// no error when the container is absent.
func StopSlaveContainer(name string, out io.Writer) error {
	cmd := exec.Command("docker", "rm", "-f", name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run() // ignore exit code; rm -f returns nonzero when absent
	fmt.Fprintln(out, "STOPPED="+name)
	return nil
}

// StartSlaveContainer launches a detached slave container that mounts
// RuntimeRoot at /e2e and runs the in-container slave-agent against the
// given workdirName. Caller is responsible for stopping any existing
// instance first if a fresh process is required.
func StartSlaveContainer(name, workdirName string, out io.Writer) error {
	args := []string{
		"run", "-d", "--name", name,
		"--network", "host",
		"-v", RuntimeRoot + ":/e2e",
		"-v", "/root/.zshrc:/root/.zshrc:ro",
		RuntimeImage,
		"zsh", "-lc",
		fmt.Sprintf("cd /e2e/%s && /e2e/bin/slave-agent config.yaml", workdirName),
	}
	cmd := exec.Command("docker", args...)
	cmd.Stderr = out
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	fmt.Fprintln(out, "STARTED="+name)
	return nil
}

// RestartSlaveContainer = StopSlaveContainer + StartSlaveContainer, with a
// brief settle delay so the slave can register/announce before the caller
// queries agentserver.
func RestartSlaveContainer(name, workdirName string, out io.Writer) error {
	if err := StopSlaveContainer(name, out); err != nil {
		return err
	}
	if err := StartSlaveContainer(name, workdirName, out); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	return nil
}

// Status prints a short summary: which binaries exist and which slave
// containers are running. Useful as a manual-testing sanity check.
func Status(out io.Writer) {
	binDir := filepath.Join(RuntimeRoot, "bin")
	fmt.Fprintln(out, "RUNTIME_ROOT="+RuntimeRoot)
	for _, name := range []string{"driver-agent", "slave-agent"} {
		path := filepath.Join(binDir, name)
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(out, "BIN %-13s MISSING\n", name)
			continue
		}
		fmt.Fprintf(out, "BIN %-13s %s  (%d bytes)\n", name, info.ModTime().Format("2006-01-02 15:04"), info.Size())
	}
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", "name="+SlaveContainerA,
		"--filter", "name="+SlaveContainerB,
		"--format", "CONTAINER {{.Names}} {{.Status}}")
	cmd.Stdout = out
	cmd.Stderr = out
	_ = cmd.Run()
}
