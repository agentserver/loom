param(
    [Parameter(Mandatory = $true)]
    [string]$Name,

    [Parameter(Mandatory = $true)]
    [string]$ObserverUrl,

    [string]$Workspace = "ws-default",

    [ValidateSet("claude", "codex")]
    [string]$Agent = "codex",

    [string]$ApiKey = "",

    [string]$Bin = "",

    [string]$LoomHome = "",

    [switch]$InstallService,

    [string]$ServiceName = "",

    [switch]$EnableBash
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-FullPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    $expanded = [Environment]::ExpandEnvironmentVariables($Path)
    if ([System.IO.Path]::IsPathRooted($expanded)) {
        return [System.IO.Path]::GetFullPath($expanded)
    }
    return [System.IO.Path]::GetFullPath((Join-Path (Get-Location) $expanded))
}

function ConvertTo-YamlQuoted {
    param([AllowNull()][string]$Value)

    $raw = if ($null -eq $Value) { "" } else { $Value }
    $escaped = $raw.Replace("\", "\\").Replace('"', '\"')
    return '"' + $escaped + '"'
}

function Get-SafeServiceName {
    param([Parameter(Mandatory = $true)][string]$Value)

    return ($Value -replace "[^A-Za-z0-9_.-]", "-")
}

function Get-LogicalProcessorCount {
    try {
        $computer = Get-CimInstance -ClassName Win32_ComputerSystem -ErrorAction Stop
        if ($computer.NumberOfLogicalProcessors -gt 0) {
            return [int]$computer.NumberOfLogicalProcessors
        }
    } catch {
    }
    if ($env:NUMBER_OF_PROCESSORS) {
        return [int]$env:NUMBER_OF_PROCESSORS
    }
    return 1
}

function Get-MemoryGb {
    try {
        $computer = Get-CimInstance -ClassName Win32_ComputerSystem -ErrorAction Stop
        if ($computer.TotalPhysicalMemory -gt 0) {
            return [Math]::Max(1, [int][Math]::Round($computer.TotalPhysicalMemory / 1GB))
        }
    } catch {
    }
    return 1
}

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$binPath = if ($Bin -ne "") {
    Resolve-FullPath $Bin
} else {
    Resolve-FullPath (Join-Path $here "..\bin\slave-agent.windows-amd64.exe")
}

if (-not (Test-Path -LiteralPath $binPath -PathType Leaf)) {
    throw "Missing slave binary: $binPath. Download https://github.com/agentserver/loom/releases/latest/download/slave-agent.windows-amd64.exe or build cmd/slave-agent for GOOS=windows GOARCH=amd64."
}

$installDir = if ($LoomHome -ne "") {
    Resolve-FullPath $LoomHome
} else {
    Join-Path $env:USERPROFILE ".loom\$Name"
}
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

$slaveExe = Join-Path $installDir "slave-agent.exe"
$configPath = Join-Path $installDir "config.yaml"
Copy-Item -LiteralPath $binPath -Destination $slaveExe -Force

$skills = @("powershell", "file", "permissions")
$agentCommand = Get-Command $Agent -ErrorAction SilentlyContinue
if ($null -ne $agentCommand) {
    $skills = @("chat", "chat_resume") + $skills
} else {
    Write-Warning "$Agent was not found. The chat and chat_resume skills will not be advertised."
}
if ($EnableBash) {
    $bash = Get-Command "bash.exe" -ErrorAction SilentlyContinue
    if ($null -ne $bash) {
        $skills += "bash"
    } else {
        Write-Warning "EnableBash was set, but bash.exe was not found. The bash skill will not be advertised."
    }
}
$skillsBlock = ($skills | ForEach-Object { "    - $_" }) -join [Environment]::NewLine

$description = "Windows slave-agent ($Name)"
$tokenStatePath = Join-Path $installDir "observer.token"
$config = Get-Content -LiteralPath (Join-Path $here "config.yaml.template") -Raw
$config = $config.Replace("__AGENT_NAME__", (ConvertTo-YamlQuoted $Name))
$config = $config.Replace("__AGENT_KIND__", (ConvertTo-YamlQuoted $Agent))
$config = $config.Replace("__DESCRIPTION__", (ConvertTo-YamlQuoted $description))
$config = $config.Replace("__LOOM_HOME__", (ConvertTo-YamlQuoted $installDir))
$config = $config.Replace("__OBSERVER_URL__", (ConvertTo-YamlQuoted $ObserverUrl))
$config = $config.Replace("__WORKSPACE_ID__", (ConvertTo-YamlQuoted $Workspace))
$config = $config.Replace("__API_KEY__", (ConvertTo-YamlQuoted $ApiKey))
$config = $config.Replace("__TOKEN_STATE_PATH__", (ConvertTo-YamlQuoted $tokenStatePath))
$config = $config.Replace("__CPU_CORES__", [string](Get-LogicalProcessorCount))
$config = $config.Replace("__MEMORY_GB__", [string](Get-MemoryGb))
$config = $config.Replace("__SKILLS__", $skillsBlock)
Set-Content -LiteralPath $configPath -Value $config -Encoding utf8

Write-Host ""
Write-Host "Slave installed: $installDir"
Write-Host "Foreground command:"
Write-Host "  Set-Location `"$installDir`""
Write-Host "  .\slave-agent.exe `"$configPath`""

if ($InstallService) {
    throw "InstallService is not supported yet on Windows. Use the foreground command or wrap slave-agent.exe with a real Windows service supervisor such as WinSW or NSSM."
}
