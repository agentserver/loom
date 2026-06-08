param(
    [Parameter(Mandatory = $true)]
    [string]$Project,

    [Parameter(Mandatory = $true)]
    [string]$Name,

    [Parameter(Mandatory = $true)]
    [string]$ObserverUrl,

    [string]$Workspace = "ws-default",

    [ValidateSet("claude", "codex")]
    [string]$Agent = "codex",

    [string]$ApiKey = "",

    [string]$Bin = "",

    [string]$TokenDir = ""
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

function ConvertTo-TomlString {
    param([AllowNull()][string]$Value)

    $raw = if ($null -eq $Value) { "" } else { $Value }
    return $raw.Replace("\", "\\").Replace('"', '\"')
}

function ConvertTo-JsonString {
    param([AllowNull()][string]$Value)

    $raw = if ($null -eq $Value) { "" } else { $Value }
    return $raw.Replace("\", "\\").Replace('"', '\"')
}

function Copy-DirectoryContents {
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$Destination
    )

    New-Item -ItemType Directory -Force -Path $Destination | Out-Null
    Copy-Item -Path (Join-Path $Source "*") -Destination $Destination -Recurse -Force
}

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$binPath = if ($Bin -ne "") {
    Resolve-FullPath $Bin
} else {
    Resolve-FullPath (Join-Path $here "..\bin\driver-agent.windows-amd64.exe")
}

if (-not (Test-Path -LiteralPath $binPath -PathType Leaf)) {
    throw "Missing driver binary: $binPath. Download https://github.com/agentserver/loom/releases/latest/download/driver-agent.windows-amd64.exe or build cmd/driver-agent for GOOS=windows GOARCH=amd64."
}

$projectDir = Resolve-FullPath $Project
New-Item -ItemType Directory -Force -Path $projectDir | Out-Null

$tokenParent = if ($TokenDir -ne "") {
    Resolve-FullPath $TokenDir
} else {
    Join-Path $env:USERPROFILE ".loom\$Name"
}
New-Item -ItemType Directory -Force -Path $tokenParent | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $projectDir "logs") | Out-Null

$driverExe = Join-Path $projectDir "driver-agent.exe"
$configPath = Join-Path $projectDir "config.yaml"
Copy-Item -LiteralPath $binPath -Destination $driverExe -Force

$description = "Windows driver-agent ($Name)"
$tokenStatePath = Join-Path $tokenParent "observer.token"
$config = Get-Content -LiteralPath (Join-Path $here "config.yaml.template") -Raw
$config = $config.Replace("__AGENT_NAME__", (ConvertTo-YamlQuoted $Name))
$config = $config.Replace("__AGENT_KIND__", (ConvertTo-YamlQuoted $Agent))
$config = $config.Replace("__DESCRIPTION__", (ConvertTo-YamlQuoted $description))
$config = $config.Replace("__PROJECT_DIR__", (ConvertTo-YamlQuoted $projectDir))
$config = $config.Replace("__OBSERVER_URL__", (ConvertTo-YamlQuoted $ObserverUrl))
$config = $config.Replace("__WORKSPACE_ID__", (ConvertTo-YamlQuoted $Workspace))
$config = $config.Replace("__API_KEY__", (ConvertTo-YamlQuoted $ApiKey))
$config = $config.Replace("__TOKEN_STATE_PATH__", (ConvertTo-YamlQuoted $tokenStatePath))
Set-Content -LiteralPath $configPath -Value $config -Encoding utf8

if ($Agent -eq "codex") {
    $codexDir = Join-Path $projectDir ".codex"
    New-Item -ItemType Directory -Force -Path $codexDir | Out-Null
    $toml = Get-Content -LiteralPath (Join-Path $here "codex-mcp.toml.template") -Raw
    $toml = $toml.Replace("__DRIVER_AGENT__", (ConvertTo-TomlString $driverExe))
    $toml = $toml.Replace("__CONFIG__", (ConvertTo-TomlString $configPath))
    Set-Content -LiteralPath (Join-Path $codexDir "config.toml") -Value $toml -Encoding utf8
} else {
    $json = Get-Content -LiteralPath (Join-Path $here "mcp.json.template") -Raw
    $json = $json.Replace("__DRIVER_AGENT__", (ConvertTo-JsonString $driverExe))
    $json = $json.Replace("__CONFIG__", (ConvertTo-JsonString $configPath))
    Set-Content -LiteralPath (Join-Path $projectDir ".mcp.json") -Value $json -Encoding utf8
}

if ($Agent -eq "codex") {
    $codexBundle = Join-Path $here "prompts-codex"
    $linuxCodexBundle = Resolve-FullPath (Join-Path $here "..\..\linux\driver\prompts-codex")
    if (Test-Path -LiteralPath (Join-Path $codexBundle "AGENTS.md") -PathType Leaf) {
        Copy-Item -LiteralPath (Join-Path $codexBundle "AGENTS.md") -Destination (Join-Path $projectDir "AGENTS.md") -Force
    } elseif (Test-Path -LiteralPath (Join-Path $linuxCodexBundle "AGENTS.md") -PathType Leaf) {
        Copy-Item -LiteralPath (Join-Path $linuxCodexBundle "AGENTS.md") -Destination (Join-Path $projectDir "AGENTS.md") -Force
    }
} else {
    $claudeBundle = Join-Path $here "skills"
    if (Test-Path -LiteralPath $claudeBundle -PathType Container) {
        Copy-DirectoryContents -Source $claudeBundle -Destination (Join-Path $projectDir ".claude\skills")
    }
}

Write-Host ""
Write-Host "Driver project ready: $projectDir"
Write-Host "Run this one-time registration command:"
Write-Host "  `"$driverExe`" register --config `"$configPath`""
Write-Host ""
Write-Host "Then launch $Agent from the project directory."
