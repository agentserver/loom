# windows-slave

Generic Windows setup for `slave-agent`. The installer stages the binary,
renders `config.yaml`, and can either print a foreground command or install a
Windows service through the included PowerShell wrapper.

Do not paste secrets or tokens into chat, terminal output, issue comments, or
logs. Use placeholder examples until you are editing your local config.

## Prereqs

1. Put `slave-agent.windows-amd64.exe` in `deploy/windows/bin/`, or pass
   `-Bin C:\path\to\slave-agent.windows-amd64.exe`.
2. Install the agent CLI you plan to use: `codex` or `claude`.
3. Use an elevated PowerShell only when installing the service.

## Foreground Workflow

```powershell
.\install.ps1 `
  -Name "slave-windows" `
  -ObserverUrl "http://observer.example.com:8090" `
  -Workspace "ws-default" `
  -Agent codex `
  -ApiKey "<workspace-api-key>"
```

The default install directory is `%USERPROFILE%\.loom\<Name>`. The installer
prints the foreground command:

```powershell
Set-Location "$env:USERPROFILE\.loom\slave-windows"
.\slave-agent.exe "$env:USERPROFILE\.loom\slave-windows\config.yaml"
```

On first run, approve the registration URL printed by `slave-agent`. After that,
the slave publishes its capability card to the observer and appears in
`list_agents` from the driver.

If you omit `-ApiKey`, edit `observer.api_key` in the local `config.yaml`
before starting `slave-agent`.

## Service Install

Run PowerShell as Administrator:

```powershell
.\install.ps1 `
  -Name "slave-windows" `
  -ObserverUrl "http://observer.example.com:8090" `
  -Workspace "ws-default" `
  -Agent codex `
  -ApiKey "<workspace-api-key>" `
  -InstallService
```

The service name defaults to `loom-slave-<Name>`. Override it with
`-ServiceName`. The wrapper writes stdout and stderr to `slave.log` in the
install directory.

Common service commands:

```powershell
Get-Service loom-slave-slave-windows
Restart-Service loom-slave-slave-windows
Stop-Service loom-slave-slave-windows
```

## Parameters

| Parameter | Default | Notes |
|---|---|---|
| `-Name` | required | Agent display name and install directory suffix. |
| `-ObserverUrl` | required | Observer URL written into `observer.url`. |
| `-Workspace` | `ws-default` | Workspace ID written into the template. |
| `-Agent` | `codex` | `codex` or `claude`. |
| `-ApiKey` | empty | Optional local-only value for `observer.api_key`. |
| `-Bin` | `..\bin\slave-agent.windows-amd64.exe` | Override binary source path. |
| `-LoomHome` | `%USERPROFILE%\.loom\<Name>` | Install directory. |
| `-InstallService` | off | Create and start a Windows service. |
| `-ServiceName` | `loom-slave-<Name>` | Service name when `-InstallService` is set. |
| `-EnableBash` | off | Adds `bash` only when `bash.exe` is found. |

## Skills

Default advertised skills:

| Skill | Purpose |
|---|---|
| `chat` | Natural-language agent task in the slave workdir. |
| `chat_resume` | Resume a paused chat task. |
| `powershell` | Run explicit PowerShell scripts through the native executor. |
| `file` | Read, write, and stat files through native code. |
| `permissions` | Inspect or update agent permissions through native code. |

`bash` is advertised only with `-EnableBash` and a discoverable `bash.exe`.

## Reset

If a service was installed, stop and delete it first:

```powershell
Stop-Service loom-slave-slave-windows
sc.exe delete loom-slave-slave-windows
```

Then remove `%USERPROFILE%\.loom\<Name>`.
