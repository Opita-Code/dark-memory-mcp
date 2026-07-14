# vibe-flow/scope-expand.157.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmp = Join-Path $env:TEMP "drift-157-scope.json"
$payload = @{
    artifact_id     = 157
    spec_id         = 157
    verdict         = "drift_detected"
    spec_diff       = '{"scope_expansion":{"added":"project namespace (multi-tenancy)","reason":"OSINT confirmed shared dark.db causes cross-project context contamination (Rafter, AWS RLS patterns, Cursor isolation bug). Multi-tenancy becomes a DB guarantee via Postgres RLS, app-layer filter fallback for SQLite.","migrations":["v7"],"new_packages":["internal/project/","tests/project/"],"new_invariants":["INV-7 project isolation"]}}'
    judge_reasoning = "Wave 3 scope expansion: project namespace + RLS isolation. Required to prevent context contamination when multiple parallel projects share dark.db. Migration v7 adds project_id column to all tenant-scoped tables with default 'default' for backward compat. Postgres RLS makes isolation a DB guarantee. SQLite fallback uses app-layer filter. Test plan includes cross-project empty queries."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload | Out-File -FilePath $tmp -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $tmp -TimeoutMs 15000 2>&1 | Select-Object -First 2