# Windows Driver Slave Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `driver-agent` and `slave-agent` build and run on Windows, with Windows slaves advertising native PowerShell, real Bash only when present, and platform/command-interface metadata in capability cards.

**Architecture:** Isolate Unix-only locks, process termination, shutdown signals, and humanloop IPC behind small build-tagged helpers. Add a native PowerShell executor plus command-interface detection, publish that metadata through agentserver cards, and teach driver tools/skills to route shell work by live card data instead of assuming Bash. Windows deployment uses PowerShell installers and foreground-first registration so credentials stay operator-managed and never enter the repo.

**Tech Stack:** Go 1.26.2, `golang.org/x/sys/windows`, agentserver SDK v0.48.1, PowerShell 5.1+, GitHub Actions Windows runners, SSH-to-Windows smoke validation.

---

## Source Spec

Implement the design in:

- `docs/superpowers/specs/2026-06-08-windows-driver-slave-support-design.md`

Validation host:

```text
9.0.16.110
```

Validation access uses SSH with an operator-provided Windows account. Do not store, echo, commit, or print passwords, observer API keys, agentserver proxy tokens, tunnel tokens, per-agent tokens, or registry tokens. Commands may identify the host and username only.

## Scope Check

This plan covers several coupled subsystems because Windows support must build, advertise capabilities, accept tasks, and be understandable by project skills in one release. Execute tasks in order; each task has a commit boundary and a verification command.

## File Structure

Create or modify these files:

- `multi-agent/internal/platform/filelock.go`: shared lock type and `ErrLocked`.
- `multi-agent/internal/platform/filelock_unix.go`: `syscall.Flock` implementation for Unix.
- `multi-agent/internal/platform/filelock_windows.go`: Windows byte-range lock implementation using `golang.org/x/sys/windows`.
- `multi-agent/internal/platform/process_unix.go`: Unix graceful process termination helpers.
- `multi-agent/internal/platform/process_windows.go`: Windows process termination and liveness helpers.
- `multi-agent/internal/platform/signals_unix.go`: Unix shutdown signal list.
- `multi-agent/internal/platform/signals_windows.go`: Windows shutdown signal list.
- `multi-agent/internal/platform/filelock_test.go`: lock contention tests.
- `multi-agent/internal/platform/process_test.go`: process liveness and compile coverage tests.
- `multi-agent/internal/executor/chat_resume.go`: replace direct `syscall.Flock` use.
- `multi-agent/cmd/slave-agent/main.go`: replace direct locks, POSIX signals, and POSIX PID termination.
- `multi-agent/internal/humanloop/ipc.go`: endpoint-aware IPC common code.
- `multi-agent/internal/humanloop/ipc_unix.go`: Unix socket listener factory.
- `multi-agent/internal/humanloop/ipc_windows.go`: loopback TCP listener factory.
- `multi-agent/internal/humanloop/ipc_test.go`: endpoint round-trip tests.
- `multi-agent/internal/humanloop/server.go`: use endpoint argument instead of raw socket path.
- `multi-agent/cmd/slave-agent/humanloop_subcmd.go`: parse endpoint argument.
- `multi-agent/pkg/agentbackend/claude/executor.go`: use endpoint-aware IPC and platform process termination.
- `multi-agent/pkg/agentbackend/claude/executor_test.go`: update humanloop hook assertions.
- `multi-agent/pkg/agentbackend/codex/executor.go`: use endpoint-aware IPC, TOML-safe endpoint args, and platform process termination.
- `multi-agent/pkg/agentbackend/codex/executor_test.go`: update humanloop hook assertions.
- `multi-agent/internal/executor/powershell.go`: native PowerShell executor.
- `multi-agent/internal/executor/powershell_test.go`: PowerShell executor tests.
- `multi-agent/internal/commandiface/interfaces.go`: platform and command-interface data model.
- `multi-agent/internal/commandiface/detect.go`: runtime detection with injectable probes.
- `multi-agent/internal/commandiface/detect_unix.go`: Unix defaults.
- `multi-agent/internal/commandiface/detect_windows.go`: Windows PowerShell, Git Bash, and WSL Bash detection.
- `multi-agent/internal/commandiface/detect_test.go`: deterministic detection tests.
- `multi-agent/internal/tunnel/tunnel.go`: publish platform and command interface card fields.
- `multi-agent/internal/tunnel/tunnel_test.go`: card field tests.
- `multi-agent/cmd/driver-agent/main.go`: publish driver platform metadata.
- `multi-agent/internal/driver/agent_card.go`: parsed card helper for skills, platform, and command interfaces.
- `multi-agent/internal/driver/agent_card_test.go`: parser compatibility tests.
- `multi-agent/internal/driver/tools.go`: include platform and interfaces in `list_agents`; mark `powershell` as JSON-prompt skill.
- `multi-agent/internal/driver/slave_tools.go`: add PowerShell and default shell tools; harden Bash selection.
- `multi-agent/internal/driver/slave_tools_test.go`: shell routing tests.
- `multi-agent/internal/capabilitydoc/doc.go`: include command interfaces and scan `powershell`, `pwsh`, `bash`.
- `multi-agent/internal/capabilitydoc/doc_test.go`: capability document coverage.
- `multi-agent/cmd/slave-agent/capabilities.go`: normalize advertised skills and command interfaces before publishing/routing.
- `multi-agent/cmd/slave-agent/capabilities_test.go`: slave skill normalization tests.
- `multi-agent/deploy/windows/driver/install.ps1`: Windows driver installer.
- `multi-agent/deploy/windows/driver/config.yaml.template`: Windows driver config template.
- `multi-agent/deploy/windows/driver/codex-mcp.toml.template`: Codex MCP config template.
- `multi-agent/deploy/windows/driver/mcp.json.template`: Claude MCP config template.
- `multi-agent/deploy/windows/driver/README.md`: Windows driver setup docs.
- `multi-agent/deploy/windows/slave/install.ps1`: Windows slave installer.
- `multi-agent/deploy/windows/slave/config.yaml.template`: Windows slave config template.
- `multi-agent/deploy/windows/slave/slave-agent-service.ps1`: Windows service wrapper.
- `multi-agent/deploy/windows/slave/README.md`: Windows slave setup docs.
- `skills/multiagent/SKILL.md`: capability inspection and shell-selection guidance.
- `skills/multiagent/references/driver-tools.md`: new tool schemas and card fields.
- `skills/multiagent/references/slave-skills.md`: PowerShell skill and Bash semantics.
- `skills/multiagent/references/orchestration-patterns.md`: Windows shell examples and file-transfer warning.
- `multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md`: Codex driver prompt updates.
- `.github/workflows/multi-agent.yml`: Windows CI and cross-compile checks.
- `multi-agent/tests/prod_test/README.md`: ignored-runtime Windows validation instructions and binary rebuild commands.

## Task 1: Add Cross-Platform Runtime Helpers

**Files:**
- Create: `multi-agent/internal/platform/filelock.go`
- Create: `multi-agent/internal/platform/filelock_unix.go`
- Create: `multi-agent/internal/platform/filelock_windows.go`
- Create: `multi-agent/internal/platform/process_unix.go`
- Create: `multi-agent/internal/platform/process_windows.go`
- Create: `multi-agent/internal/platform/signals_unix.go`
- Create: `multi-agent/internal/platform/signals_windows.go`
- Create: `multi-agent/internal/platform/filelock_test.go`
- Create: `multi-agent/internal/platform/process_test.go`
- Modify: `multi-agent/internal/executor/chat_resume.go`
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Modify: `multi-agent/pkg/agentbackend/claude/executor.go`
- Modify: `multi-agent/pkg/agentbackend/codex/executor.go`

- [ ] **Step 1: Write failing file lock tests**

Create `multi-agent/internal/platform/filelock_test.go`:

```go
package platform

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLockRejectsConcurrentHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	first, err := TryLock(path)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	defer first.Unlock()

	second, err := TryLock(path)
	if err == nil {
		second.Unlock()
		t.Fatal("second TryLock succeeded, want locked error")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock error = %v, want ErrLocked", err)
	}
}

func TestTryLockCanReacquireAfterUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.lock")
	first, err := TryLock(path)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	if err := first.Unlock(); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}

	second, err := TryLock(path)
	if err != nil {
		t.Fatalf("second TryLock after unlock: %v", err)
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
}
```

- [ ] **Step 2: Write process helper tests**

Create `multi-agent/internal/platform/process_test.go`:

```go
package platform

import (
	"os"
	"testing"
)

func TestProcessExistsCurrentPID(t *testing.T) {
	if !ProcessExists(os.Getpid()) {
		t.Fatalf("ProcessExists(%d) = false, want true", os.Getpid())
	}
}

func TestProcessExistsInvalidPID(t *testing.T) {
	if ProcessExists(-1) {
		t.Fatal("ProcessExists(-1) = true, want false")
	}
}

func TestShutdownSignalsIsNonEmpty(t *testing.T) {
	if len(ShutdownSignals()) == 0 {
		t.Fatal("ShutdownSignals returned no signals")
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```bash
cd multi-agent
go test ./internal/platform -count=1
```

Expected: FAIL because `TryLock`, `ErrLocked`, `ProcessExists`, and `ShutdownSignals` do not exist.

- [ ] **Step 4: Add common lock type**

Create `multi-agent/internal/platform/filelock.go`:

```go
package platform

import (
	"errors"
	"os"
)

var ErrLocked = errors.New("file lock already held")

type FileLock struct {
	Path string
	file *os.File
}

func (l *FileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
```

- [ ] **Step 5: Add Unix lock implementation**

Create `multi-agent/internal/platform/filelock_unix.go`:

```go
//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func TryLock(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrLocked, path)
	}
	return &FileLock{Path: path, file: f}, nil
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
```

- [ ] **Step 6: Add Windows lock implementation**

Create `multi-agent/internal/platform/filelock_windows.go`:

```go
//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func TryLock(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, err
	}
	return &FileLock{Path: path, file: f}, nil
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
```

- [ ] **Step 7: Add process and signal helpers**

Create `multi-agent/internal/platform/process_unix.go`:

```go
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
```

Create `multi-agent/internal/platform/process_windows.go`:

```go
//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

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
	return code == windows.STILL_ACTIVE
}
```

Create `multi-agent/internal/platform/signals_unix.go`:

```go
//go:build !windows

package platform

import (
	"os"
	"syscall"
)

func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
```

Create `multi-agent/internal/platform/signals_windows.go`:

```go
//go:build windows

package platform

import "os"

func ShutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
```

- [ ] **Step 8: Replace `chat_resume` flock**

In `multi-agent/internal/executor/chat_resume.go`, remove `syscall` from imports, add:

```go
	"errors"

	"github.com/yourorg/multi-agent/internal/platform"
```

Replace the `os.OpenFile` and `syscall.Flock` block with:

```go
	lock, err := platform.TryLock(lockPath)
	if err != nil {
		if errors.Is(err, platform.ErrLocked) {
			return Result{}, fmt.Errorf("chat_resume: session busy (flock=%s)", lockPath)
		}
		return Result{}, fmt.Errorf("chat_resume: open lock: %w", err)
	}
	defer lock.Unlock()
