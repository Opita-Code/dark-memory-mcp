#!/usr/bin/env pwsh
#
# scripts/test-inject-version.ps1 — PowerShell counterpart of test-inject-version.sh.
# Same cases, same expectations. Mocks `git describe` via a temp directory prepended
# to PATH and a MOCK_GIT_DESCRIBE env var.
#
# Run from repo root:  pwsh ./scripts/test-inject-version.ps1
#

$ErrorActionPreference = 'Stop'

Set-Location (Join-Path $PSScriptRoot '..')
$Script = './scripts/inject-version.ps1'

# Build a mock git on PATH that prints $env:MOCK_GIT_DESCRIBE for `git describe`.
$mockBin = New-Item -ItemType Directory -Path (Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString()))
$gitShim = Join-Path $mockBin 'git.cmd'
@'
@echo off
if "%1"=="describe" (
    if defined MOCK_GIT_DESCRIBE (
        echo %MOCK_GIT_DESCRIBE%
        exit /b 0
    )
)
exit /b 127
'@ | Set-Content -Path $gitShim

$env:Path = "$mockBin;$env:Path"

$pass = 0
$fail = 0

function Check-Env {
    param([string]$EnvInput, [string]$Want)
    $env:DARK_VERSION = $EnvInput
    $got = & $Script -Raw 2>$null
    if ($got -eq $Want) {
        Write-Host ("  PASS  env {0,-35} -> {1}" -f $EnvInput, $got)
        $script:pass++
    } else {
        Write-Host ("  FAIL  env {0,-35} want={1} got={2}" -f $EnvInput, $Want, $got)
        $script:fail++
    }
}

function Check-Git {
    param([string]$Describe, [string]$Want)
    $env:MOCK_GIT_DESCRIBE = $Describe
    Remove-Item Env:DARK_VERSION -ErrorAction SilentlyContinue
    $got = & $Script -Raw 2>$null
    if ($got -eq $Want) {
        Write-Host ("  PASS  git {0,-30} -> {1}" -f $Describe, $got)
        $script:pass++
    } else {
        Write-Host ("  FAIL  git {0,-30} want={1} got={2}" -f $Describe, $Want, $got)
        $script:fail++
    }
}

Write-Host "== env-var path (DARK_VERSION) - passthrough, v-prefix preserved =="
Check-Env -EnvInput 'v2.9.2'                  -Want 'v2.9.2'
Check-Env -EnvInput 'v2.9.2-alpha'            -Want 'v2.9.2-alpha'
Check-Env -EnvInput 'v2.9.2-beta'             -Want 'v2.9.2-beta'
Check-Env -EnvInput 'v2.9.2-rc.1'             -Want 'v2.9.2-rc.1'
Check-Env -EnvInput 'v2.9.2-alpha-3-gabc1234' -Want 'v2.9.2-alpha-3-gabc1234'
Check-Env -EnvInput 'v2.9.2-alpha-dirty'      -Want 'v2.9.2-alpha-dirty'
Check-Env -EnvInput 'v2.9.2-3-gabc1234-dirty' -Want 'v2.9.2-3-gabc1234-dirty'

Write-Host ""
Write-Host "== git-describe path (mocked) - v-prefix stripped =="
Check-Git -Describe 'v2.9.2'                  -Want '2.9.2'
Check-Git -Describe 'v2.9.2-alpha'            -Want '2.9.2-alpha'
Check-Git -Describe 'v2.9.2-beta'             -Want '2.9.2-beta'
Check-Git -Describe 'v2.9.2-rc.1'             -Want '2.9.2-rc.1'
Check-Git -Describe 'v2.9.2-3-gabc1234'       -Want '2.9.2-3-gabc1234'
Check-Git -Describe 'v2.9.2-alpha-3-gabc1234' -Want '2.9.2-alpha-3-gabc1234'
Check-Git -Describe 'v2.9.2-alpha-dirty'      -Want '2.9.2-alpha-dirty'
Check-Git -Describe 'v2.9.2-3-gabc1234-dirty' -Want '2.9.2-3-gabc1234-dirty'
Check-Git -Describe 'abc1234'                 -Want '0.0.0-dev-abc1234'
Check-Git -Describe 'v999.0.0-rc.42-7-gdeadbeef-dirty' -Want '999.0.0-rc.42-7-gdeadbeef-dirty'

Write-Host ""
Write-Host "summary: pass=$pass fail=$fail"

Remove-Item -Recurse -Force $mockBin
Remove-Item Env:MOCK_GIT_DESCRIBE -ErrorAction SilentlyContinue
Remove-Item Env:DARK_VERSION -ErrorAction SilentlyContinue

if ($fail -gt 0) { exit 1 }