# Windows Driver and Slave Support

**Status:** Draft (2026-06-08)
**Scope:** driver-agent, slave-agent, deploy assets, project skills, CI, and prod_test validation for Windows hosts.
**Non-scope:** observer-server Windows deployment, WSL provisioning, or storing test-machine credentials.
**Test host:** `9.0.16.110` via SSH as an operator-provided Windows account. Credentials must not be committed, echoed, logged, or copied into docs.

## Background

Driver and slave binaries currently assume a Unix-like runtime in several
places even though most code uses portable Go APIs. The immediate goal is to
support a Windows machine as both a driver host and a slave host, with native
PowerShell command execution and optional Bash support when Git Bash or WSL is
available.

Discovery on the test host found:

- Windows Server 2025 Datacenter, AMD64.
- Windows PowerShell 5.1 is present.
- `pwsh`, `git`, `bash`, `codex`, `claude`, `go`, `node`, and `npm` are not
  currently installed.
- `wsl.exe` exists, but no Linux distribution is installed.
- SSH access works without putting any password in commands.

The current cross-compile failure starts at `syscall.Flock` in
`internal/executor/chat_resume.go`. Additional Windows blockers are visible in
code review: Unix socket humanloop IPC, POSIX signals, the slave single-instance
lock, and the Bash executor's hard-coded `/bin/bash`.

## Goals

1. `driver-agent` and `slave-agent` build for `windows/amd64`.
2. A Windows slave can run native PowerShell tasks without WSL or Git Bash.
3. A Windows slave only advertises `bash` when a real Bash runtime is present.
4. Capability cards explicitly tell the driver the slave OS and available
   command interfaces.
5. Driver tools and project skills teach the model to choose PowerShell vs Bash
   from the card, not from stale assumptions.
6. Windows install scripts can set up driver/slave workspaces with the same
   agentserver and observer identity model as Linux.
7. CI prevents Windows build regressions.
8. Real validation runs against the Windows test host without leaking
   credentials.

## Non-Goals

- Do not require WSL or Git Bash for the first working Windows slave.
- Do not make `run_slave_bash` execute PowerShell under the hood.
- Do not add observer-server Windows service support in this feature.
- Do not add a GUI/RDP-dependent installer; SSH and PowerShell scripts are the
  automation path.

## Design Summary

Implement Windows support in three layers:

1. **Runtime portability:** isolate platform-specific locking, process
   termination, and humanloop IPC behind small Go interfaces or build-tagged
   helpers.
2. **Command interface model:** add a PowerShell executor and expose command
   runtime metadata in capability cards.
3. **Deployment and skills:** add Windows PowerShell installers, update CI, and
   update driver/slave skills so models route shell work correctly.

The first Windows validation target is a lightweight slave that can run
`powershell` and `file` tasks. Chat/Codex/Claude support can be enabled after
installing a model CLI on the Windows host, but the platform support should not
depend on that for basic command execution.

## Runtime Portability

### File Locks

Two code paths need cross-process locking:

- `cmd/slave-agent/main.go` single-instance guard.
- `internal/executor/chat_resume.go` per-session resume lock.

Create a small internal lock package with build-tagged implementations:

```go
type FileLock struct {
    Path string
    file *os.File
}

func TryLock(path string) (*FileLock, error)
func (l *FileLock) Unlock() error
```

Unix keeps `syscall.Flock`. Windows uses a lock file opened with sharing rules
that prevent concurrent writers, or the Windows file-locking API via
`golang.org/x/sys/windows` if needed. Call sites should not import `syscall`
directly.

The slave single-instance takeover semantics change on Windows:

- Manual start: refuse if another process holds the lock.
- Service restart: last-start-wins is best-effort. If the holder PID can be
  terminated safely, terminate it; otherwise return a clear error with the lock
  path and holder PID.

### Process Termination

Claude and Codex executors currently send `SIGTERM` after a grace window. Add a
small process helper:

```go
func TerminateProcess(p *os.Process) error
```

Unix sends `SIGTERM`. Windows uses `Kill` for the first implementation. If
later we need graceful Ctrl-Break behavior, it can be added without changing
executor code.

### Humanloop IPC

`internal/humanloop` currently uses Unix domain sockets. Replace the public API
with an endpoint string that can represent either Unix or TCP:

```go
type Endpoint struct {
    Network string // "unix" or "tcp"
    Address string
}

func ListenIPC(baseDir string) (*IPCServer, Endpoint, error)
func DialIPC(ep Endpoint) (*IPCClient, error)
```

