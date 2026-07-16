$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t5-retry"
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$argsFile = Join-Path $tmpDir "drift-171-t5.json"
$payload = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"T5","finding":"W3-003 Postgres option b","status":"resolved","impl":["migration v7 postgres: removed RLS DO block","withProjectTx and quotePGString removed","SaveSession/GetSession/CloseSession/ListSessions project-filtered","SaveRun/GetRun/ListRuns/Recall/ListItems/LinkResearch project-filtered","ResearchStatus documented global-by-design","package-level doc explains Postgres is partial-impl + SQLite is production"]}'
    judge_reasoning = "T5 closed: W3-003 (Postgres RLS dead code) fixed via option (b). 48/48 green suite. spec 171 + 157 close."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host "T5 drift_log retry done"