```

- [ ] **Step 9: Replace slave single-instance lock**

In `multi-agent/cmd/slave-agent/main.go`, remove the direct `syscall` import and add:

```go
	"errors"

	"github.com/yourorg/multi-agent/internal/platform"
```

Change `acquireInstanceLock` to return `*platform.FileLock`:

```go
func acquireInstanceLock() (*platform.FileLock, error) {
	lockPath, err := filepath.Abs("slave-agent.lock")
	if err != nil {
		return nil, fmt.Errorf("resolve lock path: %w", err)
	}
	lock, err := platform.TryLock(lockPath)
	if err != nil {
		holderPid := readHolderPid(lockPath)
		if !errors.Is(err, platform.ErrLocked) {
			return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
		}
		if os.Getenv("INVOCATION_ID") == "" {
			return nil, fmt.Errorf("another slave-agent is already running in this install dir "+
				"(lock=%s holder_pid=%d); refusing to start. "+
				"If this is a stale lock, stop the running slave-agent for this install dir and remove %s",
				lockPath, holderPid, lockPath)
		}
		log.Printf("acquireInstanceLock: lock held by pid=%d, taking over (managed start)", holderPid)
		if err := takeOverLock(lockPath, holderPid); err != nil {
			return nil, err
		}
		lock, err = platform.TryLock(lockPath)
		if err != nil {
			return nil, fmt.Errorf("could not acquire %s after terminating pid=%d: %w", lockPath, holderPid, err)
		}
	}
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		_ = lock.Unlock()
		return nil, fmt.Errorf("write lock holder pid: %w", err)
	}
	return lock, nil
}
```

Replace `takeOverLock` with:

```go
func takeOverLock(lockPath string, holderPid int) error {
	if holderPid > 0 && holderPid != os.Getpid() {
		if err := platform.TerminatePID(holderPid); err != nil {
			log.Printf("acquireInstanceLock: terminate pid=%d: %v (continuing)", holderPid, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if !platform.ProcessExists(holderPid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if platform.ProcessExists(holderPid) {
			log.Printf("acquireInstanceLock: pid=%d still alive after graceful terminate, killing", holderPid)
			_ = platform.KillPID(holderPid)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lock, err := platform.TryLock(lockPath)
		if err == nil {
			return lock.Unlock()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("could not acquire %s after terminating pid=%d", lockPath, holderPid)
}
```

Replace signal setup:

```go
	ctx, cancel := signal.NotifyContext(context.Background(), platform.ShutdownSignals()...)
```

- [ ] **Step 10: Replace backend SIGTERM calls**

In `multi-agent/pkg/agentbackend/claude/executor.go` and `multi-agent/pkg/agentbackend/codex/executor.go`, remove `syscall` imports, add:

```go
	"github.com/yourorg/multi-agent/internal/platform"
```

Replace:

```go
_ = cmd.Process.Signal(syscall.SIGTERM)
```

with:

```go
_ = platform.TerminateProcess(cmd.Process)
```

- [ ] **Step 11: Run focused tests**

Run:

```bash
cd multi-agent
go test ./internal/platform ./internal/executor ./cmd/slave-agent ./pkg/agentbackend/claude ./pkg/agentbackend/codex -count=1
```

Expected: PASS on Linux.

- [ ] **Step 12: Commit**

```bash
git add multi-agent/internal/platform multi-agent/internal/executor/chat_resume.go multi-agent/cmd/slave-agent/main.go multi-agent/pkg/agentbackend/claude/executor.go multi-agent/pkg/agentbackend/codex/executor.go
git commit -m "feat: add cross-platform runtime helpers"
```

## Task 2: Make Humanloop IPC Endpoint-Aware

**Files:**
- Modify: `multi-agent/internal/humanloop/ipc.go`
- Create: `multi-agent/internal/humanloop/ipc_unix.go`
- Create: `multi-agent/internal/humanloop/ipc_windows.go`
- Modify: `multi-agent/internal/humanloop/ipc_test.go`
- Modify: `multi-agent/internal/humanloop/server.go`
- Modify: `multi-agent/cmd/slave-agent/humanloop_subcmd.go`
- Modify: `multi-agent/pkg/agentbackend/claude/executor.go`
- Modify: `multi-agent/pkg/agentbackend/claude/executor_test.go`
- Modify: `multi-agent/pkg/agentbackend/codex/executor.go`
- Modify: `multi-agent/pkg/agentbackend/codex/executor_test.go`

- [ ] **Step 1: Write failing endpoint tests**

Replace `multi-agent/internal/humanloop/ipc_test.go` with:

```go
package humanloop

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEndpointArgRoundTrip(t *testing.T) {
	in := Endpoint{Network: "tcp", Address: "127.0.0.1:49152"}
	arg := EndpointArg(in)
	got, err := ParseEndpointArg(arg)
	if err != nil {
		t.Fatalf("ParseEndpointArg: %v", err)
	}
	if got != in {
		t.Fatalf("endpoint = %+v, want %+v", got, in)
	}
}

func TestParseEndpointArgAcceptsLegacyUnixPath(t *testing.T) {
	got, err := ParseEndpointArg("/tmp/hl.sock")
	if err != nil {
		t.Fatalf("ParseEndpointArg legacy path: %v", err)
	}
	if got.Network != "unix" || got.Address != "/tmp/hl.sock" {
		t.Fatalf("legacy endpoint = %+v", got)
	}
}

func TestIPCRoundTrip(t *testing.T) {
	srv, ep, err := ListenIPC(t.TempDir())
	if err != nil {
		t.Fatalf("ListenIPC: %v", err)
	}
	defer srv.Close()
	if ep.Network == "" || ep.Address == "" {
		t.Fatalf("empty endpoint: %+v", ep)
	}

	received := make(chan Payload, 1)
	go func() {
		p, err := srv.Receive()
		if err != nil {
			t.Errorf("Receive: %v", err)
			return
		}
		received <- p
	}()

	client, err := DialIPC(ep)
	if err != nil {
		t.Fatalf("DialIPC: %v", err)
	}
	defer client.Close()

	in := Payload{Kind: "ask_user", Question: "are we good?", Options: []string{"yes", "no"}}
	if err := client.Send(in); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-received:
		gj, _ := json.Marshal(got)
		ij, _ := json.Marshal(in)
		if string(gj) != string(ij) {
			t.Errorf("payload mismatch:\nwant %s\ngot  %s", ij, gj)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for IPC payload")
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd multi-agent
go test ./internal/humanloop -run 'Test(EndpointArgRoundTrip|ParseEndpointArgAcceptsLegacyUnixPath|IPCRoundTrip)' -count=1
```

Expected: FAIL because `Endpoint`, `EndpointArg`, and the new `ListenIPC`/`DialIPC` signatures do not exist.

- [ ] **Step 3: Replace common IPC code**

In `multi-agent/internal/humanloop/ipc.go`, keep `Payload`, `IPCServer`, `IPCClient`, `Receive`, `Send`, and `Close`, but replace path-specific APIs with:

```go
type Endpoint struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

func EndpointArg(ep Endpoint) string {
	b, _ := json.Marshal(ep)
	return string(b)
}

func ParseEndpointArg(arg string) (Endpoint, error) {
	var ep Endpoint
	if err := json.Unmarshal([]byte(arg), &ep); err == nil && ep.Network != "" && ep.Address != "" {
		return ep, nil
	}
	if arg == "" {
		return Endpoint{}, fmt.Errorf("humanloop endpoint is required")
	}
	return Endpoint{Network: "unix", Address: arg}, nil
}

func ListenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	return listenIPC(baseDir)
}

func DialIPC(ep Endpoint) (*IPCClient, error) {
	if ep.Network == "" || ep.Address == "" {
		return nil, fmt.Errorf("humanloop endpoint is incomplete")
	}
	c, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		return nil, fmt.Errorf("humanloop dial %s %s: %w", ep.Network, ep.Address, err)
	}
	return &IPCClient{conn: c}, nil
}
```

Update `IPCServer`:

```go
type IPCServer struct {
	ln      net.Listener
	cleanup func()
}

func (s *IPCServer) Close() error {
	err := s.ln.Close()
	if s.cleanup != nil {
		s.cleanup()
	}
	return err
}
```

- [ ] **Step 4: Add Unix and Windows listener factories**

Create `multi-agent/internal/humanloop/ipc_unix.go`:

```go
//go:build !windows

package humanloop

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

func listenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, Endpoint{}, err
	}
	path := filepath.Join(baseDir, "hl.sock")
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, Endpoint{}, fmt.Errorf("humanloop listen unix %s: %w", path, err)
	}
	return &IPCServer{ln: ln, cleanup: func() { _ = os.Remove(path) }}, Endpoint{Network: "unix", Address: path}, nil
}
```

Create `multi-agent/internal/humanloop/ipc_windows.go`:

```go
//go:build windows

package humanloop

import (
	"fmt"
	"net"
)

func listenIPC(baseDir string) (*IPCServer, Endpoint, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, Endpoint{}, fmt.Errorf("humanloop listen tcp loopback: %w", err)
	}
	return &IPCServer{ln: ln}, Endpoint{Network: "tcp", Address: ln.Addr().String()}, nil
}
```

- [ ] **Step 5: Update humanloop MCP server argument handling**

In `multi-agent/internal/humanloop/server.go`, rename `ipcSocket` parameters to `endpointArg`, parse before calls, and pass the `Endpoint` to IPC:

```go
func ServeStdio(r io.Reader, w io.Writer, endpointArg string, max int) error {
	ep, err := ParseEndpointArg(endpointArg)
	if err != nil {
		return err
	}
	// existing scanner loop
	text := handleCall(req.Params, ep, &used, max)
}
```

Update `handleCall` in `multi-agent/internal/humanloop/tools.go` to accept `Endpoint`:

```go
func handleCall(params json.RawMessage, ep Endpoint, used *int, max int) string
```

and replace `DialIPC(ipcSocket)` with `DialIPC(ep)`.

In `multi-agent/cmd/slave-agent/humanloop_subcmd.go`, update usage text:

```go
return fmt.Errorf("usage: slave-agent humanloop-mcp <endpoint-json-or-legacy-socket-path> <max-questions>")
```

- [ ] **Step 6: Update Claude backend IPC usage**

In `multi-agent/pkg/agentbackend/claude/executor.go`, replace `sockPath` construction and listener setup with:

```go
	srv, ep, err := humanloop.ListenIPC(sockDir)
	if err != nil {
		return agentbackend.Result{}, err
	}
	defer srv.Close()
	if e.socketHookForTest != nil {
		go e.socketHookForTest(humanloop.EndpointArg(ep))
	}
```

Change `writeHumanloopMCPConfig` signature and args:

```go
func writeHumanloopMCPConfig(path, binSelf string, ep humanloop.Endpoint, max int) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"loom_humanloop": map[string]any{
				"command": binSelf,
				"args":    []string{"humanloop-mcp", humanloop.EndpointArg(ep), strconv.Itoa(max)},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

- [ ] **Step 7: Update Codex backend IPC usage and TOML quoting**

In `multi-agent/pkg/agentbackend/codex/executor.go`, replace `sockPath` setup with the same `ListenIPC` pattern. Add:

```go
func tomlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
```

Build `mcpArgs` with TOML-safe strings:

```go
	endpointArg := humanloop.EndpointArg(ep)
	mcpArgs := []string{
		"-c", fmt.Sprintf("mcp_servers.loom_humanloop.command=%s", tomlString(e.binSelf)),
		"-c", fmt.Sprintf("mcp_servers.loom_humanloop.args=[%s,%s,%s]",
			tomlString("humanloop-mcp"), tomlString(endpointArg), tomlString(strconv.Itoa(e.maxQuestions))),
	}
```

- [ ] **Step 8: Run focused tests**

Run:

```bash
cd multi-agent
go test ./internal/humanloop ./pkg/agentbackend/claude ./pkg/agentbackend/codex -count=1
```

Expected: PASS on Linux.

- [ ] **Step 9: Commit**

```bash
git add multi-agent/internal/humanloop multi-agent/cmd/slave-agent/humanloop_subcmd.go multi-agent/pkg/agentbackend/claude multi-agent/pkg/agentbackend/codex
git commit -m "feat: make humanloop ipc cross-platform"
```

## Task 3: Add PowerShell Executor

**Files:**
- Create: `multi-agent/internal/executor/powershell.go`
- Create: `multi-agent/internal/executor/powershell_test.go`
- Modify: `multi-agent/internal/driver/tools.go`

- [ ] **Step 1: Write failing PowerShell executor tests**

Create `multi-agent/internal/executor/powershell_test.go`:

```go
package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPowerShellExecutorRejectsMissingScript(t *testing.T) {
	exec := NewPowerShellExecutor(PowerShellConfig{WorkDir: t.TempDir(), Bin: "powershell.exe"})
	_, err := exec.Run(context.Background(), Task{Prompt: `{}`}, noopSink{})
	if err == nil || !strings.Contains(err.Error(), "powershell script is required") {
		t.Fatalf("error = %v, want missing script", err)
	}
}

func TestPowerShellCommandArgs(t *testing.T) {
	got := powerShellArgs("Write-Output 'ok'")
	want := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "Write-Output 'ok'"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestPowerShellExecutorRunsScriptWhenAvailable(t *testing.T) {
	bin := "powershell.exe"
	if runtime.GOOS != "windows" {
		var err error
		bin, err = exec.LookPath("pwsh")
		if err != nil {
			bin, err = exec.LookPath("powershell")
		}
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				t.Skip("PowerShell is not installed on this host")
			}
			t.Fatalf("lookpath powershell: %v", err)
		}
	}
	workdir := t.TempDir()
	ps := NewPowerShellExecutor(PowerShellConfig{WorkDir: workdir, Bin: bin})
	res, err := ps.Run(context.Background(), Task{
		ID:     "task-ps",
		Skill:  "powershell",
		Prompt: `{"script":"Write-Output $env:LOOM_TEST_VALUE; Write-Error 'warn'","timeout_sec":10,"env":{"LOOM_TEST_VALUE":"hello-ps"}}`,
	}, noopSink{})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var got PowerShellResult
	if err := json.Unmarshal([]byte(res.Summary), &got); err != nil {
		t.Fatalf("summary is not PowerShellResult JSON: %v\n%s", err, res.Summary)
	}
	if got.ExitCode != 0 || !strings.Contains(got.Stdout, "hello-ps") || got.WorkDir != workdir {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestPowerShellExecutorCreatesWorkDir(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "nested", "work")
	ps := NewPowerShellExecutor(PowerShellConfig{WorkDir: workdir, Bin: "powershell.exe"})
	if ps.cfg.WorkDir != workdir {
		t.Fatalf("workdir = %q", ps.cfg.WorkDir)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd multi-agent
go test ./internal/executor -run PowerShell -count=1
```

Expected: FAIL because `PowerShellConfig`, `PowerShellResult`, `NewPowerShellExecutor`, and `powerShellArgs` do not exist.

- [ ] **Step 3: Add PowerShell executor**

Create `multi-agent/internal/executor/powershell.go`:

```go
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

type PowerShellConfig struct {
	WorkDir string
	Bin     string
}

type PowerShellExecutor struct {
	cfg PowerShellConfig
}

type PowerShellRequest struct {
	Script     string            `json:"script"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type PowerShellResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	WorkDir  string `json:"workdir"`
}

func NewPowerShellExecutor(cfg PowerShellConfig) *PowerShellExecutor {
	return &PowerShellExecutor{cfg: cfg}
}

func powerShellArgs(script string) []string {
	return []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script}
}

func (e *PowerShellExecutor) resolveBin() (string, error) {
	if e.cfg.Bin != "" {
		return e.cfg.Bin, nil
	}
	if runtime.GOOS == "windows" {
		return "powershell.exe", nil
	}
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p, nil
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("powershell binary not found")
}

func (e *PowerShellExecutor) Run(ctx context.Context, t Task, sink Sink) (Result, error) {
	defer sink.Close()
	var req PowerShellRequest
	if err := json.Unmarshal([]byte(t.Prompt), &req); err != nil {
		return Result{}, fmt.Errorf("powershell prompt must be JSON: %w", err)
	}
	if req.Script == "" {
		return Result{}, fmt.Errorf("powershell script is required")
	}
	workdir := e.cfg.WorkDir
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return Result{}, err
	}
	bin, err := e.resolveBin()
	if err != nil {
		return Result{}, err
	}
	runCtx := ctx
	cancel := func() {}
	if req.TimeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutSec)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, bin, powerShellArgs(req.Script)...)
	cmd.Dir = workdir
	cmd.Env = cmd.Environ()
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	result := PowerShellResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		WorkDir:  workdir,
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return Result{}, marshalErr
	}
	sink.Write("chunk", string(body))
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return Result{Summary: string(body)}, fmt.Errorf("powershell timeout")
		}
		return Result{Summary: string(body)}, fmt.Errorf("powershell exit code %d", exitCode)
	}
	return Result{Summary: string(body)}, nil
}
```

- [ ] **Step 4: Mark PowerShell as JSON-prompt skill**

In `multi-agent/internal/driver/tools.go`, update `jsonPromptSkill`:

```go
	case "mcp", "bash", "powershell", "register_mcp", "unregister_mcp", "claude_permissions", "permissions", "file", "chat_resume":
		return true
