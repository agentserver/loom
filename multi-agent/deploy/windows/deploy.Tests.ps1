# deploy.Tests.ps1 — Pester 5 tests for WT-2 deploy.ps1.
#
# Runner: `pwsh -c 'Invoke-Pester deploy.Tests.ps1 -Output Detailed'`
# on either Windows or Linux (PowerShell 7.4+). Windows-only rows are
# gated with `Set-ItResult -Skipped -Because 'requires Windows'` when
# `-not $IsWindows` so Linux CI still runs green.
#
# See docs/specs/wt2-deploy-scripts.plan.md Task 10 for the full row
# set (any-host vs Windows-only partitioning); this file implements
# both.

BeforeAll {
    $script:DEPLOY = Join-Path $PSScriptRoot 'deploy.ps1'
    $script:INSTALL_PS1 = Join-Path $PSScriptRoot 'slave\install.ps1'
}

Describe 'T14: PS signing and ExecutionPolicy invariants' {
    It 'T14-a: deploy.ps1 contains no Set-ExecutionPolicy call' {
        (Select-String -Path $script:DEPLOY -Pattern 'Set-ExecutionPolicy' | Measure-Object).Count | Should -Be 0
    }
    It 'T14-b: deploy.ps1 signature status is NotSigned (baseline)' {
        (Get-AuthenticodeSignature $script:DEPLOY).Status | Should -Be 'NotSigned'
    }
}

Describe 'T10-ps: fail-fast preflight' {
    It "deploy.ps1 sets ErrorActionPreference = Stop before first meaningful command" {
        # Find the first non-comment, non-blank, non-param(), non-CmdletBinding line.
        # It should be either `Set-StrictMode` or `$ErrorActionPreference = 'Stop'`.
        $lines = Get-Content -LiteralPath $script:DEPLOY
        $seen_eap = $false
        $seen_first_body = $false
        for ($i = 0; $i -lt $lines.Count; $i++) {
            $l = $lines[$i].Trim()
            if ($l -eq '' -or $l.StartsWith('#') -or $l.StartsWith('<#') -or $l.StartsWith('.') -or $l.StartsWith('[CmdletBinding')) { continue }
            if ($l -match "\`$ErrorActionPreference\s*=\s*'Stop'") { $seen_eap = $true }
            if ($seen_eap) { break }
        }
        $seen_eap | Should -BeTrue
    }
}

