# scripts/lint-no-private-projects.ps1
# PowerShell variant of lint-no-private-projects.sh. Same rule:
# no tracked file in this public repo may mention a private
# project name, and no tracked file may carry a home path of
# the operator. Used by Windows CI runners and pre-commit hooks
# when bash / git grep are not available.
#
# Run from the repo root OR from anywhere (script auto-cd's to repo root):
#   powershell -NoProfile -File scripts/lint-no-private-projects.ps1
#
# The two rules and their reasons are documented in the .sh variant.
# Keep the two in sync: a rule that lives in one and not in the other
# is a rule that only runs on the platform that has it.

$ErrorActionPreference = 'Stop'

# Resolve repo root from the script's own path so the script works
# regardless of CWD (CI runners, pre-commit hooks, manual invocations).
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location -LiteralPath (Split-Path -Parent $ScriptDir) | Out-Null

# `dark-research-mcp` is NOT here, on purpose: its name is public (the
# npm package publishes it, and it appears in 229 places in this repo).
# What is not public is its code.
$BLOCKLIST = @(
  'dark-harvest','dark-scrapper','dark-scraper','scrappingkeys',
  'scrapping-keys','dark-classify','dark-verify','dark-vault',
  'dark-rotate','dark-copilot','dark-copilot-v3','dark-copilot-private',
  'opita-growth','opita-os-engine','opita-barber','opita-vapes',
  'opita-vapes-web','client-giovanni','dark-dev','project-creator-tool',
  'maxim-operis-platform','v2-cvtools','wifi-redteam-lab','python-arboles'
)

# Paths to skip. The lint scripts themselves are excluded because
# they contain the BLOCKLIST literal. SKIP_PATHS prevents walking
# into third-party / generated artefacts.
$SKIP_PATHS = @(
  '.git',
  'node_modules',
  'vendor',
  'dist',
  'build',
  'scripts/lint-no-private-projects.sh',
  'scripts/lint-no-private-projects.ps1'
)

# The repo's placeholder convention: a segment with angle brackets.
# The generic names are allowed too.
$PLACEHOLDER = '^(<[^>]*>|you|user|name|operator|example|someone|default|USER|ROOT|root)$'

$fail = $false
foreach ($term in $BLOCKLIST) {
  $matches = git grep -l -F $term 2>$null
  if ($null -eq $matches) { continue }
  foreach ($m in $matches) {
    $skip = $false
    foreach ($p in $SKIP_PATHS) {
      if ($m -like "$p*") { $skip = $true; break }
    }
    if (-not $skip) {
      Write-Host "LEAK: '$term' in $m"
      $fail = $true
    }
  }
}

# The second rule: no home path of the operator in a tracked file.
# A segment starting with '[' is a character class of a regex, not a
# name: the governance gate searches for these paths on purpose.
$homeMatches = git grep -I -n -E '(/home/[^/[:space:]]+|/Users/[^/[:space:]]+|[A-Za-z]:[\\/]Users[\\/][^\\/[:space:]]+)' -- '*.go' '*.md' '*.sh' '*.json' '*.yaml' '*.yml' '*.toml' '*.txt' '*.mjs' '*.ts' '*.js' 2>$null
if ($null -ne $homeMatches) {
  foreach ($line in $homeMatches) {
    if ($line -match '^(.+?):(\d+):(.*)$') {
      $file = $Matches[1]
      $body = $Matches[3]
    } else {
      continue
    }
    if ($SKIP_PATHS -contains $file) { continue }
    $u = $null
    if ($body -match '(?:/home/|/Users/|[A-Za-z]:[\\/]Users[\\/])([^/\\[:space:]\[]+)') {
      $u = $Matches[1] -replace '[^A-Za-z0-9_<>.-]+$',''
    }
    if ($null -eq $u -or $u -eq '') { continue }
    if ($u -notmatch $PLACEHOLDER) {
      Write-Host "LEAK: una ruta del home del operador en un fichero publico:"
      Write-Host "  $line"
      $fail = $true
    }
  }
}

if ($fail) {
  Write-Host ""
  Write-Host "Hard rule: do not mention private projects in public artifacts."
  Write-Host "Replace with a generic placeholder ([FUTURE-MCP-N] / [COMPANION-MCP] / etc.)"
  Write-Host "Y las rutas del home: usa el placeholder <name>, y si el fichero es"
  Write-Host "un script, deriva la ruta de la ubicacion del propio script."
  exit 1
}

Write-Host "OK: no private project names and no operator home paths in tracked files."
exit 0