```

- [ ] **Step 5: Run focused tests**

Run:

```bash
cd multi-agent
go test ./internal/executor ./internal/driver -run 'PowerShell|JsonPrompt|SubmitTask' -count=1
```

Expected: PASS. The PowerShell run test may skip on Linux hosts without PowerShell.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/internal/executor/powershell.go multi-agent/internal/executor/powershell_test.go multi-agent/internal/driver/tools.go
git commit -m "feat: add powershell task executor"
```

## Task 4: Detect Platform Command Interfaces and Publish Cards

**Files:**
- Create: `multi-agent/internal/commandiface/interfaces.go`
- Create: `multi-agent/internal/commandiface/detect.go`
- Create: `multi-agent/internal/commandiface/detect_unix.go`
- Create: `multi-agent/internal/commandiface/detect_windows.go`
- Create: `multi-agent/internal/commandiface/detect_test.go`
- Modify: `multi-agent/internal/tunnel/tunnel.go`
- Modify: `multi-agent/internal/tunnel/tunnel_test.go`
- Modify: `multi-agent/cmd/driver-agent/main.go`

- [ ] **Step 1: Write failing command-interface detection tests**

Create `multi-agent/internal/commandiface/detect_test.go`:

```go
package commandiface

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"
)

func TestBuildWindowsPowerShellDefaultNoBashWithoutRuntime(t *testing.T) {
	d := Detector{
		GOOS:   "windows",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			switch name {
			case "powershell.exe":
				return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, nil
			default:
				return "", exec.ErrNotFound
			}
		},
		WSLHasDistro: func() bool { return false },
	}
	got := d.Build([]string{"chat", "bash", "powershell", "file"})
	if !reflect.DeepEqual(got.Skills, []string{"chat", "powershell", "file"}) {
		t.Fatalf("skills = %#v", got.Skills)
	}
	if len(got.CommandInterfaces) != 1 {
		t.Fatalf("interfaces = %#v", got.CommandInterfaces)
	}
	ci := got.CommandInterfaces[0]
	if ci.Skill != "powershell" || ci.Kind != "powershell" || !ci.Default {
		t.Fatalf("interface = %+v", ci)
	}
}

func TestBuildWindowsAddsGitBashWhenPresent(t *testing.T) {
	d := Detector{
		GOOS:   "windows",
		GOARCH: "amd64",
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
	got := d.Build([]string{"bash", "powershell"})
	if !hasSkill(got.Skills, "bash") {
		t.Fatalf("skills = %#v, want bash", got.Skills)
	}
	if len(got.CommandInterfaces) != 2 {
		t.Fatalf("interfaces = %#v", got.CommandInterfaces)
	}
	if got.CommandInterfaces[0].Kind != "powershell" || !got.CommandInterfaces[0].Default {
		t.Fatalf("default interface = %+v", got.CommandInterfaces[0])
	}
	if got.CommandInterfaces[1].Kind != "bash" || got.CommandInterfaces[1].Default {
		t.Fatalf("bash interface = %+v", got.CommandInterfaces[1])
	}
}

func TestBuildUnixBashDefault(t *testing.T) {
	d := Detector{
		GOOS:   "linux",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			if name == "bash" {
				return "/bin/bash", nil
			}
			return "", exec.ErrNotFound
		},
	}
	got := d.Build([]string{"chat", "bash"})
	if !reflect.DeepEqual(got.Skills, []string{"chat", "bash"}) {
		t.Fatalf("skills = %#v", got.Skills)
	}
	if len(got.CommandInterfaces) != 1 || got.CommandInterfaces[0].Kind != "bash" || !got.CommandInterfaces[0].Default {
		t.Fatalf("interfaces = %#v", got.CommandInterfaces)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd multi-agent
go test ./internal/commandiface -count=1
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Add command-interface data model**

Create `multi-agent/internal/commandiface/interfaces.go`:

```go
package commandiface

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type CommandInterface struct {
	Skill   string `json:"skill"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
	Default bool   `json:"default"`
}

type Capabilities struct {
	Platform          Platform           `json:"platform"`
	Skills            []string           `json:"skills"`
	CommandInterfaces []CommandInterface `json:"command_interfaces,omitempty"`
}
```

Create `multi-agent/internal/commandiface/detect.go`:

```go
package commandiface

import (
	"os/exec"
	"runtime"
)

type Detector struct {
	GOOS         string
	GOARCH       string
	LookPath     func(string) (string, error)
	WSLHasDistro func() bool
}

func DefaultDetector() Detector {
	return Detector{
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		LookPath:     exec.LookPath,
		WSLHasDistro: defaultWSLHasDistro,
	}
}

func Detect(skills []string) Capabilities {
	return DefaultDetector().Build(skills)
}

func (d Detector) Build(skills []string) Capabilities {
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
		d.WSLHasDistro = func() bool { return false }
	}
	out := Capabilities{Platform: Platform{OS: d.GOOS, Arch: d.GOARCH}}
	for _, skill := range skills {
		if skill == "" || hasSkill(out.Skills, skill) {
			continue
		}
		out.Skills = append(out.Skills, skill)
	}
	switch d.GOOS {
	case "windows":
		out = d.addWindowsShells(out)
	default:
		out = d.addUnixShells(out)
	}
	markDefault(out.CommandInterfaces)
	return out
}

func (d Detector) addWindowsShells(c Capabilities) Capabilities {
	if hasSkill(c.Skills, "powershell") {
		cmd := "powershell.exe"
		if p, err := d.LookPath("powershell.exe"); err == nil {
			cmd = p
		}
		c.CommandInterfaces = append(c.CommandInterfaces, CommandInterface{
			Skill: "powershell", Kind: "powershell", Command: cmd, Default: true,
		})
	}
	if hasSkill(c.Skills, "bash") {
		if p, err := d.LookPath("bash.exe"); err == nil {
			c.CommandInterfaces = append(c.CommandInterfaces, CommandInterface{
				Skill: "bash", Kind: "bash", Command: p,
			})
		} else if d.WSLHasDistro() {
			c.CommandInterfaces = append(c.CommandInterfaces, CommandInterface{
				Skill: "bash", Kind: "bash", Command: "wsl.exe -- bash -lc",
			})
		} else {
			c.Skills = removeSkill(c.Skills, "bash")
		}
	}
	return c
}

func (d Detector) addUnixShells(c Capabilities) Capabilities {
	if hasSkill(c.Skills, "bash") {
		cmd := "/bin/bash"
		if p, err := d.LookPath("bash"); err == nil {
			cmd = p
		}
		c.CommandInterfaces = append(c.CommandInterfaces, CommandInterface{
			Skill: "bash", Kind: "bash", Command: cmd, Default: true,
		})
	}
	if hasSkill(c.Skills, "powershell") {
		for _, name := range []string{"pwsh", "powershell"} {
			if p, err := d.LookPath(name); err == nil {
				c.CommandInterfaces = append(c.CommandInterfaces, CommandInterface{
					Skill: "powershell", Kind: "powershell", Command: p,
				})
				break
			}
		}
	}
	return c
}

func markDefault(interfaces []CommandInterface) {
	found := false
	for i := range interfaces {
		if interfaces[i].Default && !found {
			found = true
			continue
		}
		interfaces[i].Default = false
	}
	if !found && len(interfaces) > 0 {
		interfaces[0].Default = true
	}
}

func hasSkill(skills []string, want string) bool {
	for _, skill := range skills {
		if skill == want {
			return true
		}
	}
	return false
}

func removeSkill(skills []string, remove string) []string {
	out := skills[:0]
	for _, skill := range skills {
		if skill != remove {
			out = append(out, skill)
		}
	}
	return out
}
```

- [ ] **Step 4: Add platform-specific WSL probe**

Create `multi-agent/internal/commandiface/detect_unix.go`:

```go
//go:build !windows

package commandiface

func defaultWSLHasDistro() bool {
	return false
}
```

Create `multi-agent/internal/commandiface/detect_windows.go`:

```go
//go:build windows

package commandiface

import (
	"os/exec"
	"strings"
)

func defaultWSLHasDistro() bool {
	out, err := exec.Command("wsl.exe", "-l", "-q").CombinedOutput()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.Trim(line, "\x00"))
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
```

- [ ] **Step 5: Add card publishing fields to tunnel**

In `multi-agent/internal/tunnel/tunnel.go`, import:

```go
	"runtime"

	"github.com/yourorg/multi-agent/internal/commandiface"
```

Add fields to `Tunnel`:

```go
	platform          commandiface.Platform
	commandInterfaces []commandiface.CommandInterface
```

Add setters:

```go
func (t *Tunnel) SetPlatform(p commandiface.Platform) {
	t.platform = p
}

func (t *Tunnel) SetCommandInterfaces(in []commandiface.CommandInterface) {
	t.commandInterfaces = append([]commandiface.CommandInterface{}, in...)
}
```

In `PublishCard`, add:

```go
	platform := t.platform
	if platform.OS == "" {
		platform = commandiface.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	}
	cardBody["platform"] = platform
	if len(t.commandInterfaces) > 0 {
		cardBody["command_interfaces"] = t.commandInterfaces
	}
```

- [ ] **Step 6: Test card publishing fields**

Append to `multi-agent/internal/tunnel/tunnel_test.go`:

```go
func TestPublishCard_IncludesPlatformAndCommandInterfaces(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Server:      config.Server{URL: srv.URL, Name: "n"},
		Credentials: config.Credentials{ProxyToken: "ptoken"},
		Discovery:   config.Discovery{DisplayName: "dn", Skills: []string{"powershell"}},
	}
	tn := NewWithDeps(cfg, "/tmp/none", nil, Deps{})
	tn.SetPlatform(commandiface.Platform{OS: "windows", Arch: "amd64"})
	tn.SetCommandInterfaces([]commandiface.CommandInterface{{
		Skill: "powershell", Kind: "powershell", Command: "powershell.exe", Default: true,
	}})
	require.NoError(t, tn.PublishCard(context.Background()))
	card := got["card"].(map[string]interface{})
	platform := card["platform"].(map[string]interface{})
	require.Equal(t, "windows", platform["os"])
	require.Equal(t, "amd64", platform["arch"])
	interfaces := card["command_interfaces"].([]interface{})
	first := interfaces[0].(map[string]interface{})
	require.Equal(t, "powershell", first["skill"])
	require.Equal(t, true, first["default"])
}
```

- [ ] **Step 7: Publish driver platform metadata**

In `multi-agent/cmd/driver-agent/main.go`, import `runtime` and add to the driver card:

```go
			"platform": map[string]string{
				"os":   runtime.GOOS,
				"arch": runtime.GOARCH,
			},
```

- [ ] **Step 8: Run focused tests**

Run:

```bash
cd multi-agent
go test ./internal/commandiface ./internal/tunnel ./cmd/driver-agent -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add multi-agent/internal/commandiface multi-agent/internal/tunnel/tunnel.go multi-agent/internal/tunnel/tunnel_test.go multi-agent/cmd/driver-agent/main.go
git commit -m "feat: publish platform command interfaces"
```

## Task 5: Add Driver Card Parsing and Shell Tools

**Files:**
- Create: `multi-agent/internal/driver/agent_card.go`
- Create: `multi-agent/internal/driver/agent_card_test.go`
- Modify: `multi-agent/internal/driver/tools.go`
- Modify: `multi-agent/internal/driver/slave_tools.go`
- Modify: `multi-agent/internal/driver/slave_tools_test.go`

- [ ] **Step 1: Write failing card parser tests**

Create `multi-agent/internal/driver/agent_card_test.go`:

```go
package driver

import (
	"encoding/json"
	"testing"

	"github.com/agentserver/agentserver/pkg/agentsdk"
)

func TestParseAgentCardWithPlatformAndCommandInterfaces(t *testing.T) {
	c := agentsdk.AgentCard{Card: json.RawMessage(`{
		"skills":["chat","powershell"],
		"short_id":"sid",
		"platform":{"os":"windows","arch":"amd64"},
		"command_interfaces":[
			{"skill":"powershell","kind":"powershell","command":"powershell.exe","default":true}
		]
	}`)}
	got := parseAgentCard(c)
	if got.ShortID != "sid" || got.Platform.OS != "windows" || got.Platform.Arch != "amd64" {
		t.Fatalf("parsed card = %+v", got)
	}
	if !got.HasSkill("powershell") || !got.HasCommandKind("powershell") {
		t.Fatalf("missing powershell in %+v", got)
	}
	if got.DefaultCommandInterface().Kind != "powershell" {
		t.Fatalf("default interface = %+v", got.DefaultCommandInterface())
	}
}

func TestParseAgentCardLegacyBashFallback(t *testing.T) {
	c := agentsdk.AgentCard{Card: json.RawMessage(`{"skills":["bash"],"short_id":"sid"}`)}
	got := parseAgentCard(c)
	if !got.HasSkill("bash") {
		t.Fatalf("legacy skills missing bash: %+v", got)
	}
	if !got.SupportsExplicitShell("bash") {
		t.Fatalf("legacy bash card should support explicit bash")
	}
	if got.SupportsExplicitShell("powershell") {
		t.Fatalf("legacy bash card should not support powershell")
	}
}
```

- [ ] **Step 2: Add parsed card helper**

Create `multi-agent/internal/driver/agent_card.go`:

```go
package driver

import (
	"encoding/json"

	"github.com/agentserver/agentserver/pkg/agentsdk"
	"github.com/yourorg/multi-agent/internal/commandiface"
)

type parsedAgentCard struct {
	Skills            []string
	Tools             []string
	MCPTools          json.RawMessage
	Resources         json.RawMessage
	ShortID           string
	Platform          commandiface.Platform
	CommandInterfaces []commandiface.CommandInterface
}

func parseAgentCard(c agentsdk.AgentCard) parsedAgentCard {
	var card struct {
		Skills            []string                         `json:"skills"`
		Tools             []string                         `json:"tools"`
		MCPTools          json.RawMessage                  `json:"mcp_tools"`
		Resources         json.RawMessage                  `json:"resources"`
		ShortID           string                           `json:"short_id"`
		Platform          commandiface.Platform            `json:"platform"`
		CommandInterfaces []commandiface.CommandInterface  `json:"command_interfaces"`
	}
	_ = json.Unmarshal(c.Card, &card)
	return parsedAgentCard{
		Skills:            card.Skills,
		Tools:             card.Tools,
		MCPTools:          card.MCPTools,
		Resources:         card.Resources,
		ShortID:           card.ShortID,
		Platform:          card.Platform,
		CommandInterfaces: card.CommandInterfaces,
	}
}

func (p parsedAgentCard) HasSkill(want string) bool {
	for _, skill := range p.Skills {
		if skill == want {
			return true
		}
	}
	return false
}

func (p parsedAgentCard) HasCommandKind(kind string) bool {
	for _, iface := range p.CommandInterfaces {
		if iface.Kind == kind {
			return true
		}
	}
	return false
}

func (p parsedAgentCard) DefaultCommandInterface() commandiface.CommandInterface {
	for _, iface := range p.CommandInterfaces {
		if iface.Default {
			return iface
		}
	}
	if len(p.CommandInterfaces) > 0 {
		return p.CommandInterfaces[0]
	}
	return commandiface.CommandInterface{}
}

func (p parsedAgentCard) SupportsExplicitShell(kind string) bool {
	switch kind {
	case "bash":
		if !p.HasSkill("bash") {
			return false
		}
		return len(p.CommandInterfaces) == 0 || p.HasCommandKind("bash")
	case "powershell":
		if !p.HasSkill("powershell") {
			return false
		}
		return len(p.CommandInterfaces) == 0 || p.HasCommandKind("powershell")
	default:
		return false
	}
}
```

- [ ] **Step 3: Replace ad hoc card parsing**

In `multi-agent/internal/driver/tools.go`, update `hasSkill`:

```go
func hasSkill(c agentsdk.AgentCard, want string) bool {
	return parseAgentCard(c).HasSkill(want)
}
```

Update `cardShortID`:

```go
func cardShortID(c agentsdk.AgentCard) string {
	return parseAgentCard(c).ShortID
}
```

Extend `listAgentsTool` output struct:

```go
		Platform          commandiface.Platform           `json:"platform,omitempty"`
		CommandInterfaces []commandiface.CommandInterface `json:"command_interfaces,omitempty"`
```

and populate it with `parsed := parseAgentCard(c)`.

- [ ] **Step 4: Write failing shell tool tests**

Append to `multi-agent/internal/driver/slave_tools_test.go`:

```go
func TestRunSlaveBashRejectsPowerShellOnlyTarget(t *testing.T) {
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"platform":{"os":"windows","arch":"amd64"},
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"powershell.exe","default":true}]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			t.Fatalf("must not delegate bash to powershell-only target")
			return nil, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "run_slave_bash")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-win","script":"echo ok"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not provide a Bash command interface")
	require.Contains(t, err.Error(), "run_slave_powershell")
}

func TestRunSlavePowerShellDelegatesPowerShellSkill(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell"],
					"command_interfaces":[{"skill":"powershell","kind":"powershell","command":"powershell.exe","default":true}]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-ps"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{
				TaskID: "task-ps",
				Status: "completed",
				Result: json.RawMessage(`"{\"exit_code\":0,\"stdout\":\"ok\\r\\n\",\"stderr\":\"\",\"workdir\":\"C:\\\\loom\"}"`),
			}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "run_slave_powershell")
	out, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-win","script":"Write-Output ok","timeout_sec":30}`))
	require.NoError(t, err)
	require.Equal(t, "slave-win", delegated.TargetID)
	require.Equal(t, "powershell", delegated.Skill)
	require.JSONEq(t, `{"script":"Write-Output ok","timeout_sec":30}`, delegated.Prompt)
	require.Contains(t, string(out), `"stdout":"ok\r\n"`)
}