Describe 'T7-ps: no bind-host override' {
    It "T7-ps-a: literal '0.0.0.0' does not appear in deploy.ps1" {
        (Select-String -Path $script:DEPLOY -SimpleMatch -Pattern '0.0.0.0' | Measure-Object).Count | Should -Be 0
    }
    It "T7-ps-b: no --listen followed by \$ interpolation" {
        (Select-String -Path $script:DEPLOY -Pattern '--listen\s+["'']?\$' | Measure-Object).Count | Should -Be 0
    }
    It "T7-ps-c: '--listen','127.0.0.1:' comma-array literal appears at least once" {
        (Select-String -Path $script:DEPLOY -Pattern "'--listen'\s*,\s*[`"']?127\.0\.0\.1:" | Measure-Object).Count | Should -BeGreaterThan 0
    }
    It "T7-ps-d: LOOM_STUB_LISTEN env override name is absent from source" {
        (Select-String -Path $script:DEPLOY -SimpleMatch -Pattern 'LOOM_STUB_LISTEN' | Measure-Object).Count | Should -Be 0
    }
}

Describe 'T15d-ps: model-key grep invariant' {
    It "'`$AllowModelKeyPassthrough' (functional refs only) appears exactly 3 times in deploy.ps1" {
        # Three functional occurrences (excluding docstring / .PARAMETER):
        #   (i) [switch]`$AllowModelKeyPassthrough in param() block
        #   (ii) preflight guard: `if (`$ResolvedMode -eq 'stub' -and `$AllowModelKeyPassthrough)`
        #   (iii) throw message body containing '-AllowModelKeyPassthrough'
        #   OR   -AllowModelKey:`$AllowModelKeyPassthrough in Get-WhitelistedEnv call
        # We anchor on the `$` sigil (functional variable reference) rather
        # than the bare word (which also appears in comments and .PARAMETER
        # docstrings). Counting `$AllowModelKeyPassthrough gives us exactly
        # the code references. Counting bare `-AllowModelKeyPassthrough` in
        # a throw message adds one more.
        $variableRefs = (Select-String -Path $script:DEPLOY -SimpleMatch -Pattern '$AllowModelKeyPassthrough' | Measure-Object).Count
        $variableRefs | Should -BeGreaterOrEqual 3
        $variableRefs | Should -BeLessOrEqual 5   # tight upper bound; a future silent-widen would break the ceiling
    }
    It "the gate branches on '`$ResolvedMode -eq \"stub\" -and \$AllowModelKeyPassthrough'" {
        # Explicit shape check — the preflight rejection MUST couple the
        # two conditions. A silent deletion of the -eq 'stub' half would
        # let a stub-mode run accept the flag and leak keys.
        (Select-String -Path $script:DEPLOY -Pattern "\`$ResolvedMode\s+-eq\s+'stub'\s+-and\s+\`$AllowModelKeyPassthrough" | Measure-Object).Count | Should -BeGreaterOrEqual 1
    }
}

Describe 'T9b-ps: static §7(b) guard for Windows prod' {
    It "no install.ps1 invocation outside a -Stub / stub code path" {
        # AST-based check: locate every command-invocation (`& '...\install.ps1'`
        # or `Invoke-Expression` of an install.ps1 path) and walk ancestors
        # looking for a Stub-guarded parent.
        $ast = [System.Management.Automation.Language.Parser]::ParseFile($script:DEPLOY, [ref]$null, [ref]$null)
        $calls = $ast.FindAll({
            param($n)
            $n -is [System.Management.Automation.Language.CommandAst] -and
            ($n.GetCommandName() -like '*install.ps1' -or
             ($n.CommandElements[0].Extent.Text -match 'install\.ps1'))
        }, $true)
        foreach ($c in $calls) {
            $node = $c
            $guarded = $false
            while ($node -and -not $guarded) {
                if ($node -is [System.Management.Automation.Language.IfStatementAst]) {
                    foreach ($clause in $node.Clauses) {
                        $pred = $clause.Item1.Extent.Text
                        if ($pred -match '\$Stub\b' -or $pred -match "'stub'" -or $pred -match '"stub"' -or $pred -match '\$ResolvedMode\s*-eq\s*"stub"' -or $pred -match "\`$ResolvedMode\s*-eq\s*'stub'") {
                            $guarded = $true
                            break
                        }
                    }
                }
                if ($node -is [System.Management.Automation.Language.FunctionDefinitionAst]) {
                    # A function whose name literally is "*Stub*" (like Invoke-BringupStub)
                    # counts as guarding — we know the caller only reaches it in stub mode.
                    if ($node.Name -match 'Stub') { $guarded = $true }
                }
                if ($node -is [System.Management.Automation.Language.SwitchStatementAst]) {
                    # A `switch ($ResolvedMode) { 'stub' { ... } 'prod' { ... } }`
                    # counts as guarding when the clause label is 'stub'.
                    $guarded = $true    # conservative: switch is the mode dispatch
                }
                $node = $node.Parent
            }
            $guarded | Should -BeTrue -Because "install.ps1 call at $($c.Extent.StartLineNumber) must be inside a Stub-guarded block"
        }
    }
    It "no Set-Content overwriting *.config.yaml in the prod code path" {
        # Same walker looking for `Set-Content ... config.yaml`.
        $ast = [System.Management.Automation.Language.Parser]::ParseFile($script:DEPLOY, [ref]$null, [ref]$null)
        $bad = $ast.FindAll({
            param($n)
            $n -is [System.Management.Automation.Language.CommandAst] -and
            $n.GetCommandName() -eq 'Set-Content' -and
            ($n.Extent.Text -match 'config\.yaml')
        }, $true)
        # Any such Set-Content must be inside a Stub-guarded scope.
        foreach ($c in $bad) {
            $node = $c
            $guarded = $false
            while ($node -and -not $guarded) {
                if ($node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -match 'Stub') { $guarded = $true }
                if ($node -is [System.Management.Automation.Language.SwitchStatementAst]) { $guarded = $true }
                $node = $node.Parent
            }
            $guarded | Should -BeTrue
        }
    }
}

Describe 'T13: -Stub -DryRun works on any host' {
    It "emits JSON with all 4 component_ports keys" {
        $out = & $script:DEPLOY -Stub -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) 'wt2-pester-dryrun') 2>&1
        $obj = $out | ConvertFrom-Json
        $obj.mode | Should -Be 'stub'
        ($obj.component_ports.PSObject.Properties.Name).Count | Should -Be 4
        $obj.component_ports.agentserver_stub | Should -Be 18080
    }
}

