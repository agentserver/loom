# windows-slave

Generic Windows setup for `slave-agent`. The installer stages the binary,
renders `config.yaml`, and prints a foreground command.

Do not paste secrets or tokens into chat, terminal output, issue comments, or
logs. Use placeholder examples until you are editing your local config.

## Prereqs

1. Put `slave-agent.windows-amd64.exe` in `deploy/windows/bin/`, or pass
   `-Bin C:\path\to\slave-agent.windows-amd64.exe`.
2. Install the agent CLI you plan to use (`codex` or `claude`) only when this
   Windows slave should advertise chat support.

## Foreground Workflow

```powershell
.\install.ps1 `
  -Name "slave-windows" `
  -ObserverUrl "http://observer.example.com:8090" `
  -Workspace "ws-default" `
  -Agent codex
```

The default install directory is `%USERPROFILE%\.loom\<Name>`. The installer
prints the foreground command:

```powershell
Set-Location "$env:USERPROFILE\.loom\slave-windows"
.\slave-agent.exe "$env:USERPROFILE\.loom\slave-windows\config.yaml"
```

On first run, approve the registration URL printed by `slave-agent`. After that,
the slave publishes its capability card through agentserver identity and appears
in `list_agents` from the driver.

Legacy observer registration is disabled by default. If you explicitly enable
`observer.enabled` in the local `config.yaml`, also set `observer.api_key`
locally before starting `slave-agent`.

## Service Install

`-InstallService` is intentionally unsupported for now. A plain PowerShell
script cannot be registered directly as a real Windows service. Use the
foreground command, or wrap `slave-agent.exe` with a service supervisor such as
WinSW or NSSM outside this installer.

## Parameters

| Parameter | Default | Notes |
|---|---|---|
| `-Name` | required | Agent display name and install directory suffix. |
| `-ObserverUrl` | required | Observer URL written into `observer.url`. |
| `-Workspace` | `ws-default` | Workspace ID written into the template. |
| `-Agent` | `codex` | `codex` or `claude`. |
| `-ApiKey` | empty | Optional local-only value for `observer.api_key` when legacy observer registration is explicitly enabled. |
| `-Bin` | `..\bin\slave-agent.windows-amd64.exe` | Override binary source path. |
| `-LoomHome` | `%USERPROFILE%\.loom\<Name>` | Install directory. |
| `-InstallService` | off | Unsupported; fails with a clear error. |
| `-ServiceName` | `loom-slave-<Name>` | Reserved for a future service supervisor integration. |
| `-EnableBash` | off | Adds `bash` only when `bash.exe` is found. |

## Skills

Default advertised skills:

| Skill | Purpose |
|---|---|
| `powershell` | Run explicit PowerShell scripts through the native executor. |
| `file` | Read, write, and stat files through native code. |
| `permissions` | Inspect or update agent permissions through native code. |

`chat` and `chat_resume` are advertised only when the selected `-Agent`
command (`codex` or `claude`) is found on `PATH`.

`bash` is advertised only with `-EnableBash` and a discoverable `bash.exe`.

## Reset

Remove `%USERPROFILE%\.loom\<Name>`.