func TestRunSlaveShellUsesDefaultInterface(t *testing.T) {
	var delegated agentsdk.DelegateTaskRequest
	sdk := &fakeSDK{
		discoverFunc: func() ([]agentsdk.AgentCard, error) {
			return []agentsdk.AgentCard{
				{AgentID: "slave-win", DisplayName: "slave-win", Status: "available", Card: json.RawMessage(`{
					"skills":["powershell","bash"],
					"command_interfaces":[
						{"skill":"powershell","kind":"powershell","command":"powershell.exe","default":true},
						{"skill":"bash","kind":"bash","command":"C:\\\\Program Files\\\\Git\\\\bin\\\\bash.exe","default":false}
					]
				}`)},
			}, nil
		},
		delegateFunc: func(req agentsdk.DelegateTaskRequest) (*agentsdk.DelegateTaskResponse, error) {
			delegated = req
			return &agentsdk.DelegateTaskResponse{TaskID: "task-shell"}, nil
		},
		getTaskFunc: func(id string, includeOutput bool) (*agentsdk.TaskInfo, error) {
			return &agentsdk.TaskInfo{TaskID: id, Status: "completed", Result: json.RawMessage(`"{\"exit_code\":0,\"stdout\":\"ok\",\"stderr\":\"\",\"workdir\":\"C:\\\\loom\"}"`)}, nil
		},
	}
	tool := toolByName(t, newTestTools(t, sdk), "run_slave_shell")
	_, err := tool.Call(context.Background(), json.RawMessage(`{"target_display_name":"slave-win","script":"Write-Output ok","timeout_sec":10}`))
	require.NoError(t, err)
	require.Equal(t, "powershell", delegated.Skill)
}
```

- [ ] **Step 5: Add PowerShell and generic shell tools**

In `multi-agent/internal/driver/tools.go`, add tools after `run_slave_bash`:

```go
		&runSlavePowerShellTool{t},
		&runSlaveShellTool{t},
