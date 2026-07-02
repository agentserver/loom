# deploy/windows — one-key stack bring-up (Windows)

`deploy.ps1` is the Windows counterpart of
[`../linux/deploy.sh`](../linux/deploy.sh). Same four processes, same
CLI shape (PowerShell-native switches), same JSON topology emit.
Requires PowerShell 7.4+.

- **Spec**: [`../../../docs/specs/wt2-deploy-scripts.spec.md`](../../../docs/specs/wt2-deploy-scripts.spec.md)
- **Plan**: [`../../../docs/specs/wt2-deploy-scripts.plan.md`](../../../docs/specs/wt2-deploy-scripts.plan.md)
- **Boundary**: this script owns the *orchestrator*. It never modifies
  `slave/install.ps1` (owned by WT-0-windows-slave) or `driver/install.ps1`
  (pre-existing); it invokes them as subprocesses in stub mode, and
  bypasses them entirely in prod mode.

---

## Quick start

```powershell
# Build every binary the stack needs (from a Linux host with Go — the
# Windows binaries cross-compile trivially).
$env:GOOS = 'windows'; $env:GOARCH = 'amd64'
foreach ($cmd in 'driver-agent','slave-agent','observer-server') {
    go build -o "deploy/windows/bin/${cmd}.windows-amd64.exe" "./cmd/${cmd}"
}
go build -o deploy/windows/bin/agentserver-stub.windows-amd64.exe ./tools/eval/agentserver-stub

# Stub mode (default): brings up all 4 processes against the local
# agentserver-stub — no OAuth device flow.
pwsh -NoProfile -ExecutionPolicy Bypass -File deploy/windows/deploy.ps1 -Stub

# Plan-only (no side effects):
pwsh -NoProfile -ExecutionPolicy Bypass -File deploy/windows/deploy.ps1 -Stub -DryRun
```

Successful `-Stub` exits 0 with PID files under
`$env:USERPROFILE\.loom\eval-deploy\.pids\` and the machine_topology
JSON on stdout.

## ExecutionPolicy — never modify globally

On locked-down Windows machines you may see:

```
File deploy.ps1 cannot be loaded because running scripts is disabled
on this system.
```

The **correct** answer is the per-process flag shown above:

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File deploy.ps1 -Stub
```

That sets `Bypass` for the one `pwsh` invocation only. **Do not** run
`Set-ExecutionPolicy Bypass -Scope CurrentUser` or `-Scope
LocalMachine` — those persist across every future script the shell
loads, permanently weakening the machine. `deploy.ps1` contains **no**
`Set-ExecutionPolicy` call at any scope (Pester test T14 enforces
this).

The script is shipped **unsigned** (no Authenticode certificate for
this project). Pester test T14 also asserts
`(Get-AuthenticodeSignature deploy.ps1).Status -eq 'NotSigned'` — a
future release-signing PR that flips this baseline will also update
that assertion.

## Teardown

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File deploy/windows/deploy.ps1 -Shutdown
# reads $LoomHome\.pids\*.pid, Stop-Process -Force, 5s grace, removes .pids
```

## CLI reference

```
deploy.ps1 [-Stub | -Prod | -Mode {stub|prod}] `
           [-ObserverPort 18091] [-DriverPort 18092] [-SlavePort 18093] `
           [-StubPort 18080] [-LoomHome DIR] [-BinDir DIR] `
           [-DryRun] [-AllowModelKeyPassthrough] `
           [-TopologyOut PATH]

deploy.ps1 -Shutdown [-LoomHome DIR]
```

Behavioural notes (see [`../linux/README.md`](../linux/README.md) for
the full reference — all semantics are identical):

- **Default mode = stub** (matches spec §3.2 and todo_list line 105:
  `pwsh deploy.ps1 --stub` = `pwsh deploy.ps1` = stub bring-up).
- **`-Prod` is spawn-only** — never invokes `slave/install.ps1` or
  `driver/install.ps1`, because their `Set-Content -LiteralPath
  $configPath` would rewrite the operator-registered
  `credentials.proxy_token` back to `""` (spec §7(b), P0-B fix). Prod
  preflight throws on missing `observer/observer.yaml`,
  `slave/config.yaml` credentials, `driver/config.yaml` credentials.
- **Windows-native readiness probes**: `TcpClient` for LISTEN gates,
  `Invoke-WebRequest -SkipHttpErrorCheck` for HTTP any-response gates,
  `Invoke-WebRequest -Headers @{Authorization='Bearer …'}` for the
  stub-mode slave `/whoami` gate.
- **Topology emit**: PowerShell-native (`Win32_ComputerSystem` for
  `TotalPhysicalMemory`, `[Environment]::ProcessorCount` for
  `cpu_count`). Payload validates against
  [`../topology_schema.json`](../topology_schema.json) — the schema
  is OS-agnostic.

## Security notes (mirrors spec §7 — identical to Linux side)

See [`../linux/README.md#security-notes`](../linux/README.md) for the
full list. Windows-specific implementation choices:

- **(g) env whitelist**: cleared via
  `[System.Diagnostics.ProcessStartInfo]::EnvironmentVariables.Clear()`
  followed by explicit `.Add(k, v)` for whitelisted keys — NOT
  `Start-Process -Environment`, which inherits by default.
- **PID file 0600 analog**: NTFS ACL restricted to the current user
  via `icacls`-style Set-Acl in `deploy.ps1`; the read/write bits are
  the closest POSIX equivalent Windows offers.

## Tests

```powershell
# Pester 5.x — runs on both Windows and Linux (via pwsh) thanks to
# Set-ItResult -Skipped -Because 'requires Windows' guards on native
# rows.
pwsh -c 'Invoke-Pester deploy/windows/deploy.Tests.ps1 -Output Detailed'
```

Any-host rows exercised on Linux CI include the AST-based T9b-ps
(§7(b) static guard — asserts no `install.ps1` invocation inside a
non-Stub-guarded code path) and every source-grep invariant
(T7-ps-a..d, T14, T15d-ps, T10-ps).

## Linux counterpart

See [`../linux/README.md`](../linux/README.md).
