# deploy.internal.psm1 — pure functions extracted from deploy.ps1 so
# Pester unit tests can `Import-Module` them without dot-sourcing the
# entire CLI (which would trigger port validation, spawn attempts, and
# the top-level trap). Every function here is idempotent, side-effect
# free, and consumes only its explicit parameters + a small set of
# module-scoped constants.
#
# Spec: docs/specs/wt2-deploy-scripts.spec.md §7(g) env whitelist.
# Fresh-review P1-4 fix (round 8): the previous approach dot-sourced
# deploy.ps1 with a regex that stripped the top-level try/catch. That
# regex is fragile — a maintainer inserting any non-# line between
# `try {` and `Initialize-PidsDir` silently regresses the strip and
# tests start hitting the real bring-up path. Module import cannot
# regress that way.

Set-StrictMode -Version Latest

# Windows ALWAYS_ENV_KEYS EXTENDS (not "mirrors") tools/eval/runner/
# subprocess.go:AlwaysAllowedEnvKeys. The first 6 are the exact Linux
# set. The extra 6 (USERPROFILE / APPDATA / LOCALAPPDATA / SystemRoot
# / SystemDrive / ComSpec) are Windows-required — pwsh and other
# child processes cannot start without SystemRoot/ComSpec, and any
# installer or Codex/Claude CLI that resolves user paths needs
# USERPROFILE + APPDATA + LOCALAPPDATA. Not a security-relevant
# divergence: none of the added keys carry credentials. Round-9 P2-A
# clarification (spec §7(g) uses "extends" for this list).
$script:ALWAYS_ENV_KEYS = @('PATH', 'HOME', 'LANG', 'LC_ALL', 'TZ', 'USER',
                            'USERPROFILE', 'APPDATA', 'LOCALAPPDATA',
                            'SystemRoot', 'SystemDrive', 'ComSpec')
$script:IFSET_ENV_KEYS  = @('AGENTSERVER_ROOT', 'MODELSERVER_ROOT', 'APP_ROOT', 'MOCK_MODEL_URL')

function Get-WhitelistedEnv {
    # Return a hashtable of KEY -> value pairs that a subprocess spawned
    # by deploy.ps1 should inherit. Mirrors bash `emit_whitelisted_env`.
    #
    # Rules (spec §7(g)):
    #   ALWAYS keys — emit even when absent from parent (empty value),
    #     matching bash impl.
    #   IF-SET keys — emit only when the parent has them.
    #   LOOM_* prefix — emit any LOOM_<suffix> from parent (suffix len >= 1).
    #   OPENAI_API_KEY / ANTHROPIC_API_KEY — emit only when
    #     $Mode -eq 'prod' AND $AllowModelKey. Writes a WARN line to
    #     stderr for each key passed through.
    param(
        [Parameter(Mandatory)][string]$Mode,
        [switch]$AllowModelKey
    )
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
                [System.Console]::Error.WriteLine("deploy.ps1: passing $k through to subprocesses")
                $out[$k] = $v
            }
        }
    }
    return $out
}

Export-ModuleMember -Function Get-WhitelistedEnv -Variable ALWAYS_ENV_KEYS, IFSET_ENV_KEYS