```

In `multi-agent/internal/driver/slave_tools.go`, add shared args:

```go
type runSlaveShellArgs struct {
	TargetAgentID     string            `json:"target_agent_id"`
	TargetDisplayName string            `json:"target_display_name"`
	Script            string            `json:"script"`
	Env               map[string]string `json:"env,omitempty"`
	TimeoutSec        int               `json:"timeout_sec,omitempty"`
	Wait              *bool             `json:"wait,omitempty"`
}
```

Add helper:

```go
func (t *Tools) delegateShellTask(ctx context.Context, card agentsdk.AgentCard, skill string, args runSlaveShellArgs) (json.RawMessage, error) {
	prompt, err := json.Marshal(struct {
		Script     string            `json:"script"`
		TimeoutSec int               `json:"timeout_sec,omitempty"`
		Env        map[string]string `json:"env,omitempty"`
	}{Script: args.Script, TimeoutSec: args.TimeoutSec, Env: args.Env})
	if err != nil {
		return nil, &MCPToolError{Message: err.Error()}
	}
	resp, err := t.sdk.DelegateTask(ctx, agentsdk.DelegateTaskRequest{
		TargetID:       card.AgentID,
		Skill:          skill,
		Prompt:         string(prompt),
		TimeoutSeconds: args.TimeoutSec,
	})
	if err != nil {
		return nil, &MCPToolError{Message: "delegate " + skill + " task: " + err.Error()}
	}
	wait := true
	if args.Wait != nil {
		wait = *args.Wait
	}
	if !wait {
		return json.Marshal(map[string]interface{}{
			"task_id":             resp.TaskID,
			"target_id":           card.AgentID,
			"target_display_name": card.DisplayName,
			"skill":               skill,
			"status":              resp.Status,
		})
	}
	return t.waitDelegatedTask(ctx, resp.TaskID, args.TimeoutSec)
}
```

Change `runSlaveBashTool.Call` to parse `runSlaveShellArgs`, require `parsed.SupportsExplicitShell("bash")`, and use this error when PowerShell exists:

```go
parsed := parseAgentCard(card)
if !parsed.SupportsExplicitShell("bash") {
	msg := "target " + card.DisplayName + " does not provide a Bash command interface"
	if parsed.SupportsExplicitShell("powershell") {
		msg += "; use run_slave_powershell for this target"
	}
	return nil, &MCPToolError{Message: msg}
}
return r.t.delegateShellTask(ctx, card, "bash", args)
```

Add `runSlavePowerShellTool` and `runSlaveShellTool` using the same input schema:

```go
type runSlavePowerShellTool struct{ t *Tools }

func (r *runSlavePowerShellTool) Name() string { return "run_slave_powershell" }
func (r *runSlavePowerShellTool) Description() string {
	return "Run an explicit PowerShell script on a selected slave that advertises the powershell command interface."
}
func (r *runSlavePowerShellTool) InputSchema() json.RawMessage { return shellInputSchema() }
func (r *runSlavePowerShellTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	args, err := parseShellArgs(raw)
	if err != nil {
		return nil, err
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	if !parseAgentCard(card).SupportsExplicitShell("powershell") {
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not provide a PowerShell command interface"}
	}
	return r.t.delegateShellTask(ctx, card, "powershell", args)
}

type runSlaveShellTool struct{ t *Tools }

func (r *runSlaveShellTool) Name() string { return "run_slave_shell" }
func (r *runSlaveShellTool) Description() string {
	return "Run a script on the target slave using its default command interface from the capability card."
}
func (r *runSlaveShellTool) InputSchema() json.RawMessage { return shellInputSchema() }
func (r *runSlaveShellTool) Call(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	args, err := parseShellArgs(raw)
	if err != nil {
		return nil, err
	}
	card, err := r.t.resolveAvailableAgent(ctx, args.TargetAgentID, args.TargetDisplayName)
	if err != nil {
		return nil, err
	}
	parsed := parseAgentCard(card)
	iface := parsed.DefaultCommandInterface()
	switch iface.Kind {
	case "powershell":
		return r.t.delegateShellTask(ctx, card, "powershell", args)
	case "bash":
		return r.t.delegateShellTask(ctx, card, "bash", args)
	case "":
		if parsed.HasSkill("bash") {
			return r.t.delegateShellTask(ctx, card, "bash", args)
		}
		return nil, &MCPToolError{Message: "target " + card.DisplayName + " does not advertise a shell command interface"}
	default:
		return nil, &MCPToolError{Message: "unsupported default command interface " + iface.Kind}
	}
}
```

Add helpers:

```go
func shellInputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
        "target_agent_id":{"type":"string"},
        "target_display_name":{"type":"string"},
        "script":{"type":"string"},
        "env":{"type":"object","additionalProperties":{"type":"string"}},
        "timeout_sec":{"type":"integer"},
        "wait":{"type":"boolean"}
    },"required":["script"],"additionalProperties":false}`)
}

func parseShellArgs(raw json.RawMessage) (runSlaveShellArgs, error) {
	var args runSlaveShellArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, &MCPToolError{Message: "invalid args: " + err.Error()}
	}
	if args.Script == "" {
		return args, &MCPToolError{Message: "script is required"}
	}
	return args, nil
}
```

- [ ] **Step 6: Run focused tests**

Run:

```bash
cd multi-agent
go test ./internal/driver -run 'AgentCard|ListAgents|RunSlave(Bash|PowerShell|Shell)' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/internal/driver/agent_card.go multi-agent/internal/driver/agent_card_test.go multi-agent/internal/driver/tools.go multi-agent/internal/driver/slave_tools.go multi-agent/internal/driver/slave_tools_test.go
git commit -m "feat: route slave shell tools by card metadata"
```

## Task 6: Normalize Slave Capabilities and Register PowerShell Route

**Files:**
- Create: `multi-agent/cmd/slave-agent/capabilities.go`
- Create: `multi-agent/cmd/slave-agent/capabilities_test.go`
- Modify: `multi-agent/cmd/slave-agent/main.go`
- Modify: `multi-agent/internal/capabilitydoc/doc.go`
- Modify: `multi-agent/internal/capabilitydoc/doc_test.go`

- [ ] **Step 1: Write failing slave capability tests**

Create `multi-agent/cmd/slave-agent/capabilities_test.go`:

