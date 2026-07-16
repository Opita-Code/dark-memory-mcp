# scripts/lint-no-private-projects.ps1
# PowerShell variant of lint-no-private-projects.sh. Same rule:
# no tracked file in this public repo may mention a private
# project name. Used by Windows CI runners and pre-commit hooks
# when bash / git grep are not available.
#
# Run from the repo root:
#   powershell -NoProfile -File scripts/lint-no-private-projects.ps1

$ErrorActionPreference = 'Stop'

$BLOCKLIST = @(
  'dark-harvest','dark-scrapper','dark-scraper','scrappingkeys',
  'scrapping-keys','dark-classify','dark-verify','dark-vault',
  'dark-rotate','dark-copilot','dark-copilot-v3'
)

# Paths to skip. The lint scripts themselves are excluded because
# they contain the BLOCKLIST literal. SKIP_DIRS prevents walking
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

if ($fail) {
  Write-Host ""
  Write-Host "Hard rule: do not mention private projects in public artifacts."
  Write-Host "Replace with a generic placeholder ([FUTURE-MCP-N] / etc.)"
  exit 1
}

Write-Host "OK: no private project names in tracked files."
exit 0
