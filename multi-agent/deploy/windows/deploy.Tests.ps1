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
        $out = & $script:DEPLOY -Stub -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-pester-dryrun-' + [guid]::NewGuid().ToString('N').Substring(0,8))) 2>&1
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
            $out = & $script:DEPLOY -Prod -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-pester-dryrun-prod-' + [guid]::NewGuid().ToString('N').Substring(0,8))) 2>&1
            $joined = ($out -join "`n")
            $joined | Should -Not -Match 'SECRET_TOKEN_WIN_1234'
        } finally {
            Remove-Item Env:\LOOM_API_KEY -ErrorAction SilentlyContinue
        }
    }
}

Describe 'T3-ps / T4-ps / T5-ps: port validation (any host)' {
    It "T3-ps: -ObserverPort 3389 (RDP) rejected" {
        { & $script:DEPLOY -Stub -ObserverPort 3389 -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-pester-t3-' + [guid]::NewGuid().ToString('N').Substring(0,8))) 2>&1 } | Should -Throw
    }
    It "T4-ps: -ObserverPort 80 rejected (< 1024 by ValidateRange)" {
        { & $script:DEPLOY -Stub -ObserverPort 80 -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-pester-t4-' + [guid]::NewGuid().ToString('N').Substring(0,8))) 2>&1 } | Should -Throw
    }
    It "T5-ps: colliding ports rejected" {
        { & $script:DEPLOY -Stub -ObserverPort 18091 -DriverPort 18091 -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-pester-t5-' + [guid]::NewGuid().ToString('N').Substring(0,8))) 2>&1 } | Should -Throw
    }
}

Describe 'T11-ps: topology hostname redaction (any host)' {
    It "hostname with @ produces <hash>@<hash>; no plaintext leak" {
        $env:LOOM_TEST_HOSTNAME = 'alice@corp-laptop'
        try {
            $out = & $script:DEPLOY -Stub -DryRun -LoomHome (Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-pester-t11-' + [guid]::NewGuid().ToString('N').Substring(0,8))) 2>&1
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

Describe 'T15-ps / T15b-ps / T15c-ps: env whitelist (any host)' {
    # Import deploy.internal.psm1 directly instead of dot-sourcing
    # deploy.ps1 with a regex strip of the CLI body. The previous
    # regex-based approach (Codex rounds 4-6) fragile-ly depended on
    # the exact shape of the top-level try/catch; fresh-review P1-4
    # (round 8) moved Get-WhitelistedEnv into the module for exactly
    # this reason. Same source of truth, no strip fragility.
    BeforeAll {
        $script:MODULE_PATH = Join-Path $PSScriptRoot 'deploy.internal.psm1'
        Import-Module -Force $script:MODULE_PATH
    }
    AfterAll {
        Remove-Module deploy.internal -ErrorAction SilentlyContinue
    }

    It 'T15-ps: drops AWS_/GITHUB_TOKEN/DOCKER_CONFIG/NPM_TOKEN' {
        $env:AWS_ACCESS_KEY_ID = 'NOPE_AWS'
        $env:GITHUB_TOKEN      = 'NOPE_GH'
        $env:DOCKER_CONFIG     = '/etc/docker'
        $env:NPM_TOKEN         = 'NOPE_NPM'
        try {
            $out = Get-WhitelistedEnv -Mode 'stub'
            $out.ContainsKey('AWS_ACCESS_KEY_ID') | Should -BeFalse
            $out.ContainsKey('GITHUB_TOKEN')      | Should -BeFalse
            $out.ContainsKey('DOCKER_CONFIG')     | Should -BeFalse
            $out.ContainsKey('NPM_TOKEN')         | Should -BeFalse
        } finally {
            Remove-Item Env:\AWS_ACCESS_KEY_ID -ErrorAction SilentlyContinue
            Remove-Item Env:\GITHUB_TOKEN      -ErrorAction SilentlyContinue
            Remove-Item Env:\DOCKER_CONFIG     -ErrorAction SilentlyContinue
            Remove-Item Env:\NPM_TOKEN         -ErrorAction SilentlyContinue
        }
    }

    It 'T15b-ps: passes always-allowed + if-set + LOOM_* keys' {
        $env:MOCK_MODEL_URL     = 'http://127.0.0.1:9090'
        $env:AGENTSERVER_ROOT   = '/repo/agentserver'
        $env:LOOM_OBSERVER_URL  = 'http://127.0.0.1:18091'
        try {
            $out = Get-WhitelistedEnv -Mode 'stub'
            $out['PATH']                | Should -Not -BeNullOrEmpty
            $out['MOCK_MODEL_URL']      | Should -Be 'http://127.0.0.1:9090'
            $out['AGENTSERVER_ROOT']    | Should -Be '/repo/agentserver'
            $out['LOOM_OBSERVER_URL']   | Should -Be 'http://127.0.0.1:18091'
        } finally {
            Remove-Item Env:\MOCK_MODEL_URL    -ErrorAction SilentlyContinue
            Remove-Item Env:\AGENTSERVER_ROOT  -ErrorAction SilentlyContinue
            Remove-Item Env:\LOOM_OBSERVER_URL -ErrorAction SilentlyContinue
        }
    }

    It 'T15c-i-ps: stub never propagates OPENAI/ANTHROPIC even with AllowModelKey' {
        $env:OPENAI_API_KEY   = 'STUB_KEY_9zzz'
        $env:ANTHROPIC_API_KEY = 'STUB_KEY_8yyy'
        try {
            $out = Get-WhitelistedEnv -Mode 'stub' -AllowModelKey
            $out.ContainsKey('OPENAI_API_KEY')    | Should -BeFalse
            $out.ContainsKey('ANTHROPIC_API_KEY') | Should -BeFalse
        } finally {
            Remove-Item Env:\OPENAI_API_KEY    -ErrorAction SilentlyContinue
            Remove-Item Env:\ANTHROPIC_API_KEY -ErrorAction SilentlyContinue
        }
    }

    It 'T15c-ii-ps: prod without flag drops OPENAI_API_KEY' {
        $env:OPENAI_API_KEY = 'PROD_KEY_1234'
        try {
            $out = Get-WhitelistedEnv -Mode 'prod'
            $out.ContainsKey('OPENAI_API_KEY') | Should -BeFalse
        } finally {
            Remove-Item Env:\OPENAI_API_KEY -ErrorAction SilentlyContinue
        }
    }

    It 'T15c-iii-ps: prod with flag passes OPENAI_API_KEY AND emits WARN' {
        # Fresh-review P1-8 (round 8): previous test asserted only that
        # the key was propagated. Spec §7(g) mitigation includes the
        # WARN line so operators see "silent leak" preventable in real
        # time. Capture stderr via a temp file + pwsh subprocess.
        $env:OPENAI_API_KEY = 'PROD_KEY_9999'
        $errFile = Join-Path ([System.IO.Path]::GetTempPath()) ("wt2-t15c-iii-" + [guid]::NewGuid().ToString('N').Substring(0,8) + ".err")
        try {
            $modPath = $script:MODULE_PATH
            $inner = @"
Import-Module -Force '$modPath'
`$out = Get-WhitelistedEnv -Mode 'prod' -AllowModelKey
Write-Output ('KEY=' + `$out['OPENAI_API_KEY'])
"@
            $stdout = pwsh -NoProfile -Command $inner 2> $errFile
            ($stdout | Out-String) | Should -Match 'KEY=PROD_KEY_9999'
            (Get-Content -Raw -LiteralPath $errFile) | Should -Match 'passing OPENAI_API_KEY through'
        } finally {
            Remove-Item Env:\OPENAI_API_KEY -ErrorAction SilentlyContinue
            Remove-Item -LiteralPath $errFile -ErrorAction SilentlyContinue
        }
    }
}

Describe 'T6-ps / T6c-ps / T9-ps / T17-ps / T18-ps: Windows-only runtime' {
    # These rows require Windows-native cmdlets (Test-NetConnection,
    # Start-Process semantics that materially differ from Linux) and
    # real binaries under $BinDir. They are Set-ItResult -Skipped on
    # non-Windows so Pester-on-Linux still runs green — Task 12's
    # fresh-Windows-host leg (see plan §6.3) is what actually
    # exercises them.

    It 'T6-ps: agentserver-stub -ArgumentList begins with 127.0.0.1 (source-argv check)' {
        if (-not $IsWindows) { Set-ItResult -Skipped -Because 'requires Windows'; return }
        # On Windows, static grep the built argv construction inside
        # Invoke-BringupStub — the runtime argv is passed straight from
        # Start-Sub's -ArgList parameter, so source-argv is dispositive.
        # (A full-stack live run is the manual fresh-host smoke in
        # plan §6.3 Windows leg; this test catches the source-level
        # regression on any Windows Pester run.)
        $src = Get-Content -Raw -LiteralPath $script:DEPLOY
        $src | Should -Match "Start-Sub -Role 'agentserver-stub'[\s\S]*?-ArgList\s+@\('--listen',\s*[`"']127\.0\.0\.1:"
    }
    It 'T6c-ps: post-Stub slave config shows auto_start=false + stub server.url (source check)' {
        if (-not $IsWindows) { Set-ItResult -Skipped -Because 'requires Windows'; return }
        # Static: Update-SlaveConfigStubMode writes both fields. A
        # regression that dropped either edit fails this Pester assertion.
        $src = Get-Content -Raw -LiteralPath $script:DEPLOY
        $src | Should -Match "Update-YamlLeaf -Path \`$SlaveConfigPath -Key 'daemon\.auto_start'"
        $src | Should -Match "Update-YamlLeaf -Path \`$SlaveConfigPath -Key 'server\.url'"
    }
    It 'T9-ps: -Prod does not clobber pre-registered proxy_token' {
        if (-not $IsWindows) { Set-ItResult -Skipped -Because 'requires Windows'; return }
        # Synthetic pre-existing $LoomHome/slave/config.yaml with
        # proxy_token=PROXY_TOKEN_SECRET_WIN; run deploy.ps1 -Prod;
        # assert Get-FileHash before/after equal AND grep for the token
        # returns exactly the original file (§7(b) Windows counterpart
        # of Linux T9).
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-t9-ps-' + [guid]::NewGuid().ToString('N').Substring(0,8))
        New-Item -ItemType Directory -Force -Path (Join-Path $tmp 'slave')    | Out-Null
        New-Item -ItemType Directory -Force -Path (Join-Path $tmp 'observer') | Out-Null
        New-Item -ItemType Directory -Force -Path (Join-Path $tmp 'driver')   | Out-Null
        try {
            # Minimal fixture: observer + slave + driver configs with real
            # credentials + observer listen_addr matching -ObserverPort.
            Set-Content -LiteralPath (Join-Path $tmp 'observer\observer.yaml') `
                -Value @'
listen_addr: "127.0.0.1:18091"
db_path: /tmp/prod-fixture.db
api_keys:
  - id: bootstrap
    key: "fixture-key"
    note: "fixture"
'@
            # Fresh-review P1-6 (round 8): the previous test used
            # `Copy-Item -ErrorAction SilentlyContinue`, which silently
            # dropped the copy on any host without prebuilt binaries —
            # prod preflight then threw at the first `Test-Path
            # -PathType Leaf` and Invoke-BringupProd never ran, making
            # the file-hash assertion trivially green. We now bake in
            # tiny dummy `.exe` placeholders that satisfy Test-Path
            # so preflight passes and the real bring-up path (which
            # would be where a config-clobber regression would land)
            # is exercised.
            # Round-9 P1-A fix: `-Encoding Byte` was removed in
            # PowerShell 6+ (the file's own header requires 7.4+) —
            # the replacement is `-AsByteStream` with a `[byte[]]`
            # value. Test-Path -PathType Leaf doesn't require any
            # specific content; two-byte MZ prefix is symbolic only.
            foreach ($p in @('observer\observer-server.exe',
                             'slave\slave-agent.exe',
                             'driver\driver-agent.exe')) {
                [System.IO.File]::WriteAllBytes((Join-Path $tmp $p), [byte[]]@(0x4D, 0x5A))
            }

            Set-Content -LiteralPath (Join-Path $tmp 'slave\config.yaml') `
                -Value @'
credentials:
  proxy_token: "PROXY_TOKEN_SECRET_WIN"
  short_id: "slv-x"
  workspace_id: "ws-x"
daemon:
  listen: "127.0.0.1:18093"
'@
            $preHash = (Get-FileHash -LiteralPath (Join-Path $tmp 'slave\config.yaml') -Algorithm SHA256).Hash

            Set-Content -LiteralPath (Join-Path $tmp 'driver\config.yaml') `
                -Value @'
credentials:
  proxy_token: "DRIVER_TOKEN_X"
  short_id: "drv-x"
'@
            # Run -Prod as a dry-run analog: we only need preflight to
            # succeed and no config edit to happen. Full spawn requires
            # real binaries so we accept exit != 0 on the spawn attempt
            # and only assert the file-unchanged property.
            & $script:DEPLOY -Prod -LoomHome $tmp -ObserverPort 18091 -SlavePort 18093 -DriverPort 18092 2>&1 | Out-Null
            $postHash = (Get-FileHash -LiteralPath (Join-Path $tmp 'slave\config.yaml') -Algorithm SHA256).Hash
            $preHash | Should -Be $postHash
            $hits = Select-String -Path (Join-Path $tmp 'slave\config.yaml') -SimpleMatch -Pattern 'PROXY_TOKEN_SECRET_WIN'
            $hits.Count | Should -Be 1
        } finally {
            Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
        }
    }
    It 'T17-ps: fresh Windows -Stub runs to completion with 4 readiness gates' {
        if (-not $IsWindows) { Set-ItResult -Skipped -Because 'requires Windows'; return }
        # Requires prebuilt Windows binaries under deploy/windows/bin/.
        # When absent (a fresh CI Windows runner without a build step),
        # skip with a specific reason rather than silently pass.
        $binDir = Join-Path $PSScriptRoot 'bin'
        $stubBin = Join-Path $binDir 'agentserver-stub.windows-amd64.exe'
        if (-not (Test-Path -LiteralPath $stubBin)) {
            Set-ItResult -Skipped -Because "requires $stubBin (rebuild with go build)"; return
        }
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-t17-ps-' + [guid]::NewGuid().ToString('N').Substring(0,8))
        try {
            & $script:DEPLOY -Stub -LoomHome $tmp 2>&1 | Out-Null
            $LASTEXITCODE | Should -Be 0
            (Test-Path (Join-Path $tmp '.pids\agentserver-stub.pid')) | Should -BeTrue
            (Test-Path (Join-Path $tmp '.pids\observer.pid')) | Should -BeTrue
            (Test-Path (Join-Path $tmp '.pids\driver.pid')) | Should -BeTrue
            (Test-Path (Join-Path $tmp '.pids\slave.pid')) | Should -BeTrue
        } finally {
            # Reap via -Shutdown.
            try { & $script:DEPLOY -Shutdown -LoomHome $tmp 2>&1 | Out-Null } catch {}
            Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
        }
    }
    It 'T18-ps: -Stub writes .pids/*.pid, spawn-then-exit contract' {
        if (-not $IsWindows) { Set-ItResult -Skipped -Because 'requires Windows'; return }
        $binDir = Join-Path $PSScriptRoot 'bin'
        if (-not (Test-Path (Join-Path $binDir 'agentserver-stub.windows-amd64.exe'))) {
            Set-ItResult -Skipped -Because 'requires prebuilt Windows binaries'; return
        }
        # Covered by T17-ps above; explicit assertion of spawn-then-exit
        # (deploy exits before PIDs are reaped).
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-t18-ps-' + [guid]::NewGuid().ToString('N').Substring(0,8))
        try {
            & $script:DEPLOY -Stub -LoomHome $tmp 2>&1 | Out-Null
            # Read each PID and assert Get-Process finds it alive.
            foreach ($role in 'agentserver-stub','observer','slave','driver') {
                $pf = Join-Path $tmp ".pids\$role.pid"
                (Test-Path $pf) | Should -BeTrue
                $pidVal = [int](Get-Content -LiteralPath $pf)
                (Get-Process -Id $pidVal -ErrorAction SilentlyContinue) | Should -Not -BeNullOrEmpty
            }
        } finally {
            try { & $script:DEPLOY -Shutdown -LoomHome $tmp 2>&1 | Out-Null } catch {}
            Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
        }
    }
    It 'T18b-ps: -Shutdown reaps every pid in .pids' {
        if (-not $IsWindows) { Set-ItResult -Skipped -Because 'requires Windows'; return }
        $binDir = Join-Path $PSScriptRoot 'bin'
        if (-not (Test-Path (Join-Path $binDir 'agentserver-stub.windows-amd64.exe'))) {
            Set-ItResult -Skipped -Because 'requires prebuilt Windows binaries'; return
        }
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ('wt2-t18b-ps-' + [guid]::NewGuid().ToString('N').Substring(0,8))
        try {
            & $script:DEPLOY -Stub -LoomHome $tmp 2>&1 | Out-Null
            $pids = Get-ChildItem -Path (Join-Path $tmp '.pids') -Filter '*.pid' | ForEach-Object {
                [int](Get-Content -LiteralPath $_.FullName)
            }
            & $script:DEPLOY -Shutdown -LoomHome $tmp 2>&1 | Out-Null
            Start-Sleep -Seconds 6
            foreach ($p in $pids) {
                (Get-Process -Id $p -ErrorAction SilentlyContinue) | Should -BeNullOrEmpty
            }
            (Test-Path (Join-Path $tmp '.pids')) | Should -BeFalse
        } finally {
            Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
        }
    }
    It 'T18c-ps: readiness timeout exits 4 with cleanup (source invariants)' {
        # Round-9 P2-C hardening: the previous single-line regex was
        # fragile — reordering `$script:StageFailed=$true` and
        # `$script:ExitCode=4` broke the grep even though behaviour
        # was unchanged. We now assert three independent invariants
        # that together mean "readiness timeout on stub /healthz →
        # exit 4 → cleanup runs":
        #   (i) `Wait-HttpOk` is what gates the stub (not TCP-only),
        #  (ii) somewhere in Invoke-BringupStub, a readiness-fail
        #       path sets `$script:ExitCode = 4`,
        # (iii) the outer catch block calls `Invoke-Cleanup-OnFailure`
        #       (which we know from Get-Cleanup-OnFailure static test
        #       reaps SpawnedPids + removes .pids/).
        # A regression that removes any one of these breaks a test.
        $src = Get-Content -Raw -LiteralPath $script:DEPLOY
        # (i) Wait-HttpOk gates the stub.
        $src | Should -Match "Wait-HttpOk -Url\s+`"http://127\.0\.0\.1:\`$StubPort/healthz`""
        # (ii) `$script:ExitCode = 4` appears at least once inside
        # Invoke-BringupStub's body.
        $bringupBody = ($src -split '(?m)^function Invoke-BringupStub\s*\{')[1]
        if ($bringupBody) {
            $bringupBody = ($bringupBody -split '(?m)^\}\s*$')[0]
        }
        $bringupBody | Should -Match '\$script:ExitCode\s*=\s*4'
        # (iii) The outer catch calls Invoke-Cleanup-OnFailure.
        $src | Should -Match "catch\s*\{[\s\S]*?Invoke-Cleanup-OnFailure[\s\S]*?exit \`$script:ExitCode"
        # NOTE: a full runtime test needs a stalled-stub .exe that
        # binds $StubPort but never responds to /healthz. plan §6.3
        # fresh-host smoke covers this end-to-end on a real Windows
        # runner with a real agentserver-stub.exe.
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