```go
package main

import (
	"os/exec"
	"testing"

	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
)

func TestNormalizeDiscoveryWindowsRemovesBashWhenMissing(t *testing.T) {
	cfg := &config.Config{Discovery: config.Discovery{Skills: []string{"chat", "bash", "powershell", "file"}}}
	caps := normalizeDiscoveryForRuntime(cfg, commandiface.Detector{
		GOOS:   "windows",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			if name == "powershell.exe" {
				return "powershell.exe", nil
			}
			return "", exec.ErrNotFound
		},
		WSLHasDistro: func() bool { return false },
	})
	if hasSkill(cfg.Discovery.Skills, "bash") {
		t.Fatalf("bash should not be advertised without a Bash runtime: %#v", cfg.Discovery.Skills)
	}
	if !hasSkill(cfg.Discovery.Skills, "powershell") {
		t.Fatalf("powershell missing: %#v", cfg.Discovery.Skills)
	}
	if len(caps.CommandInterfaces) != 1 || caps.CommandInterfaces[0].Kind != "powershell" {
		t.Fatalf("interfaces = %#v", caps.CommandInterfaces)
	}
}
```

- [ ] **Step 2: Add normalization helper**

Create `multi-agent/cmd/slave-agent/capabilities.go`:

```go
package main

import (
	"github.com/yourorg/multi-agent/internal/commandiface"
	"github.com/yourorg/multi-agent/internal/config"
)

func normalizeDiscoveryForRuntime(cfg *config.Config, detector commandiface.Detector) commandiface.Capabilities {
	caps := detector.Build(cfg.Discovery.Skills)
	cfg.Discovery.Skills = append([]string{}, caps.Skills...)
	return caps
}
```

- [ ] **Step 3: Register routes from normalized skills**

In `multi-agent/cmd/slave-agent/main.go`, after config load and before `tunnel.New`:

```go
	caps := normalizeDiscoveryForRuntime(cfg, commandiface.DefaultDetector())
```

Add import:

```go
	"github.com/yourorg/multi-agent/internal/commandiface"
```

After `tn := tunnel.New(cfg, cfgPath, ui)`:

```go
	tn.SetPlatform(caps.Platform)
	tn.SetCommandInterfaces(caps.CommandInterfaces)
```

Register PowerShell route:

```go
	if hasSkill(cfg.Discovery.Skills, "powershell") {
		routes["powershell"] = executor.NewPowerShellExecutor(executor.PowerShellConfig{WorkDir: cfg.Claude.WorkDir})
	}
```

Keep Bash route gated by the normalized skill list:

```go
	if hasSkill(cfg.Discovery.Skills, "bash") {
		routes["bash"] = executor.NewBashExecutor(executor.BashConfig{WorkDir: cfg.Claude.WorkDir})
	}
```

- [ ] **Step 4: Include shell commands in capability document**

In `multi-agent/internal/capabilitydoc/doc.go`, change `scanCommands` base list:

```go
	names := []string{"powershell.exe", "powershell", "pwsh", "bash", "python3", "node", "npm", "go", "docker"}
```

Append to `multi-agent/internal/capabilitydoc/doc_test.go`:

```go
func TestStoreRefreshIncludesPowerShellSkill(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	require.NoError(t, store.Refresh(context.Background(), Input{
		Config: &config.Config{
			Server: config.Server{Name: "slave-win"},
			Discovery: config.Discovery{
				DisplayName: "slave-win",
				Skills:      []string{"chat", "powershell", "file"},
			},
		},
		WorkDir: "C:\\Users\\Administrator\\.loom\\slave-win",
		Reason:  "startup",
	}))
	body, err := os.ReadFile(filepath.Join(dir, "CAPABILITIES.md"))
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "- powershell")
	require.Contains(t, text, "workdir: C:\\Users\\Administrator\\.loom\\slave-win")
}
```

- [ ] **Step 5: Run focused tests**

Run:

```bash
cd multi-agent
go test ./cmd/slave-agent ./internal/capabilitydoc -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add multi-agent/cmd/slave-agent/capabilities.go multi-agent/cmd/slave-agent/capabilities_test.go multi-agent/cmd/slave-agent/main.go multi-agent/internal/capabilitydoc/doc.go multi-agent/internal/capabilitydoc/doc_test.go
git commit -m "feat: advertise slave command interfaces"
```

## Task 7: Add Windows Deployment Assets

**Files:**
- Create: `multi-agent/deploy/windows/driver/install.ps1`
- Create: `multi-agent/deploy/windows/driver/config.yaml.template`
- Create: `multi-agent/deploy/windows/driver/codex-mcp.toml.template`
- Create: `multi-agent/deploy/windows/driver/mcp.json.template`
- Create: `multi-agent/deploy/windows/driver/README.md`
- Create: `multi-agent/deploy/windows/slave/install.ps1`
- Create: `multi-agent/deploy/windows/slave/config.yaml.template`
- Create: `multi-agent/deploy/windows/slave/slave-agent-service.ps1`
- Create: `multi-agent/deploy/windows/slave/README.md`

- [ ] **Step 1: Add Windows driver installer**

Create `multi-agent/deploy/windows/driver/install.ps1`:

```powershell
[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$Project,
  [Parameter(Mandatory=$true)][string]$Name,
  [Parameter(Mandatory=$true)][string]$ObserverUrl,
  [string]$Workspace = "ws-default",
  [ValidateSet("claude","codex")][string]$Agent = "codex",
  [string]$ApiKey = "",
  [string]$Bin = "",
  [string]$TokenDir = ""
)

$ErrorActionPreference = "Stop"
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$ProjectPath = (New-Item -ItemType Directory -Force -Path $Project).FullName
if ($TokenDir -eq "") {
  $TokenDir = Join-Path $env:USERPROFILE ".loom\$Name"
}
New-Item -ItemType Directory -Force -Path $TokenDir | Out-Null

if ($Bin -eq "") {
  $Bin = Join-Path (Split-Path -Parent (Split-Path -Parent $Here)) "bin\driver-agent.windows-amd64.exe"
}
if (!(Test-Path $Bin)) {
  throw "missing driver-agent binary: $Bin"
}
$DestBin = Join-Path $ProjectPath "driver-agent.exe"
Copy-Item -Force $Bin $DestBin

$Config = Get-Content (Join-Path $Here "config.yaml.template") -Raw
$Config = $Config.Replace("__SERVER_NAME__", $Name)
$Config = $Config.Replace("__DISPLAY_NAME__", $Name)
$Config = $Config.Replace("__OBSERVER_URL__", $ObserverUrl)
$Config = $Config.Replace("__WORKSPACE_ID__", $Workspace)
$Config = $Config.Replace("__AGENT_KIND__", $Agent)
$Config = $Config.Replace("__API_KEY__", $ApiKey)
$Config = $Config.Replace("__TOKEN_STATE_PATH__", (Join-Path $TokenDir "observer.token").Replace("\","\\"))
$Config = $Config.Replace("__WORKDIR__", $ProjectPath.Replace("\","\\"))
$ConfigPath = Join-Path $ProjectPath "config.yaml"
Set-Content -Path $ConfigPath -Value $Config -Encoding UTF8

if ($Agent -eq "codex") {
  $CodexDir = Join-Path $ProjectPath ".codex"
  New-Item -ItemType Directory -Force -Path $CodexDir | Out-Null
  $Toml = Get-Content (Join-Path $Here "codex-mcp.toml.template") -Raw
  $Toml = $Toml.Replace("__DRIVER_BIN__", $DestBin.Replace("\","\\"))
  $Toml = $Toml.Replace("__CONFIG_PATH__", $ConfigPath.Replace("\","\\"))
  Set-Content -Path (Join-Path $CodexDir "config.toml") -Value $Toml -Encoding UTF8
} else {
  $Mcp = Get-Content (Join-Path $Here "mcp.json.template") -Raw
  $Mcp = $Mcp.Replace("__DRIVER_BIN__", $DestBin.Replace("\","\\"))
  $Mcp = $Mcp.Replace("__CONFIG_PATH__", $ConfigPath.Replace("\","\\"))
  Set-Content -Path (Join-Path $ProjectPath ".mcp.json") -Value $Mcp -Encoding UTF8
}

$SkillSource = Join-Path (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $Here))) "skills\multiagent"
if (Test-Path $SkillSource) {
  $SkillDest = Join-Path $ProjectPath "skills\multiagent"
  New-Item -ItemType Directory -Force -Path (Split-Path -Parent $SkillDest) | Out-Null
  Copy-Item -Recurse -Force $SkillSource $SkillDest
}

Write-Host "Driver installed at $ProjectPath"
Write-Host "Register with:"
Write-Host "  $DestBin register --config $ConfigPath"
```

- [ ] **Step 2: Add Windows driver templates**

Create `multi-agent/deploy/windows/driver/config.yaml.template`:

```yaml
server:
  url: https://agent.cs.ac.cn
  name: __SERVER_NAME__

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

agent:
  kind: __AGENT_KIND__

claude:
  bin: claude
  workdir: "__WORKDIR__"
  extra_args: []

codex:
  bin: codex
  workdir: "__WORKDIR__"
  extra_args: []

discovery:
  display_name: __DISPLAY_NAME__
  description: "Windows driver agent."
  skills: []

listen_addr: "127.0.0.1:0"

planner:
  timeout_sec: 300

fanout:
  max_concurrency: 4
  subtask_defaults:
    timeout_sec: 900

observer:
  enabled: false
  telemetry_enabled: false
  url: __OBSERVER_URL__
  workspace_id: __WORKSPACE_ID__
  workspace_name: ""
  agent_id: __DISPLAY_NAME__
  api_key: "__API_KEY__"
  token_state_path: "__TOKEN_STATE_PATH__"

driver_defaults:
  target_display_name: ""
  task_timeout_sec: 600
  audit_log_dir: ""
  disable_uid_check: true
  max_dir_cache_entries: 50000
  artifact_transport: peer_proxy
```

Create `multi-agent/deploy/windows/driver/codex-mcp.toml.template`:

```toml
[mcp_servers.driver]
command = "__DRIVER_BIN__"
args = ["serve-mcp", "--config", "__CONFIG_PATH__"]
```

Create `multi-agent/deploy/windows/driver/mcp.json.template`:

```json
{
  "mcpServers": {
    "driver": {
      "command": "__DRIVER_BIN__",
      "args": ["serve-mcp", "--config", "__CONFIG_PATH__"]
    }
  }
}
```

- [ ] **Step 3: Add Windows slave installer**

Create `multi-agent/deploy/windows/slave/install.ps1`:

```powershell
[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$Name,
  [Parameter(Mandatory=$true)][string]$ObserverUrl,
  [string]$Workspace = "ws-default",
  [ValidateSet("claude","codex")][string]$Agent = "codex",
  [string]$ApiKey = "",
  [string]$Bin = "",
  [string]$LoomHome = "",
  [switch]$InstallService,
  [string]$ServiceName = "",
  [switch]$EnableBash
)

$ErrorActionPreference = "Stop"
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
if ($LoomHome -eq "") {
  $LoomHome = Join-Path $env:USERPROFILE ".loom\$Name"
}
if ($ServiceName -eq "") {
  $ServiceName = "loom-slave-agent-$Name"
}
New-Item -ItemType Directory -Force -Path $LoomHome | Out-Null

if ($Bin -eq "") {
  $Bin = Join-Path (Split-Path -Parent (Split-Path -Parent $Here)) "bin\slave-agent.windows-amd64.exe"
}
if (!(Test-Path $Bin)) {
  throw "missing slave-agent binary: $Bin"
}
$DestBin = Join-Path $LoomHome "slave-agent.exe"
Copy-Item -Force $Bin $DestBin

$Skills = @("chat","chat_resume","powershell","file","permissions")
if ($EnableBash) {
  $Bash = Get-Command bash.exe -ErrorAction SilentlyContinue
  if ($null -ne $Bash) {
    $Skills += "bash"
  } else {
    Write-Warning "EnableBash was requested, but bash.exe was not found. The slave will not advertise bash."
  }
}
$SkillYaml = ($Skills | ForEach-Object { "    - $_" }) -join "`n"
$WorkDir = $LoomHome

