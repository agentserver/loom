# windows-driver

Generic Windows project setup for `driver-agent`. The driver is launched by
Codex or Claude as an MCP server; it is not installed as a Windows service.

Do not paste secrets or tokens into chat, terminal output, issue comments, or
logs. Use placeholder examples until you are editing your local config.

## Prereqs

1. Put `driver-agent.windows-amd64.exe` in `deploy/windows/bin/`, or pass
   `-Bin C:\path\to\driver-agent.windows-amd64.exe`.
2. Install the agent CLI you plan to use: `codex` or `claude`.
3. Choose a project directory where the MCP registration should live.

## Foreground Workflow

```powershell
.\install.ps1 `
  -Project "$env:USERPROFILE\loom-driver" `
  -Name "driver-windows" `
  -ObserverUrl "http://observer.example.com:8090" `
  -Workspace "ws-default" `
  -Agent codex
```

The installer stages:

| File | Purpose |
|---|---|
| `driver-agent.exe` | Windows amd64 driver binary |
| `config.yaml` | Driver config with escaped Windows paths |
| `.codex\config.toml` | Codex MCP registration when `-Agent codex` |
| `.mcp.json` | Claude MCP registration when `-Agent claude` |
| `AGENTS.md` or `.claude\skills\` | Optional project bundle when available |
| `logs\` | Driver audit logs |

After install, run the one-time registration command printed by the script:

```powershell
& "$env:USERPROFILE\loom-driver\driver-agent.exe" register --config "$env:USERPROFILE\loom-driver\config.yaml"
```

Then start Codex or Claude from the project directory:

```powershell
Set-Location "$env:USERPROFILE\loom-driver"
codex
```

## Parameters

| Parameter | Default | Notes |
|---|---|---|
| `-Project` | required | Project directory to create or update. |
| `-Name` | required | Agent display name and local config identity. |
| `-ObserverUrl` | required | Observer URL written into `observer.url`. |
| `-Workspace` | `ws-default` | Workspace ID written into the template. |
| `-Agent` | `codex` | `codex` or `claude`. |
| `-ApiKey` | empty | Optional local-only value for `observer.api_key`. |
| `-Bin` | `..\bin\driver-agent.windows-amd64.exe` | Override binary source path. |
| `-TokenDir` | `%USERPROFILE%\.loom\<Name>` | Parent directory for observer token state if observer is later enabled. |

## Reset

Remove the project directory and `%USERPROFILE%\.loom\<Name>` to start over.
