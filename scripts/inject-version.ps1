#!/usr/bin/env pwsh
#
# inject-version.ps1 — Windows-native counterpart to scripts/inject-version.sh.
# Same resolution rules, same output formats. Use this on Windows hosts
# where bash / git-bash is not available.
#
# Usage (from a PowerShell session):
#   $LDFLAGS = ./scripts/inject-version.ps1
#   go build -ldflags $LDFLAGS ./cmd/dark-mem-mcp
#
#   ./scripts/inject-version.ps1 -Raw     # prints "1.3.2"
#   ./scripts/inject-version.ps1 -Json    # prints {"version":"1.3.2",...}
#   ./scripts/inject-version.ps1 -Strict  # exit 1 if resolution falls back to "dev"

[CmdletBinding()]
param(
    [switch]$Raw,
    [switch]$Json,
    [switch]$Strict
)

$ErrorActionPreference = 'Stop'

$PKG = 'github.com/dark-agents/dark-memory-mcp/internal/version'
$VAR = 'buildVersion'

function Resolve-GitDescribe {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
        Write-Warning 'inject-version: git not available; falling back to "dev"'
        return @{ Version = 'dev'; Source = 'no-git' }
    }
    try {
        $describe = git describe --tags --always --dirty 2>$null
        if ($LASTEXITCODE -ne 0) {
            Write-Warning 'inject-version: git describe failed'
            return @{ Version = 'dev'; Source = 'git-failed' }
        }
    } catch {
        Write-Warning "inject-version: git describe threw: $_"
        return @{ Version = 'dev'; Source = 'git-failed' }
    }

    # Strip optional leading 'v' so the regex below stays simple.
    $stripped = if ($describe.StartsWith('v')) { $describe.Substring(1) } else { $describe }
    # Accept: X.Y.Z optional pre-release (alpha|beta|rc.N) optional -N-gSHA optional -dirty
    # or bare SHA.
    if ($stripped -cmatch '^(\d+\.\d+\.\d+)(-(alpha|beta|rc\.\d+))?(-(\d+)-g([0-9a-f]+))?(-dirty)?$') {
        return @{ Version = $stripped; Source = 'git' }
    } elseif ($stripped -cmatch '^[0-9a-f]+$') {
        return @{ Version = "0.0.0-dev-$stripped"; Source = 'git-sha-only' }
    } else {
        Write-Warning "inject-version: unparseable git describe: $describe"
        return @{ Version = 'dev'; Source = 'git-unparseable' }
    }
}

# 1. Operator override.
if ($env:DARK_VERSION) {
    $result = @{ Version = $env:DARK_VERSION; Source = 'env' }
} else {
    $result = Resolve-GitDescribe
}

# Strict mode.
if ($Strict -and $result.Version -eq 'dev') {
    Write-Error '--Strict set and resolution fell back to "dev"; aborting'
    exit 1
}

# Output.
if ($Raw) {
    Write-Output $result.Version
} elseif ($Json) {
    $commit = ''
    $dirty = $false
    if (Get-Command git -ErrorAction SilentlyContinue) {
        try { $commit = (git rev-parse --short HEAD 2>$null) } catch {}
        try {
            $porcelain = git status --porcelain 2>$null
            if ($porcelain) { $dirty = $true }
        } catch {}
    }
    @{ version = $result.Version; commit = $commit; dirty = $dirty; source = $result.Source } | ConvertTo-Json -Compress
} else {
    Write-Output "-X $PKG.$VAR=$($result.Version)"
}