$Config = Get-Content (Join-Path $Here "config.yaml.template") -Raw
$Config = $Config.Replace("__SERVER_NAME__", $Name)
$Config = $Config.Replace("__DISPLAY_NAME__", $Name)
$Config = $Config.Replace("__OBSERVER_URL__", $ObserverUrl)
$Config = $Config.Replace("__WORKSPACE_ID__", $Workspace)
$Config = $Config.Replace("__AGENT_KIND__", $Agent)
$Config = $Config.Replace("__API_KEY__", $ApiKey)
$Config = $Config.Replace("__LOOM_HOME__", $LoomHome.Replace("\","\\"))
$Config = $Config.Replace("__WORKDIR__", $WorkDir.Replace("\","\\"))
$Config = $Config.Replace("__TOKEN_STATE_PATH__", (Join-Path $LoomHome "observer.token").Replace("\","\\"))
$Config = $Config.Replace("__SKILLS__", $SkillYaml)
$ConfigPath = Join-Path $LoomHome "config.yaml"
Set-Content -Path $ConfigPath -Value $Config -Encoding UTF8

if ($InstallService) {
  $Wrapper = Join-Path $LoomHome "slave-agent-service.ps1"
  $WrapperBody = Get-Content (Join-Path $Here "slave-agent-service.ps1") -Raw
  $WrapperBody = $WrapperBody.Replace("__SLAVE_BIN__", $DestBin.Replace("\","\\"))
  $WrapperBody = $WrapperBody.Replace("__CONFIG_PATH__", $ConfigPath.Replace("\","\\"))
  Set-Content -Path $Wrapper -Value $WrapperBody -Encoding UTF8
  $Existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
  if ($null -eq $Existing) {
    New-Service -Name $ServiceName -BinaryPathName "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$Wrapper`"" -StartupType Automatic
  }
  Start-Service -Name $ServiceName
  Write-Host "Service started: $ServiceName"
} else {
  Write-Host "Slave installed at $LoomHome"
  Write-Host "Start in foreground with:"
  Write-Host "  $DestBin $ConfigPath"
}
```

- [ ] **Step 4: Add Windows slave templates**

Create `multi-agent/deploy/windows/slave/config.yaml.template`:

```yaml
server:
  url: https://agent.cs.ac.cn
  name: __SERVER_NAME__

credentials:
  sandbox_id: ""
  tunnel_token: ""
  proxy_token: ""
  workspace_id: ""
  short_id: ""

agent:
  kind: __AGENT_KIND__

claude:
  bin: claude
  workdir: "__WORKDIR__"
  extra_args: []

codex:
  bin: codex
  workdir: "__WORKDIR__"
  extra_args: []

mcp_servers: {}

discovery:
  display_name: __DISPLAY_NAME__
  description: "Windows slave-agent (__DISPLAY_NAME__)."
  skills:
__SKILLS__

observer:
  enabled: false
  telemetry_enabled: false
  url: __OBSERVER_URL__
  workspace_id: __WORKSPACE_ID__
  workspace_name: ""
  agent_id: __DISPLAY_NAME__
  api_key: "__API_KEY__"
  token_state_path: "__TOKEN_STATE_PATH__"

humanloop:
  shutdown_grace_sec: 10
  max_questions_per_task: 5

resources:
  cpu:
    cores: 1
    arch: amd64
  memory_gb: 1
  devices: []
  tags:
    - windows
```

Create `multi-agent/deploy/windows/slave/slave-agent-service.ps1`:

```powershell
$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent "__CONFIG_PATH__")
& "__SLAVE_BIN__" "__CONFIG_PATH__"
exit $LASTEXITCODE
```

- [ ] **Step 5: Add Windows deployment READMEs**

Create `multi-agent/deploy/windows/driver/README.md` with:

```markdown
# Windows Driver Installer

Use this installer from PowerShell. It stages `driver-agent.windows-amd64.exe`, renders `config.yaml`, writes the Codex or Claude MCP registration file, and copies the project `multiagent` skill bundle.

```powershell
.\install.ps1 -Project "$HOME\loom-driver" -Name "driver-windows" -ObserverUrl "https://loom.nj.cs.ac.cn:10062/" -Agent codex
```

Then register the driver:

```powershell
$HOME\loom-driver\driver-agent.exe register --config $HOME\loom-driver\config.yaml
```

The registration command prints an agentserver device-code URL. Open it in a browser and approve. Do not paste Windows host passwords, observer keys, or agentserver tokens into this repository.
```

Create `multi-agent/deploy/windows/slave/README.md` with:

```markdown
# Windows Slave Installer

Use this installer from PowerShell. It stages `slave-agent.windows-amd64.exe`, renders `config.yaml`, and can run the slave in foreground mode or install a Windows Service.

Foreground smoke install:

```powershell
.\install.ps1 -Name "slave-windows" -ObserverUrl "https://loom.nj.cs.ac.cn:10062/" -Agent codex
$HOME\.loom\slave-windows\slave-agent.exe $HOME\.loom\slave-windows\config.yaml
```

The first foreground run prints an agentserver device-code URL. Register the slave before converting it to a service.

Git Bash is opt-in:

```powershell
.\install.ps1 -Name "slave-windows" -ObserverUrl "https://loom.nj.cs.ac.cn:10062/" -EnableBash
```

If `bash.exe` is not found, the rendered config will not advertise `bash`. PowerShell remains the Windows default command interface.
```

- [ ] **Step 6: Run PowerShell syntax checks when available**

Run on a Windows host or GitHub Actions Windows runner:

```powershell
Get-ChildItem deploy/windows -Recurse -Filter *.ps1 | ForEach-Object {
  $null = [System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$null, [ref]$errors)
  if ($errors.Count -gt 0) { throw $errors }
}
```

Expected: PASS with no parser errors.

- [ ] **Step 7: Commit**

```bash
git add multi-agent/deploy/windows
git commit -m "feat: add windows deployment assets"
```

## Task 8: Update Project Skills and Prompts

**Files:**
- Modify: `skills/multiagent/SKILL.md`
- Modify: `skills/multiagent/references/driver-tools.md`
- Modify: `skills/multiagent/references/slave-skills.md`
- Modify: `skills/multiagent/references/orchestration-patterns.md`
- Modify: `multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md`

- [ ] **Step 1: Update core skill rule**

In `skills/multiagent/SKILL.md`, add after "Do not assume driver and slaves share a local filesystem":

```markdown
Do not assume every slave has Bash. Inspect `platform` and `command_interfaces` from `list_agents` or `inspect_capabilities` before choosing a shell tool. Use `run_slave_shell` when the user did not specify a shell, `run_slave_powershell` for Windows/PowerShell work, and `run_slave_bash` only when the target advertises a real Bash command interface.
```

Add to Common Mistakes:

```markdown
- Calling `run_slave_bash` on a Windows slave just because PowerShell can run commands. `run_slave_bash` means real Bash only; use `run_slave_powershell` or `run_slave_shell` from the target's card.
```

- [ ] **Step 2: Update driver tools reference**

In `skills/multiagent/references/driver-tools.md`, update the `list_agents` response example to include:

```json
"platform": {"os": "windows", "arch": "amd64"},
"command_interfaces": [
  {"skill": "powershell", "kind": "powershell", "command": "powershell.exe", "default": true}
]
```

Replace the `run_slave_bash` text with:

```markdown
Requires target skill `bash` and a Bash command interface. Delegates `skill:"bash"` with JSON prompt. This is real Bash only; it does not execute PowerShell on Windows.
```

Add sections:

```markdown
### `run_slave_powershell`

Input:

```json
{
  "target_agent_id": "optional",
  "target_display_name": "slave-windows",
  "script": "Write-Output 'ok'",
  "env": {"KEY": "value"},
  "timeout_sec": 60,
  "wait": true
}
```

Requires target skill `powershell` and a PowerShell command interface. Delegates `skill:"powershell"` with JSON prompt. Use Windows path syntax such as `C:\Users\Administrator\.loom\slave-windows`.

### `run_slave_shell`

Input is the same as `run_slave_bash`. The driver reads the target card's `command_interfaces` and delegates to the interface marked `default:true`. On Windows this should be PowerShell; on Unix this should be Bash. If the target is a legacy card with `bash` but no `command_interfaces`, the driver treats it as legacy Bash.
```

- [ ] **Step 3: Update slave skills reference**

In `skills/multiagent/references/slave-skills.md`, change the top skill list:

```markdown
- `powershell`: run explicit PowerShell through native slave-agent code.
- `bash`: run explicit Bash through native slave-agent code. On Windows this is advertised only when Git Bash or WSL Bash is detected.
```

Add after `## bash`:

```markdown
## `powershell`

Prompt is JSON:

```json
{
  "script": "Write-Output 'hello'",
  "timeout_sec": 60,
  "env": {"KEY": "value"}
}
```

Use for explicit Windows-native commands. Prefer PowerShell path operations (`Join-Path`, `New-Item`, `Get-Content`, `Set-Content`) over Bash syntax on Windows slaves.
```

Add a card metadata section:

```markdown
## Platform and command interfaces

Slave discovery cards include:

```json
{
  "platform": {"os": "windows", "arch": "amd64"},
  "command_interfaces": [
    {"skill": "powershell", "kind": "powershell", "command": "powershell.exe", "default": true}
  ]
}
```

Drivers must choose shell tools from this metadata. Missing metadata means a legacy card; if it advertises `bash`, the driver may treat it as Unix-like Bash.
```

- [ ] **Step 4: Update orchestration patterns**

In `skills/multiagent/references/orchestration-patterns.md`, add under file transfer anti-patterns:

```markdown
Windows examples should use `run_slave_powershell` or `run_slave_shell`:

```powershell
New-Item -ItemType Directory -Force -Path 'C:\tmp\loom'
Set-Content -Path 'C:\tmp\loom\hello.txt' -Value 'hello'
```

Do not ship files through PowerShell here-strings when `write_slave_file` is available. The same file-transfer rule applies on Windows and Unix.
```

- [ ] **Step 5: Update Codex driver prompt**

Replace the core tools section in `multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md` with:

```markdown
## Core tools

- `mcp__driver__list_agents()` — list workspace agents with `platform` and `command_interfaces`
- `mcp__driver__inspect_capabilities()` — inspect skills, resources, MCP tools, and shell interfaces
- `mcp__driver__run_slave_shell(...)` — run the target's default shell
- `mcp__driver__run_slave_powershell(...)` — run explicit PowerShell
- `mcp__driver__run_slave_bash(...)` — run explicit real Bash only
```

Add to "When you start":

```markdown
3. Pick shell tools from `command_interfaces`: Windows defaults to PowerShell; Bash means real Bash only.
```

- [ ] **Step 6: Check tracked skill text**

Run:

```bash
rg -n "run_slave_powershell|run_slave_shell|command_interfaces|PowerShell|real Bash" skills/multiagent multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md
```

Expected: each search term appears in the tracked sources.

- [ ] **Step 7: Commit**

```bash
git add skills/multiagent multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md
git commit -m "docs: teach skills windows shell routing"
```

## Task 9: Add Windows CI and Build Outputs

**Files:**
- Modify: `.github/workflows/multi-agent.yml`
- Modify: `multi-agent/tests/prod_test/README.md`

- [ ] **Step 1: Add Windows runner job**

In `.github/workflows/multi-agent.yml`, add:

```yaml
  windows:
    runs-on: windows-latest
    defaults:
      run:
        working-directory: multi-agent
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: multi-agent/go.mod
          cache-dependency-path: multi-agent/go.sum
      - run: go test ./cmd/driver-agent ./cmd/slave-agent ./internal/humanloop ./internal/executor ./pkg/agentbackend/claude ./pkg/agentbackend/codex -count=1
      - run: go build -trimpath -ldflags="-s -w" -o driver-agent.windows-amd64.exe ./cmd/driver-agent
      - run: go build -trimpath -ldflags="-s -w" -o slave-agent.windows-amd64.exe ./cmd/slave-agent
```

- [ ] **Step 2: Add Linux cross-compile check**

In the existing Linux `go` job, after `go vet ./...`, add:

```yaml
      - run: GOOS=windows GOARCH=amd64 go test -exec=true ./cmd/driver-agent ./cmd/slave-agent
      - run: CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/driver-agent.windows-amd64.exe ./cmd/driver-agent
      - run: CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/slave-agent.windows-amd64.exe ./cmd/slave-agent
```

- [ ] **Step 3: Update prod_test binary rebuild docs**

In `multi-agent/tests/prod_test/README.md`, add to the rebuild block:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/driver-agent.windows-amd64.exe ./cmd/driver-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' \
  -o tests/prod_test/bin/slave-agent.windows-amd64.exe ./cmd/slave-agent
```

Add to the file tree:

```text
│   ├── driver-agent.windows-amd64.exe
│   └── slave-agent.windows-amd64.exe
```

- [ ] **Step 4: Run CI-equivalent local checks**

Run:

```bash
cd multi-agent
GOOS=windows GOARCH=amd64 go test -exec=true ./cmd/driver-agent ./cmd/slave-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/driver-agent.windows-amd64.exe ./cmd/driver-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/slave-agent.windows-amd64.exe ./cmd/slave-agent
```

Expected: PASS and both `/tmp/*.exe` files are created.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/multi-agent.yml multi-agent/tests/prod_test/README.md
git commit -m "ci: add windows agent checks"
```

## Task 10: Real Windows Host Smoke and End-to-End Validation

**Files:**
- Runtime only: `multi-agent/tests/prod_test/windows/driver/`
- Runtime only: `multi-agent/tests/prod_test/windows/slave/`
- Runtime only: `multi-agent/tests/prod_test/windows/scripts/`
- Modify only if tracked docs need correction: `multi-agent/tests/prod_test/README.md`

- [ ] **Step 1: Build prod_test Windows binaries**

Run:

```bash
cd multi-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o tests/prod_test/bin/driver-agent.windows-amd64.exe ./cmd/driver-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o tests/prod_test/bin/slave-agent.windows-amd64.exe ./cmd/slave-agent
```

Expected: both `.exe` files exist under `tests/prod_test/bin/`.

- [ ] **Step 2: Verify Windows host access without printing credentials**

Run:

```bash
ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new Administrator@9.0.16.110 hostname
```

Expected: prints the Windows hostname and does not prompt for or print a password.

- [ ] **Step 3: Upload binaries and installer assets**

Run:

```bash
ssh Administrator@9.0.16.110 "powershell -NoProfile -ExecutionPolicy Bypass -Command \"New-Item -ItemType Directory -Force -Path C:\loom\bin,C:\loom\deploy\windows\slave,C:\loom\deploy\windows\driver | Out-Null\""
scp multi-agent/tests/prod_test/bin/driver-agent.windows-amd64.exe Administrator@9.0.16.110:C:/loom/bin/
scp multi-agent/tests/prod_test/bin/slave-agent.windows-amd64.exe Administrator@9.0.16.110:C:/loom/bin/
scp -r multi-agent/deploy/windows/slave/* Administrator@9.0.16.110:C:/loom/deploy/windows/slave/
scp -r multi-agent/deploy/windows/driver/* Administrator@9.0.16.110:C:/loom/deploy/windows/driver/
```

Expected: uploads complete without exposing secrets.

- [ ] **Step 4: Install and start Windows slave in foreground**

Run:

```bash
ssh Administrator@9.0.16.110 "powershell -NoProfile -ExecutionPolicy Bypass -Command \"Set-Location C:\loom\deploy\windows\slave; .\install.ps1 -Name slave-windows-smoke -ObserverUrl https://loom.nj.cs.ac.cn:10062/ -Bin C:\loom\bin\slave-agent.windows-amd64.exe; & $HOME\.loom\slave-windows-smoke\slave-agent.exe $HOME\.loom\slave-windows-smoke\config.yaml\""
```

Expected: the process prints an agentserver device-code registration URL. Copy only the URL into the user-facing response when registration is needed; do not print tokens from the config.

- [ ] **Step 5: After manual registration, inspect published card**

From an already registered driver, run `list_agents` or `inspect_capabilities`.

Expected Windows slave card contains:

```json
{
  "platform": {"os": "windows", "arch": "amd64"},
  "command_interfaces": [
    {"skill": "powershell", "kind": "powershell", "command": "powershell.exe", "default": true}
  ]
}
```

Expected: no `bash` skill or Bash command interface unless Git Bash or WSL Bash exists on the host.

- [ ] **Step 6: Run PowerShell task through driver**

Use the driver MCP tool:

```json
{
  "target_display_name": "slave-windows-smoke",
  "script": "Write-Output 'hello-windows'; $PSVersionTable.PSVersion.ToString()",
  "timeout_sec": 60
}
```

Expected through `run_slave_powershell`: completed task with `exit_code:0` and `stdout` containing `hello-windows`.

- [ ] **Step 7: Run file round-trip through driver**

Use `write_slave_file`:

```json
{
  "target_display_name": "slave-windows-smoke",
  "path": "C:\\loom\\smoke\\hello.txt",
  "mode": "overwrite",
  "mkdir": true,
  "encoding": "utf-8",
  "content": "hello from driver\n"
}
```

Then use `read_slave_file`:

```json
{
  "target_display_name": "slave-windows-smoke",
  "path": "C:\\loom\\smoke\\hello.txt",
  "encoding": "utf-8",
  "inline_max_bytes": 65536
}
```

Expected: read result content is `hello from driver\n`.

- [ ] **Step 8: Run generic shell routing smoke**

Use `run_slave_shell`:

```json
{
  "target_display_name": "slave-windows-smoke",
  "script": "Write-Output 'default-shell-is-powershell'",
  "timeout_sec": 60
}
```

Expected: delegated skill is `powershell` and stdout contains `default-shell-is-powershell`.

- [ ] **Step 9: Confirm Bash rejection on PowerShell-only Windows slave**

Use `run_slave_bash`:

```json
{
  "target_display_name": "slave-windows-smoke",
  "script": "echo should-not-run",
  "timeout_sec": 60
}
```

Expected: driver returns an MCP tool error saying the target does not provide a Bash command interface and suggests `run_slave_powershell`.

- [ ] **Step 10: Commit tracked doc correction if validation finds one**

If validation exposes an incorrect tracked command or doc statement, commit only tracked source/doc fixes. Do not commit runtime files under `multi-agent/tests/prod_test/windows/`.

```bash
git status --short
git add multi-agent/tests/prod_test/README.md
git commit -m "docs: correct windows prod smoke instructions"
```

## Task 11: Full Verification

**Files:**
- Modify only files changed by prior tasks.

- [ ] **Step 1: Run focused package tests**

Run:

```bash
cd multi-agent
go test ./internal/platform ./internal/humanloop ./internal/executor ./internal/commandiface ./internal/tunnel ./internal/driver ./internal/capabilitydoc ./cmd/driver-agent ./cmd/slave-agent ./pkg/agentbackend/claude ./pkg/agentbackend/codex -count=1
```

Expected: PASS.

- [ ] **Step 2: Run Linux full test suite**

Run:

```bash
cd multi-agent
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Run Windows cross-compile checks**

Run:

```bash
cd multi-agent
GOOS=windows GOARCH=amd64 go test -exec=true ./cmd/driver-agent ./cmd/slave-agent ./internal/humanloop ./internal/executor ./pkg/agentbackend/claude ./pkg/agentbackend/codex
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/driver-agent.windows-amd64.exe ./cmd/driver-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/slave-agent.windows-amd64.exe ./cmd/slave-agent
```

Expected: PASS.

- [ ] **Step 4: Run static checks**

Run:

```bash
cd multi-agent
go vet ./...
go mod tidy -diff
git diff --check
```

Expected: PASS with no diff from `go mod tidy -diff`.

- [ ] **Step 5: Run real Windows smoke**

Run Task 10 against `9.0.16.110`.

Expected: Windows slave registers, publishes `platform.os=windows`, exposes default PowerShell command interface, rejects Bash when no Bash runtime exists, and completes PowerShell/file/default-shell round trips.

- [ ] **Step 6: Final commit if verification edits were needed**

Use this commit only when the verification steps required tracked source fixes after the prior task commits. Stage the touched tracked files explicitly:

```bash
git status --short
git add multi-agent/internal/platform multi-agent/internal/humanloop multi-agent/internal/executor multi-agent/internal/commandiface multi-agent/internal/tunnel multi-agent/internal/driver multi-agent/internal/capabilitydoc multi-agent/cmd/driver-agent multi-agent/cmd/slave-agent multi-agent/pkg/agentbackend/claude multi-agent/pkg/agentbackend/codex
git commit -m "test: validate windows driver slave support"
```

Do not stage `multi-agent/slave-agent.lock` or any runtime credentials.

## Security Review Checklist

- [ ] No Windows host password appears in docs, scripts, shell history copied into docs, commits, or final summaries.
- [ ] No observer API key, telemetry key, agentserver proxy token, tunnel token, sandbox token, or registry token appears in tracked files.
- [ ] PowerShell installers accept API keys only through parameters and do not log generated config contents.
- [ ] Agentserver registration URLs may be printed; generated credential files must not be printed.
- [ ] Runtime files under `multi-agent/tests/prod_test/windows/` are not committed.
