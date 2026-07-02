<#
.SYNOPSIS
    WT-2-deploy-scripts one-key stack bring-up for Windows.

.DESCRIPTION
    Windows counterpart of multi-agent/deploy/linux/deploy.sh. See
    docs/specs/wt2-deploy-scripts.spec.md §3.2 for the parameter shape
    and §4.2 for the mode-conditional bring-up tables. Never modifies
    deploy/windows/slave/install.ps1 (WT-0-windows-slave boundary).

.PARAMETER Stub
    Bring up in stub mode against agentserver-stub on 127.0.0.1.
.PARAMETER Prod
    Bring up in prod mode against pre-registered $LoomHome/*.
.PARAMETER Mode
    Alias for -Stub / -Prod ('stub' or 'prod').
.PARAMETER ObserverPort
    Local observer TCP port. Default 18091.
.PARAMETER DriverPort
    Local driver-agent serve-daemon TCP port. Default 18092.
.PARAMETER SlavePort
    Local slave-agent daemon TCP port. Default 18093.
.PARAMETER StubPort
    agentserver-stub TCP port (stub mode only). Default 18080.
.PARAMETER LoomHome
    Install/runtime dir root. Default $env:USERPROFILE\.loom\eval-deploy.
.PARAMETER BinDir
    Prebuilt Windows binary cache dir. Default $PSScriptRoot\bin.
.PARAMETER DryRun
    Print resolved plan as JSON, exit 0.
.PARAMETER AllowModelKeyPassthrough
    Prod-only opt-in for OPENAI_API_KEY / ANTHROPIC_API_KEY passthrough.
.PARAMETER TopologyOut
    Write topology JSON to PATH (0600-analogue via icacls) instead of
    stdout final line.
.PARAMETER Shutdown
    Reap PIDs recorded in $LoomHome\.pids\*.pid, then exit.

.NOTES
    * First executable line: $ErrorActionPreference = 'Stop' (§7(c)).
    * Never modifies deploy/windows/slave/install.ps1 (§0 boundary).
    * Stub bind is hard-coded 127.0.0.1:$StubPort (§7(a)); no env
      override channel of any kind.
    * Prod mode is spawn-only — never invokes install.ps1 (§7(b) —
      install.ps1 unconditionally rewrites config.yaml from a blank
      template, which would clobber operator-registered credentials).
    * Env whitelist: PATH / HOME / LANG / LC_ALL / TZ / USER always;
      AGENTSERVER_ROOT / MODELSERVER_ROOT / APP_ROOT / MOCK_MODEL_URL
      if-set; LOOM_* prefix; OPENAI_API_KEY / ANTHROPIC_API_KEY only
      when prod + -AllowModelKeyPassthrough (WARN logged).
#>

[CmdletBinding(DefaultParameterSetName = 'ModeSwitch')]
param(
    [Parameter(ParameterSetName = 'ModeSwitch')]
    [switch]$Stub,

    [Parameter(ParameterSetName = 'ModeSwitch')]
    [switch]$Prod,

    [Parameter(ParameterSetName = 'ModeString', Mandatory = $true)]
    [ValidateSet('stub', 'prod')]
    [string]$Mode,

    [Parameter(ParameterSetName = 'ShutdownSet', Mandatory = $true)]
    [switch]$Shutdown,

    [ValidateRange(1024, 65535)]
    [int]$ObserverPort = 18091,
    [ValidateRange(1024, 65535)]
    [int]$DriverPort = 18092,
    [ValidateRange(1024, 65535)]
    [int]$SlavePort = 18093,
    [ValidateRange(1024, 65535)]
    [int]$StubPort = 18080,

    [string]$LoomHome = '',
    [string]$BinDir = '',
    [switch]$DryRun,
    [switch]$AllowModelKeyPassthrough,
    [string]$TopologyOut = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Default exit code for unclassified failures. Each throw site sets
# $script:ExitCode to the appropriate value (2 = preflight, 3 =
# sub-installer, 4 = readiness, 5 = topology). The outer catch reads
# this and exits with the classified code. The main body below is
# wrapped in a top-level try/catch that starts BEFORE the CLI preflight
# checks (mode-conflict / port range / port-collision / model-key
# gate) so those throws produce the classified exit code, not the
# default PowerShell error exit (P1-1 fix, Codex rounds 2+3).
$script:ExitCode = 2

# Track whether the operator explicitly supplied each port (mirrors
# the Linux *_PORT_SET flags). Prod preflight below uses these to
# honor the "omitted → adopt operator's yaml value" contract in
# spec §4.2 prod row (P1-3 fix, Codex round 2).
$script:ObserverPortSet = $PSBoundParameters.ContainsKey('ObserverPort')
$script:SlavePortSet    = $PSBoundParameters.ContainsKey('SlavePort')
$script:DriverPortSet   = $PSBoundParameters.ContainsKey('DriverPort')

# --- top-level trap for classified preflight exits --------------------
# Registered here so any throw between here and the main try/catch
# below (particularly the CLI preflight in Resolve-Mode, Assert-Port,
# port-collision, and model-key checks) exits with the classified code
# in $script:ExitCode instead of the default PowerShell error exit.
trap {
    [System.Console]::Error.WriteLine("deploy.ps1: $_")
    exit $script:ExitCode
}

# --- constants ---------------------------------------------------------

$script:WELL_KNOWN_PORTS = @(22, 23, 25, 53, 80, 110, 143, 443, 465, 587, 993, 995, 3389, 5432, 6379, 8080, 8443)
$script:ALWAYS_ENV_KEYS = @('PATH', 'HOME', 'LANG', 'LC_ALL', 'TZ', 'USER')
$script:IFSET_ENV_KEYS  = @('AGENTSERVER_ROOT', 'MODELSERVER_ROOT', 'APP_ROOT', 'MOCK_MODEL_URL')
$script:READY_TIMEOUT_DEFAULT = 30

# --- mode resolution ---------------------------------------------------

function Resolve-Mode {
    if ($PSCmdlet.ParameterSetName -eq 'ShutdownSet') { return 'shutdown' }
    if ($PSCmdlet.ParameterSetName -eq 'ModeString')  { return $Mode }
    # ModeSwitch: at most one of -Stub / -Prod may be set.
    if ($Stub -and $Prod) {
        throw "conflicting mode flags: -Stub and -Prod both supplied"
    }
    if ($Prod) { return 'prod' }
    return 'stub'   # default when neither switch is present
}

$script:ResolvedMode = Resolve-Mode

# --- LoomHome / BinDir defaults ---------------------------------------

if ([string]::IsNullOrEmpty($LoomHome)) {
    $home_dir = if ($env:USERPROFILE) { $env:USERPROFILE } else { $env:HOME }
    $LoomHome = Join-Path $home_dir '.loom\eval-deploy'
}
# --dry-run must be side-effect free (spec §3.3); create the dir only
# in the actual spawn path below.
if (Test-Path -LiteralPath $LoomHome) {
    $LoomHome = (Resolve-Path -LiteralPath $LoomHome).Path
}

if ([string]::IsNullOrEmpty($BinDir)) {
    $BinDir = Join-Path $PSScriptRoot 'bin'
}

$PidsDir = Join-Path $LoomHome '.pids'

# --- shutdown mode -----------------------------------------------------

if ($ResolvedMode -eq 'shutdown') {
    if (-not (Test-Path $PidsDir)) {
        Write-Host "deploy.ps1: no .pids under $LoomHome; nothing to shut down"
        exit 0
    }
    $reaped = 0
    Get-ChildItem -Path $PidsDir -Filter '*.pid' | ForEach-Object {
        $pid_val = Get-Content -LiteralPath $_.FullName -ErrorAction SilentlyContinue
        if ($pid_val) {
            $proc = Get-Process -Id $pid_val -ErrorAction SilentlyContinue
            if ($proc) {
                Stop-Process -Id $pid_val -Force -ErrorAction SilentlyContinue
                $reaped++
            }
        }
    }
    # Grace 5 s (Stop-Process is synchronous but children may take time).
    Start-Sleep -Seconds 5
    Remove-Item -Recurse -Force -Path $PidsDir -ErrorAction SilentlyContinue
    Write-Host "deploy.ps1: shutdown complete ($reaped process(es) signalled)"
    exit 0
}

# --- port validation (§7(e)) ------------------------------------------

function Assert-Port {
    param([string]$Name, [int]$Value)
    if ($script:WELL_KNOWN_PORTS -contains $Value) {
        throw "${Name}=${Value} is a well-known port (blacklist: $($script:WELL_KNOWN_PORTS -join ','))"
    }
}
Assert-Port -Name '-ObserverPort' -Value $ObserverPort
Assert-Port -Name '-DriverPort'   -Value $DriverPort
Assert-Port -Name '-SlavePort'    -Value $SlavePort
if ($ResolvedMode -eq 'stub') { Assert-Port -Name '-StubPort' -Value $StubPort }

# Pairwise distinct.
$ports = @{
    'observer' = $ObserverPort
    'driver'   = $DriverPort
    'slave'    = $SlavePort
}
if ($ResolvedMode -eq 'stub') { $ports['stub'] = $StubPort }
$seen = @{}
foreach ($kv in $ports.GetEnumerator()) {
    if ($seen.ContainsKey($kv.Value)) {
        throw "port $($kv.Value) used by both $($seen[$kv.Value]) and $($kv.Key)"
    }
    $seen[$kv.Value] = $kv.Key
}

# Stub + AllowModelKeyPassthrough combination is a preflight error
# (§7(g); stub has no model plane).
if ($ResolvedMode -eq 'stub' -and $AllowModelKeyPassthrough) {
    throw "-AllowModelKeyPassthrough is only valid with -Prod (spec §7(g); the stub has no model plane)"
}

# --- env whitelist (§7(g)) --------------------------------------------

function Get-WhitelistedEnv {
    param([string]$Mode, [switch]$AllowModelKey)
    $out = @{}
    foreach ($k in $script:ALWAYS_ENV_KEYS) {
        $v = [Environment]::GetEnvironmentVariable($k)
        if ($null -eq $v) { $v = '' }
        $out[$k] = $v
    }
    foreach ($k in $script:IFSET_ENV_KEYS) {
        $v = [Environment]::GetEnvironmentVariable($k)
        if (-not [string]::IsNullOrEmpty($v)) {
            $out[$k] = $v
        }
    }
    # LOOM_* prefix (require ≥1 char after prefix).
    foreach ($e in [Environment]::GetEnvironmentVariables().GetEnumerator()) {
        $k = [string]$e.Key
        if ($k.Length -gt 5 -and $k.StartsWith('LOOM_')) {
            $out[$k] = [string]$e.Value
        }
    }
    if ($Mode -eq 'prod' -and $AllowModelKey) {
        foreach ($k in 'OPENAI_API_KEY', 'ANTHROPIC_API_KEY') {
            $v = [Environment]::GetEnvironmentVariable($k)
            if (-not [string]::IsNullOrEmpty($v)) {
                Write-Host "deploy.ps1: passing $k through to subprocesses" -InformationAction Ignore
                [System.Console]::Error.WriteLine("deploy.ps1: passing $k through to subprocesses")
                $out[$k] = $v
            }
        }
    }
    return $out
}

# --- dry-run branch ---------------------------------------------------

function Get-RedactedHost {
    $raw = if ($env:LOOM_TEST_HOSTNAME) { $env:LOOM_TEST_HOSTNAME } else { [System.Net.Dns]::GetHostName() }
    $h1 = { param($s)
        $sha = [System.Security.Cryptography.SHA256]::Create()
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($s)
        $hash = $sha.ComputeHash($bytes)
        return -join ($hash[0..3] | ForEach-Object { $_.ToString('x2') })
    }
    if ($raw -match '@') {
        $left = $raw.Substring(0, $raw.LastIndexOf('@'))
        $right = $raw.Substring($raw.LastIndexOf('@') + 1)
        return "$(& $h1 $left)@$(& $h1 $right)"
    }
    return & $h1 $raw
}

function Get-PlannedCommands {
    if ($ResolvedMode -eq 'stub') {
        return @(
            @((Join-Path $BinDir 'agentserver-stub.windows-amd64.exe'), '--listen', "127.0.0.1:$StubPort", '--workspace-id', 'auto'),
            @('<observer-inline-render>', (Join-Path $LoomHome 'observer\observer.yaml'), '--api-key', '<REDACTED>'),
            @((Join-Path $LoomHome 'observer\observer-server.exe'), '-config', (Join-Path $LoomHome 'observer\observer.yaml')),
            @((Join-Path $PSScriptRoot 'slave\install.ps1'), '-Name', 'eval-slave', '-ObserverUrl', "http://127.0.0.1:$ObserverPort", '-Workspace', 'ws-eval-auto', '-LoomHome', (Join-Path $LoomHome 'slave'), '-Bin', (Join-Path $BinDir 'slave-agent.windows-amd64.exe')),
            @('<yq-patch>', (Join-Path $LoomHome 'slave\config.yaml'), "server.url=http://127.0.0.1:$StubPort", 'credentials.*=<REDACTED>', 'daemon.auto_start=false', "daemon.listen=127.0.0.1:$SlavePort"),
            @((Join-Path $LoomHome 'slave\slave-agent.exe'), (Join-Path $LoomHome 'slave\config.yaml')),
            @((Join-Path $PSScriptRoot 'driver\install.ps1'), '-Project', (Join-Path $LoomHome 'driver'), '-Name', 'eval-driver', '-ObserverUrl', "http://127.0.0.1:$ObserverPort", '-Bin', (Join-Path $BinDir 'driver-agent.windows-amd64.exe')),
            @('<yq-patch>', (Join-Path $LoomHome 'driver\config.yaml'), "server.url=http://127.0.0.1:$StubPort", 'credentials.*=<REDACTED>'),
            @((Join-Path $LoomHome 'driver\driver-agent.exe'), 'serve-daemon', '--config', (Join-Path $LoomHome 'driver\config.yaml'), '--listen', "127.0.0.1:$DriverPort")
        )
    } else {
        return @(
            @((Join-Path $LoomHome 'observer\observer-server.exe'), '-config', (Join-Path $LoomHome 'observer\observer.yaml')),
            @((Join-Path $LoomHome 'slave\slave-agent.exe'), (Join-Path $LoomHome 'slave\config.yaml')),
            @((Join-Path $LoomHome 'driver\driver-agent.exe'), 'serve-daemon', '--config', (Join-Path $LoomHome 'driver\config.yaml'), '--listen', "127.0.0.1:$DriverPort")
        )
    }
}

function Get-ComponentPorts {
    # Build the ordered dict incrementally rather than using the
    # `[ordered]@{...} + [ordered]@{...}` merge operator (which raises
    # under strict mode on some PowerShell 7.x combinations).
    $cp = [ordered]@{}
    if ($ResolvedMode -eq 'stub') {
        $cp['agentserver_stub'] = $StubPort
    }
    $cp['observer'] = $ObserverPort
    $cp['driver']   = $DriverPort
    $cp['slave']    = $SlavePort
    return $cp
}

function Emit-DryRun {
    $osName = if ($IsWindows) { 'windows' } elseif ($IsLinux) { 'linux' } elseif ($IsMacOS) { 'darwin' } else { 'linux' }
    $arch   = ($env:PROCESSOR_ARCHITECTURE) 2>$null
    if ([string]::IsNullOrEmpty($arch)) { $arch = 'amd64' }
    else {
        switch -Regex ($arch) {
            'AMD64|x86_64' { $arch = 'amd64' }
            'ARM64|aarch64' { $arch = 'arm64' }
        }
    }
    $obj = [ordered]@{
        mode                  = $ResolvedMode
        host                  = Get-RedactedHost
        os                    = $osName
        arch                  = $arch
        component_ports       = Get-ComponentPorts
        planned_commands      = Get-PlannedCommands
        planned_env_whitelist = [ordered]@{
            always = $script:ALWAYS_ENV_KEYS
            if_set = $script:IFSET_ENV_KEYS
            prefix = @('LOOM_*')
        }
        loom_home             = $LoomHome
        bin_dir               = $BinDir
    }
    # Redact any --api-key / --token / --secret / --password / --bearer
    # argv positions (§7(h)).
    $secretFlags = @('--api-key', '--token', '--secret', '--password', '--bearer',
                     '-ApiKey', '-Token', '-Secret', '-Password', '-Bearer')
    for ($i = 0; $i -lt $obj.planned_commands.Count; $i++) {
        $cmd = $obj.planned_commands[$i]
        for ($j = 0; $j -lt $cmd.Count - 1; $j++) {
            if ($secretFlags -contains $cmd[$j]) {
                $cmd[$j+1] = '<REDACTED>'
            }
        }
        $obj.planned_commands[$i] = $cmd
    }
    $json = $obj | ConvertTo-Json -Depth 8
    Write-Output $json
}

if ($DryRun) {
    Emit-DryRun
    exit 0
}

# --- non-dry-run: preflight (prod) ------------------------------------

function Test-ProdPreflight {
    # --- observer -----------------------------------------------------
    $obs_yaml = Join-Path $LoomHome 'observer\observer.yaml'
    if (-not (Test-Path -PathType Leaf -LiteralPath $obs_yaml)) {
        $script:ExitCode = 2; throw "prod preflight failed: $obs_yaml missing; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
    $obs_bin = Join-Path $LoomHome 'observer\observer-server.exe'
    if (-not (Test-Path -PathType Leaf -LiteralPath $obs_bin)) {
        $script:ExitCode = 2; throw "prod preflight failed: $obs_bin missing; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
    # observer.yaml must have listen_addr matching -ObserverPort. See
    # deploy.sh prod_preflight() for the rationale (empty listen_addr
    # would let observer-server pick a random port, hiding the actual
    # misconfiguration behind an exit-4 timeout).
    $obs_cfg = Get-Content -Raw -LiteralPath $obs_yaml
    if (-not ($obs_cfg -match '(?m)^\s*listen_addr\s*:\s*["'']?([^"''\s]+)')) {
        $script:ExitCode = 2; throw "prod preflight failed: $obs_yaml listen_addr is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
    $obs_listen = $Matches[1]
    if ($obs_listen -match ':(\d+)$') {
        $obs_port = [int]$Matches[1]
        if ($script:ObserverPortSet) {
            if ($obs_port -ne $ObserverPort) {
                $script:ExitCode = 2; throw "prod preflight failed: operator-registered observer uses port $obs_port; -ObserverPort $ObserverPort must match or be omitted"
            }
        } else {
            $script:ObserverPort = $obs_port
            Set-Variable -Scope Script -Name ObserverPort -Value $obs_port -Force
        }
    }

    # --- slave --------------------------------------------------------
    $slave_yaml = Join-Path $LoomHome 'slave\config.yaml'
    if (-not (Test-Path -PathType Leaf -LiteralPath $slave_yaml)) {
        $script:ExitCode = 2; throw "prod preflight failed: $slave_yaml missing; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
    $slave_cfg = Get-Content -Raw -LiteralPath $slave_yaml
    foreach ($field in 'proxy_token', 'short_id', 'workspace_id') {
        if (-not ($slave_cfg -match "(?m)^\s*${field}\s*:\s*[\`"']?\S+")) {
            $script:ExitCode = 2; throw "prod preflight failed: $slave_yaml credentials.$field is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
        }
    }
    # daemon.listen required (same rationale as deploy.sh: empty means
    # the readiness gate waits on the wrong port).
    if (-not ($slave_cfg -match '(?m)^\s*listen\s*:\s*["'']?([^"''\s]+)')) {
        $script:ExitCode = 2; throw "prod preflight failed: $slave_yaml daemon.listen is empty; the readiness gate needs an explicit port; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
    $slave_listen = $Matches[1]
    if ($slave_listen -match ':(\d+)$') {
        $slave_port_val = [int]$Matches[1]
        if ($script:SlavePortSet) {
            if ($slave_port_val -ne $SlavePort) {
                $script:ExitCode = 2; throw "prod preflight failed: operator-registered slave uses port $slave_port_val; -SlavePort $SlavePort must match or be omitted"
            }
        } else {
            Set-Variable -Scope Script -Name SlavePort -Value $slave_port_val -Force
        }
    }
    $slave_bin = Join-Path $LoomHome 'slave\slave-agent.exe'
    if (-not (Test-Path -PathType Leaf -LiteralPath $slave_bin)) {
        $script:ExitCode = 2; throw "prod preflight failed: $slave_bin missing; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }

    # --- driver -------------------------------------------------------
    $driver_yaml = Join-Path $LoomHome 'driver\config.yaml'
    if (-not (Test-Path -PathType Leaf -LiteralPath $driver_yaml)) {
        $script:ExitCode = 2; throw "prod preflight failed: $driver_yaml missing; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
    $driver_cfg = Get-Content -Raw -LiteralPath $driver_yaml
    foreach ($field in 'proxy_token', 'short_id') {
        if (-not ($driver_cfg -match "(?m)^\s*${field}\s*:\s*[\`"']?\S+")) {
            $script:ExitCode = 2; throw "prod preflight failed: $driver_yaml credentials.$field is empty; see tests/prod_test/E2E_RUNBOOK.md:83-108"
        }
    }
    $driver_bin = Join-Path $LoomHome 'driver\driver-agent.exe'
    if (-not (Test-Path -PathType Leaf -LiteralPath $driver_bin)) {
        $script:ExitCode = 2; throw "prod preflight failed: $driver_bin missing; see tests/prod_test/E2E_RUNBOOK.md:83-108"
    }
}

# --- lifecycle scaffolding --------------------------------------------

$script:SpawnedPids = New-Object System.Collections.Generic.List[int]
$script:StageFailed = $false

function Invoke-Cleanup-OnFailure {
    if (-not $script:StageFailed) { return }
    # Write-Warning (not Write-Error) — Write-Error under
    # $ErrorActionPreference='Stop' would throw here and cut cleanup
    # short before we reap PIDs (P1-1 fix, Codex round 2).
    [System.Console]::Error.WriteLine("deploy.ps1: failure — reaping spawned processes")
    foreach ($p in $script:SpawnedPids) {
        try { Stop-Process -Id $p -Force -ErrorAction SilentlyContinue } catch {}
    }
    Start-Sleep -Seconds 3
    Remove-Item -Recurse -Force -Path $PidsDir -ErrorAction SilentlyContinue
}

function Initialize-PidsDir {
    # Called only from the non-dry-run bring-up path. Creates $LoomHome
    # + $PidsDir on disk and restricts the .pids ACL to the current
    # user. --dry-run must not reach this function (spec §3.3
    # no-side-effects contract).
    $null = New-Item -ItemType Directory -Force -Path $LoomHome
    $null = New-Item -ItemType Directory -Force -Path $PidsDir
    try {
        $acl = Get-Acl -Path $PidsDir
        $acl.SetAccessRuleProtection($true, $false)
        $user = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($user, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.ResetAccessRule($rule)
        Set-Acl -Path $PidsDir -AclObject $acl
    } catch {
        # Non-fatal on non-Windows hosts (Pester-on-Linux exercise).
    }
}

function Invoke-Whitelisted {
    # Foreground exec of a helper subprocess (agentserver-stub issue,
    # per-role installers when they take env-sensitive params) with the
    # same env-clear + explicit .Add() pattern Start-Sub applies.
    # Returns the subprocess stdout as a string. Bypassing this and
    # running `& $bin ...` inherits the current PowerShell process's
    # full env (AWS_*, GITHUB_TOKEN, OPENAI_API_KEY, …) — §7(g)
    # requires the whitelist for EVERY spawned subprocess (P0-1 fix,
    # Codex round 2).
    param([string]$FilePath, [string[]]$ArgList)
    $env_map = Get-WhitelistedEnv -Mode $ResolvedMode -AllowModelKey:$AllowModelKeyPassthrough
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $FilePath
    foreach ($a in $ArgList) { $null = $psi.ArgumentList.Add($a) }
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError  = $true
    $psi.UseShellExecute = $false
    $psi.EnvironmentVariables.Clear()
    foreach ($kv in $env_map.GetEnumerator()) { $psi.EnvironmentVariables.Add($kv.Key, $kv.Value) }
    $proc = [System.Diagnostics.Process]::Start($psi)
    $stdout = $proc.StandardOutput.ReadToEnd()
    $stderr = $proc.StandardError.ReadToEnd()
    $proc.WaitForExit()
    if ($proc.ExitCode -ne 0) {
        throw "Invoke-Whitelisted: $FilePath returned $($proc.ExitCode): $stderr"
    }
    return $stdout
}

function Start-Sub {
    param([string]$Role, [string]$LogPath, [string]$FilePath, [string[]]$ArgList)
    $null = New-Item -ItemType Directory -Force -Path (Split-Path $LogPath)
    $env_map = Get-WhitelistedEnv -Mode $ResolvedMode -AllowModelKey:$AllowModelKeyPassthrough
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $FilePath
    foreach ($a in $ArgList) { $null = $psi.ArgumentList.Add($a) }
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError  = $true
    $psi.UseShellExecute = $false
    # Cleared env + explicit whitelist.
    $psi.EnvironmentVariables.Clear()
    foreach ($kv in $env_map.GetEnumerator()) {
        $psi.EnvironmentVariables.Add($kv.Key, $kv.Value)
    }
    $proc = [System.Diagnostics.Process]::Start($psi)
    $script:SpawnedPids.Add($proc.Id) | Out-Null
    $pf = Join-Path $PidsDir "$Role.pid"
    Set-Content -LiteralPath $pf -Value $proc.Id
    Write-Host "deploy.ps1: spawned $Role pid=$($proc.Id) (log: $LogPath)"
}

function Wait-TcpListen {
    param([string]$Address = '127.0.0.1', [int]$Port, [int]$TimeoutSec = $script:READY_TIMEOUT_DEFAULT)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $c = New-Object System.Net.Sockets.TcpClient
            $ar = $c.BeginConnect($Address, $Port, $null, $null)
            if ($ar.AsyncWaitHandle.WaitOne(1000)) {
                $c.EndConnect($ar)
                $c.Close()
                return $true
            }
            $c.Close()
        } catch {}
        Start-Sleep -Milliseconds 200
    }
    return $false
}

function Wait-HttpAny {
    param([string]$Url, [int]$TimeoutSec = 5)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2 -SkipHttpErrorCheck -ErrorAction SilentlyContinue
            if ($r) { return $true }
        } catch {
            if ($_.Exception.Response) { return $true }
        }
        Start-Sleep -Milliseconds 200
    }
    return $false
}

# --- bring-up: stub ---------------------------------------------------

function Invoke-BringupStub {
    $stub_bin = Join-Path $BinDir 'agentserver-stub.windows-amd64.exe'
    if (-not (Test-Path -PathType Leaf -LiteralPath $stub_bin)) {
        throw "$stub_bin not executable; build with: GOOS=windows GOARCH=amd64 go build -o deploy/windows/bin/agentserver-stub.windows-amd64.exe ./tools/eval/agentserver-stub"
    }
    Start-Sub -Role 'agentserver-stub' -LogPath (Join-Path $LoomHome 'logs\agentserver-stub.log') `
              -FilePath $stub_bin -ArgList @('--listen', "127.0.0.1:$StubPort", '--workspace-id', 'auto')
    if (-not (Wait-TcpListen -Port $StubPort)) { $script:StageFailed = $true; $script:ExitCode = 4; throw "stub :$StubPort did not LISTEN" }

    # observer inline-render (§3.2 clause 2).
    $obs_dir = Join-Path $LoomHome 'observer'
    $null = New-Item -ItemType Directory -Force -Path $obs_dir
    $tpl = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'observer\config.yaml.template')
    # Strip the "duplicated from" header line.
    $tpl = ($tpl -split "`n" | Where-Object { $_ -notmatch '^# duplicated from' }) -join "`n"
    $apikey = -join ((1..16) | ForEach-Object { '{0:x2}' -f (Get-Random -Minimum 0 -Maximum 256) })
    $tpl = $tpl.Replace('__LISTEN_ADDR__', "127.0.0.1:$ObserverPort")
    $tpl = $tpl.Replace('__LOOM_HOME__', $obs_dir)
    $tpl = $tpl.Replace('__WS_APIKEY__', $apikey)
    Set-Content -LiteralPath (Join-Path $obs_dir 'observer.yaml') -Value $tpl -Encoding utf8
    # Copy the arch-suffixed prebuilt into the installed dir under the
    # stable name observer-server.exe so the subsequent Start-Sub and
    # any operator-side re-invocation both target one canonical path
    # (matches driver/slave installers which likewise rename to
    # driver-agent.exe / slave-agent.exe).
    Copy-Item -LiteralPath (Join-Path $BinDir 'observer-server.windows-amd64.exe') `
              -Destination (Join-Path $obs_dir 'observer-server.exe') -Force
    Start-Sub -Role 'observer' -LogPath (Join-Path $LoomHome 'logs\observer.log') `
              -FilePath (Join-Path $obs_dir 'observer-server.exe') `
              -ArgList @('-config', (Join-Path $obs_dir 'observer.yaml'))
    if (-not (Wait-TcpListen -Port $ObserverPort)) { $script:StageFailed = $true; $script:ExitCode = 4; throw "observer :$ObserverPort did not LISTEN" }

    # slave: real install.ps1 (WT-0-owned; do NOT modify) with its real
    # params. Invoke via `pwsh -NoProfile -File` so the child inherits
    # only the whitelisted env — the same posture Start-Sub applies to
    # daemons. `& install.ps1` inside our process would inherit our
    # full env (§7(g) violation). $script:ExitCode = 3 marks the
    # failure as sub-installer.
    try {
        Invoke-Whitelisted -FilePath 'pwsh' -ArgList @(
            '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File',
            (Join-Path $PSScriptRoot 'slave\install.ps1'),
            '-Name', 'eval-slave',
            '-ObserverUrl', "http://127.0.0.1:$ObserverPort",
            '-Workspace', 'ws-eval-auto',
            '-LoomHome', (Join-Path $LoomHome 'slave'),
            '-Bin', (Join-Path $BinDir 'slave-agent.windows-amd64.exe')
        ) | Out-Null
    } catch {
        $script:StageFailed = $true; $script:ExitCode = 3; throw "slave install.ps1 failed: $_"
    }
    # Post-process config.yaml — server.url, credentials, daemon.auto_start,
    # daemon.listen. Implemented via plain regex-replace + append since Windows
    # doesn't ship yq by default; deploy.ps1's YAML edits are constrained to
    # scalar leaf fields we can safely rewrite by line.
    Update-SlaveConfigStubMode -SlaveConfigPath (Join-Path $LoomHome 'slave\config.yaml') `
                               -StubBin $stub_bin
    Start-Sub -Role 'slave' -LogPath (Join-Path $LoomHome 'logs\slave.log') `
              -FilePath (Join-Path $LoomHome 'slave\slave-agent.exe') `
              -ArgList @((Join-Path $LoomHome 'slave\config.yaml'))
    # Stub-mode slave readiness gate: HTTP whoami round-trip with slave.proxy_token
    # (§4.2 stub table). We captured proxy_token in the yaml above.
    $slave_token = Get-CredFromYaml -Path (Join-Path $LoomHome 'slave\config.yaml') -Field 'proxy_token'
    if (-not (Wait-Whoami -Port $StubPort -ProxyToken $slave_token)) {
        $script:StageFailed = $true; $script:ExitCode = 4; throw "slave whoami round-trip failed"
    }

    # driver: real install.ps1 with real params — same whitelist +
    # exit-3 classification as slave above.
    try {
        Invoke-Whitelisted -FilePath 'pwsh' -ArgList @(
            '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File',
            (Join-Path $PSScriptRoot 'driver\install.ps1'),
            '-Project', (Join-Path $LoomHome 'driver'),
            '-Name', 'eval-driver',
            '-ObserverUrl', "http://127.0.0.1:$ObserverPort",
            '-Bin', (Join-Path $BinDir 'driver-agent.windows-amd64.exe')
        ) | Out-Null
    } catch {
        $script:StageFailed = $true; $script:ExitCode = 3; throw "driver install.ps1 failed: $_"
    }
    Update-DriverConfigStubMode -DriverConfigPath (Join-Path $LoomHome 'driver\config.yaml') `
                                -StubBin $stub_bin
    Start-Sub -Role 'driver' -LogPath (Join-Path $LoomHome 'logs\driver.log') `
              -FilePath (Join-Path $LoomHome 'driver\driver-agent.exe') `
              -ArgList @('serve-daemon', '--config', (Join-Path $LoomHome 'driver\config.yaml'), '--listen', "127.0.0.1:$DriverPort")
    if (-not (Wait-TcpListen -Port $DriverPort)) { $script:StageFailed = $true; $script:ExitCode = 4; throw "driver :$DriverPort did not LISTEN" }
    if (-not (Wait-HttpAny -Url "http://127.0.0.1:$DriverPort/" -TimeoutSec 5)) {
        $script:StageFailed = $true; $script:ExitCode = 4; throw "driver HTTP did not respond"
    }
}

function Update-SlaveConfigStubMode {
    param([string]$SlaveConfigPath, [string]$StubBin)
    $creds = Invoke-Whitelisted -FilePath $StubBin `
        -ArgList @('issue', '--server', "http://127.0.0.1:$StubPort", '--role', 'slave', '--short-id', 'slv-eval-001') `
        | ConvertFrom-Json
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'server.url'                -Value ("http://127.0.0.1:" + $StubPort)
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'credentials.sandbox_id'    -Value $creds.sandbox_id
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'credentials.tunnel_token'  -Value $creds.tunnel_token
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'credentials.proxy_token'   -Value $creds.proxy_token
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'credentials.workspace_id'  -Value $creds.workspace_id
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'credentials.short_id'      -Value $creds.short_id
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'daemon.auto_start'         -Value 'false' -RawValue
    Update-YamlLeaf -Path $SlaveConfigPath -Key 'daemon.listen'             -Value ("127.0.0.1:" + $SlavePort)
}

function Update-DriverConfigStubMode {
    param([string]$DriverConfigPath, [string]$StubBin)
    $creds = Invoke-Whitelisted -FilePath $StubBin `
        -ArgList @('issue', '--server', "http://127.0.0.1:$StubPort", '--role', 'driver', '--short-id', 'drv-eval-001') `
        | ConvertFrom-Json
    Update-YamlLeaf -Path $DriverConfigPath -Key 'server.url'                -Value ("http://127.0.0.1:" + $StubPort)
    Update-YamlLeaf -Path $DriverConfigPath -Key 'credentials.sandbox_id'    -Value $creds.sandbox_id
    Update-YamlLeaf -Path $DriverConfigPath -Key 'credentials.tunnel_token'  -Value $creds.tunnel_token
    Update-YamlLeaf -Path $DriverConfigPath -Key 'credentials.proxy_token'   -Value $creds.proxy_token
    Update-YamlLeaf -Path $DriverConfigPath -Key 'credentials.workspace_id'  -Value $creds.workspace_id
    Update-YamlLeaf -Path $DriverConfigPath -Key 'credentials.short_id'      -Value $creds.short_id
}

function Update-YamlLeaf {
    # Line-oriented scalar YAML edit. Handles nested keys of form parent.child
    # by locating the `parent:` line and rewriting/appending the `  child:`
    # under it. Sufficient for the leaf fields we touch — NOT a general YAML
    # editor.
    param(
        [Parameter(Mandatory=$true)][string]$Path,
        [Parameter(Mandatory=$true)][string]$Key,
        [Parameter(Mandatory=$true)][string]$Value,
        [switch]$RawValue
    )
    $parts = $Key -split '\.'
    $lines = Get-Content -LiteralPath $Path
    if ($parts.Count -eq 1) {
        $leaf = $parts[0]
        $newval = if ($RawValue) { $Value } else { "`"$Value`"" }
        $lines = $lines | ForEach-Object {
            if ($_ -match "^\s*${leaf}\s*:") { "${leaf}: $newval" } else { $_ }
        }
    } elseif ($parts.Count -eq 2) {
        $parent = $parts[0]; $leaf = $parts[1]
        $newval = if ($RawValue) { $Value } else { "`"$Value`"" }
        $out = New-Object System.Collections.Generic.List[string]
        $in_parent = $false
        $found = $false
        foreach ($ln in $lines) {
            if ($in_parent -and $ln -match "^\s+${leaf}\s*:") {
                $out.Add("  ${leaf}: $newval")
                $found = $true
                continue
            }
            if ($ln -match "^${parent}\s*:") {
                $in_parent = $true
                $out.Add($ln)
                continue
            }
            if ($in_parent -and $ln -match "^\S") {
                # Left the parent block.
                if (-not $found) { $out.Add("  ${leaf}: $newval"); $found = $true }
                $in_parent = $false
            }
            $out.Add($ln)
        }
        if ($in_parent -and -not $found) { $out.Add("  ${leaf}: $newval") }
        if (-not $found -and -not $in_parent) {
            # Parent not found at all — append.
            $out.Add("${parent}:")
            $out.Add("  ${leaf}: $newval")
        }
        $lines = $out
    } else {
        throw "Update-YamlLeaf: keys deeper than 2 levels not supported ($Key)"
    }
    Set-Content -LiteralPath $Path -Value $lines
}

function Get-CredFromYaml {
    param([string]$Path, [string]$Field)
    $raw = Get-Content -Raw -LiteralPath $Path
    if ($raw -match "(?m)^\s*${Field}\s*:\s*`"?([^`"\s]+)`"?") {
        return $Matches[1]
    }
    return ''
}

function Wait-Whoami {
    param([int]$Port, [string]$ProxyToken, [int]$TimeoutSec = $script:READY_TIMEOUT_DEFAULT)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        try {
            $r = Invoke-WebRequest -Uri "http://127.0.0.1:$Port/api/agent/whoami" `
                    -Headers @{ 'Authorization' = "Bearer $ProxyToken" } `
                    -UseBasicParsing -TimeoutSec 2 -ErrorAction SilentlyContinue
            if ($r.StatusCode -eq 200) { return $true }
        } catch {}
        Start-Sleep -Milliseconds 200
    }
    return $false
}

# --- bring-up: prod ---------------------------------------------------

function Invoke-BringupProd {
    Test-ProdPreflight
    Start-Sub -Role 'observer' -LogPath (Join-Path $LoomHome 'logs\observer.log') `
              -FilePath (Join-Path $LoomHome 'observer\observer-server.exe') `
              -ArgList @('-config', (Join-Path $LoomHome 'observer\observer.yaml'))
    if (-not (Wait-TcpListen -Port $ObserverPort)) { $script:StageFailed = $true; $script:ExitCode = 4; throw "observer :$ObserverPort did not LISTEN" }
    Start-Sub -Role 'slave' -LogPath (Join-Path $LoomHome 'logs\slave.log') `
              -FilePath (Join-Path $LoomHome 'slave\slave-agent.exe') `
              -ArgList @((Join-Path $LoomHome 'slave\config.yaml'))
    if (-not (Wait-TcpListen -Port $SlavePort)) { $script:StageFailed = $true; $script:ExitCode = 4; throw "slave :$SlavePort did not LISTEN" }
    Start-Sub -Role 'driver' -LogPath (Join-Path $LoomHome 'logs\driver.log') `
              -FilePath (Join-Path $LoomHome 'driver\driver-agent.exe') `
              -ArgList @('serve-daemon', '--config', (Join-Path $LoomHome 'driver\config.yaml'), '--listen', "127.0.0.1:$DriverPort")
    if (-not (Wait-TcpListen -Port $DriverPort)) { $script:StageFailed = $true; $script:ExitCode = 4; throw "driver :$DriverPort did not LISTEN" }
}

try {
    # Realize $LoomHome + $PidsDir now (post-dry-run branch). --dry-run
    # returned above without touching the filesystem.
    Initialize-PidsDir
    switch ($ResolvedMode) {
        'stub' { Invoke-BringupStub }
        'prod' { Invoke-BringupProd }
    }
    # Topology emit — the Bash helper is OS-agnostic in that it reads only
    # its CLI + /proc-style sources; on Windows we compute the payload in
    # PowerShell natively to avoid a bash dependency.
    $topo = [ordered]@{
        schema_version         = 1
        host                   = Get-RedactedHost
        os                     = 'windows'
        os_release             = [System.Environment]::OSVersion.VersionString
        arch                   = if ($env:PROCESSOR_ARCHITECTURE -match 'ARM') { 'arm64' } else { 'amd64' }
        kernel                 = [System.Environment]::OSVersion.Version.ToString()
        cpu_count              = [Environment]::ProcessorCount
        mem_bytes              = (Get-CimInstance -ClassName Win32_ComputerSystem -ErrorAction SilentlyContinue).TotalPhysicalMemory
        mode                   = $ResolvedMode
        component_ports        = Get-ComponentPorts
        collected_at_unix      = [int64][math]::Floor(([DateTime]::UtcNow - (Get-Date '1970-01-01Z').ToUniversalTime()).TotalSeconds)
        deploy_script_version  = "wt2-deploy-scripts@0000000"
    }
    $topoJson = $topo | ConvertTo-Json -Compress -Depth 6
    if (-not [string]::IsNullOrEmpty($TopologyOut)) {
        Set-Content -LiteralPath $TopologyOut -Value $topoJson
    } else {
        Write-Output $topoJson
    }
    exit 0
} catch {
    $script:StageFailed = $true
    Invoke-Cleanup-OnFailure
    # Write error message to stderr without triggering ErrorActionPreference=Stop.
    [System.Console]::Error.WriteLine("deploy.ps1: $_")
    exit $script:ExitCode
}
