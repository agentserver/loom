param(
    [Parameter(Mandatory=$true)]
    [string]$Config
)

# windows_up.ps1 — WT-3-prod-multidevice Windows desktop (slave-B) wrapper.
#
# Bind: 127.0.0.1:18094 (loopback only per spec §7(b)).
# ExecutionPolicy: Process scope ONLY (spec §7(c)); LocalMachine /
# CurrentUser scope changes are banned.
# --mode prod gated on $env:ALLOW_PROD_DEPLOY -eq '1' (spec §7(k));
# absent → falls back to stub.
#
# HARNESS-ONLY: dry_run_all.sh never invokes this on a real Windows
# machine; the ps1 is here as the scaffolding the real-run worktree
# picks up.

Set-ExecutionPolicy -Scope Process Bypass

if (-not (Test-Path $Config)) {
    Write-Error "windows_up.ps1: cannot read config: $Config"
    exit 2
}

$Mode = if ($env:LOOM_DEPLOY_MODE) { $env:LOOM_DEPLOY_MODE } else { 'stub' }
if ($Mode -eq 'prod' -and $env:ALLOW_PROD_DEPLOY -ne '1') {
    Write-Warning "windows_up.ps1: ALLOW_PROD_DEPLOY not set; falling back to -Mode stub"
    $Mode = 'stub'
}

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$DeployPs1 = Join-Path $ScriptDir '..\..\..\..\deploy\windows\deploy.ps1'
$DeployPs1 = [System.IO.Path]::GetFullPath($DeployPs1)

if (-not (Test-Path $DeployPs1)) {
    Write-Warning "windows_up.ps1: deploy.ps1 not present at $DeployPs1 (dry-run mode)"
    Write-Output "would-exec: $DeployPs1 -Mode $Mode -SlavePort 18094"
    exit 0
}

& $DeployPs1 -Mode $Mode -SlavePort 18094