Unix uses a socket under the temp dir. Windows uses `127.0.0.1:0` TCP and passes
the chosen address to the humanloop MCP subcommand. The payload format stays
newline-delimited JSON.

Security note: TCP humanloop is loopback-only and short-lived. It carries only
the pause payload, not agentserver or observer credentials.

## Command Interfaces

### PowerShell Executor

Add `internal/executor/powershell.go` with request/response shape parallel to
the Bash executor:

```json
{
  "script": "Write-Output \"hello\"",
  "timeout_sec": 30,
  "env": {
    "NAME": "value"
  }
}
```

Response:

```json
{
  "exit_code": 0,
  "stdout": "hello\r\n",
  "stderr": "",
  "workdir": "C:\\Users\\<User>\\.loom\\slave-windows"
}
```

Execution command on Windows:

```text
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command <script>
```

On non-Windows, `powershell` is registered only if `pwsh` or `powershell` is
explicitly configured or found. The first implementation can keep PowerShell
focused on Windows.

### Bash Detection

`run_slave_bash` must keep meaning real Bash. On Windows:

- If `bash.exe` is found in PATH, register `bash` and report that command.
- Else if WSL has an installed distribution, register `bash` with a WSL-backed
  command interface.
- Else do not register `bash` and do not advertise the `bash` skill.

PowerShell is never advertised as `bash`.

### Generic Shell Tool

Add an optional driver tool `run_slave_shell` after the card schema lands. It
chooses the command interface marked `default=true`. This is convenience only;
existing explicit tools remain:

- `run_slave_bash` -> requires `bash`.
- `run_slave_powershell` -> requires `powershell`.

## Capability Card Schema

Capability cards need explicit platform and command-interface metadata. Add
fields under the card published by slaves:

```json
{
  "skills": ["chat", "powershell", "file", "permissions"],
  "platform": {
    "os": "windows",
    "arch": "amd64"
  },
  "command_interfaces": [
    {
      "skill": "powershell",
      "kind": "powershell",
      "command": "powershell.exe",
      "default": true
    },
    {
      "skill": "bash",
      "kind": "bash",
      "command": "C:\\Program Files\\Git\\bin\\bash.exe",
      "default": false
    }
  ]
}
```

Rules:

- `platform.os` comes from `runtime.GOOS`.
- `platform.arch` comes from `runtime.GOARCH`.
- `command_interfaces[].skill` must correspond to an advertised skill.
- Exactly one command interface should be `default=true` when any shell-like
  interface is present. On Windows, default is PowerShell. On Unix, default is
  Bash.
- Existing cards without these fields remain valid. Driver code must treat
  missing metadata as legacy Unix-like Bash if the `bash` skill exists.

## Driver Tool Behavior

Update card parsing in driver code so shell tools can reason about
`command_interfaces`.

`run_slave_bash`:

- Requires target card to advertise `bash`.
- If command metadata exists, requires an entry with `kind=bash`.
- Error should say the target does not provide a Bash command interface and
  suggest `run_slave_powershell` when PowerShell is present.

`run_slave_powershell`:

- Requires target card to advertise `powershell`.
- Submits skill `powershell`.
- Uses the same target selection path as `run_slave_bash`.

`run_slave_shell`:

- Reads default command interface.
- Delegates to `bash` or `powershell` based on that entry.
- If no command metadata exists, falls back to Bash only when `bash` is
  advertised.

## Skills and Prompt Updates

Update tracked project skills and prompts so agent behavior matches the new
runtime model.

Tracked sources:

- `skills/multiagent/SKILL.md`
- `skills/multiagent/references/driver-tools.md`
- `skills/multiagent/references/slave-skills.md`
- `skills/multiagent/references/orchestration-patterns.md`
- `multi-agent/deploy/linux/driver/prompts-codex/AGENTS.md`

Required content changes:

- Explain `platform.os`, `platform.arch`, and `command_interfaces`.
- Teach drivers to inspect capabilities before choosing shell tools.
- Document that `run_slave_bash` means Bash only.
- Document `run_slave_powershell`.
- Prefer `run_slave_shell` when the user did not specify a shell.
- Keep the existing guidance that file transfer should use file tools rather
  than heredocs/base64 through shell tools.
- Add Windows examples for PowerShell commands and path syntax.

Runtime prod_test copies under `multi-agent/tests/prod_test/...` may be updated
for the test machine, but tracked source files are authoritative.

## Windows Deployment Assets