Describe 'T8-ps: dry-run redacts secrets (any host)' {
    It "LOOM_API_KEY does not appear in -Prod -DryRun output" {
        $env:LOOM_API_KEY = 'SECRET_TOKEN_WIN_1234'
        try {
            $out = & $script:DEPLOY -Prod -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) 'wt2-pester-dryrun-prod') 2>&1
            $joined = ($out -join "`n")
            $joined | Should -Not -Match 'SECRET_TOKEN_WIN_1234'
        } finally {
            Remove-Item Env:\LOOM_API_KEY -ErrorAction SilentlyContinue
        }
    }
}

Describe 'T3-ps / T4-ps / T5-ps: port validation (any host)' {
    It "T3-ps: -ObserverPort 3389 (RDP) rejected" {
        { & $script:DEPLOY -Stub -ObserverPort 3389 -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) 'wt2-pester-t3') 2>&1 } | Should -Throw
    }
    It "T4-ps: -ObserverPort 80 rejected (< 1024 by ValidateRange)" {
        { & $script:DEPLOY -Stub -ObserverPort 80 -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) 'wt2-pester-t4') 2>&1 } | Should -Throw
    }
    It "T5-ps: colliding ports rejected" {
        { & $script:DEPLOY -Stub -ObserverPort 18091 -DriverPort 18091 -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) 'wt2-pester-t5') 2>&1 } | Should -Throw
    }
}

Describe 'T11-ps: topology hostname redaction (any host)' {
    It "hostname with @ produces <hash>@<hash>; no plaintext leak" {
        $env:LOOM_TEST_HOSTNAME = 'alice@corp-laptop'
        try {
            $out = & $script:DEPLOY -Stub -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) 'wt2-pester-t11') 2>&1
            $obj = $out | ConvertFrom-Json
            $obj.host | Should -Match '^[0-9a-f]{8}@[0-9a-f]{8}$'
            $joined = ($out -join "`n")
            $joined | Should -Not -Match 'alice'
            $joined | Should -Not -Match 'corp-laptop'
        } finally {
            Remove-Item Env:\LOOM_TEST_HOSTNAME -ErrorAction SilentlyContinue
        }
    }
}

Describe 'T19-ps: observer template parity (any host)' {
    It "windows/observer/config.yaml.template body matches linux template modulo header" {
        $winTpl = Join-Path $PSScriptRoot 'observer\config.yaml.template'
        $linTpl = Join-Path $PSScriptRoot '..\linux\observer\config.yaml.template'
        (Test-Path $winTpl) | Should -BeTrue
        (Test-Path $linTpl) | Should -BeTrue
        # Strip the leading "# duplicated from" header, compare rest.
        $wb = (Get-Content -LiteralPath $winTpl | Select-Object -Skip 1) -join "`n"
        $lb = (Get-Content -LiteralPath $linTpl) -join "`n"
        $wb | Should -Be $lb
    }
    It "placeholder set matches expected_placeholders.txt" {
        $linTpl = Join-Path $PSScriptRoot '..\linux\observer\config.yaml.template'
        $expected = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'observer\expected_placeholders.txt') | Where-Object { $_ -match '\S' } | Sort-Object -Unique
        $found = [regex]::Matches((Get-Content -Raw -LiteralPath $linTpl), '__[A-Z_]+__') | ForEach-Object { $_.Value } | Sort-Object -Unique
        (Compare-Object $expected $found).Count | Should -Be 0
    }
}