Add a new directory parallel to `deploy/linux`:

```text
multi-agent/deploy/windows/
  driver/
    install.ps1
    config.yaml.template
    codex-mcp.toml.template
    mcp.json.template
    README.md
  slave/
    install.ps1
    config.yaml.template
    slave-agent-service.ps1
    README.md
```

### Driver Installer

`deploy/windows/driver/install.ps1`:

- Parameters: `-Project`, `-Name`, `-ObserverUrl`, `-Workspace`, `-Agent`,
  `-ApiKey`, `-Bin`, `-TokenDir`.
- Stages `driver-agent.windows-amd64.exe` into the project.
- Renders `config.yaml` with Windows paths.
- Writes Codex or Claude MCP registration.
- Copies project skill/prompt bundle.
- Prints the one-time `driver-agent register --config ...` command.

### Slave Installer

`deploy/windows/slave/install.ps1`:

- Parameters: `-Name`, `-ObserverUrl`, `-Workspace`, `-Agent`, `-ApiKey`,
  `-Bin`, `-LoomHome`, `-InstallService`, `-ServiceName`, `-EnableBash`.
- Default install dir: `%USERPROFILE%\.loom\<Name>`.
- Stages `slave-agent.windows-amd64.exe`.
- Renders `config.yaml`.
- Default skills: `chat`, `powershell`, `file`, `permissions`.
- Adds `bash` only when `-EnableBash` is passed and detection succeeds.
- Supports foreground run and Windows Service installation.

The installer must not accept or print test-machine passwords.

## Build and CI

Add Windows build outputs:

- `driver-agent.windows-amd64.exe`
- `slave-agent.windows-amd64.exe`

Update docs for prod_test rebuild commands and release assets. If this project
later adds a release workflow for binaries, the Windows assets must be uploaded
with checksums next to Linux assets.

CI additions:

- Add a Windows runner job:
  - `go test ./cmd/driver-agent ./cmd/slave-agent ./internal/humanloop ./internal/executor ./pkg/agentbackend/claude ./pkg/agentbackend/codex`
- Add a Linux cross-compile smoke:
  - `GOOS=windows GOARCH=amd64 go test -exec=true ./cmd/driver-agent ./cmd/slave-agent`
  - Use only for compile-style packages. Real test execution belongs on the
    Windows runner.

## prod_test Windows Layout

Create ignored runtime assets under `multi-agent/tests/prod_test/windows/` for
the test host:

```text
multi-agent/tests/prod_test/windows/
  driver/
    config.yaml
    codex-config.toml
    state/
  slave/
    config.yaml
    logs/
  scripts/
    install-driver.ps1
    install-slave.ps1
    smoke-powershell.ps1
```

These files may contain machine-local paths and runtime tokens, so they remain
ignored. Tracked templates live under `deploy/windows`.

## Validation Plan

Local:

1. Run targeted tests for new lock, IPC, PowerShell executor, and driver tools.
2. Run `go test ./...` on Linux.
3. Run Windows CI job or `GOOS=windows` compile checks.

Windows host:

1. SSH to `9.0.16.110` without printing credentials.
2. Upload Windows binaries and PowerShell install scripts.
3. Install Windows slave in foreground mode first.
4. Complete agentserver device-code registration manually.
5. Confirm the published card includes:
   - `platform.os=windows`
   - `platform.arch=amd64`
   - `command_interfaces` with PowerShell default
   - no Bash interface unless Bash exists
6. From a driver, submit:
   - `run_slave_powershell` with `Write-Output "hello-windows"`
   - `write_slave_file` and `read_slave_file` path round-trip using Windows paths
7. If Git Bash is installed later, rerun detection and verify `bash` appears as
   a non-default command interface and `run_slave_bash` works.

## Security Requirements

- Do not store Windows host passwords in repo files, scripts, command history,
  logs, specs, plans, commits, or final summaries.
- Do not pass passwords as command-line arguments.
- Redact agentserver proxy tokens, observer API keys, and any generated
  per-agent tokens from logs.
- SSH host access may be documented by host/IP only; credentials are
  operator-provided out of band.

## Open Risks

- Windows Service behavior for interactive device-code registration may need a
  foreground first-run workflow before installing the service.
- Claude/Codex CLI installation on Windows is not present on the test host.
  Basic PowerShell/file validation should not depend on those CLIs.
- WSL detection output can be localized or encoded differently; Bash detection
  should prefer explicit `bash.exe` before parsing WSL output.
