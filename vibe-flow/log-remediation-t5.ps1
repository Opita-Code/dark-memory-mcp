# vibe-flow/log-remediation-t5.ps1
# T5 + final close of spec 171 + spec 157.
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t5"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\migrate\postgres\ddl.go",
    "internal\store\postgres\store.go"
)
$ok = 0; $fail = 0
foreach ($rel in $artifacts) {
    $full = Join-Path $root $rel
    $argsFile = Join-Path $tmpDir ("art-" + ($rel -replace '[/\\]', '_') + ".json")
    $url = "file:///" + (($full -replace '\\', '/').TrimStart('/'))
    $payload = @{
        case_kind     = "C1"
        artifact_type = "code"
        artifact_url  = $url
        spec_id       = 171
        session_id    = "dark-memory-mcp-v1-remediation"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

# Drift log T5
$argsFile3 = Join-Path $tmpDir "drift-171-t5.json"
$driftPayload = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"T5","finding":"W3-003 (Postgres option b)","status":"resolved","design_decision":"drop RLS, mirror SQLite explicit WHERE project_id=$1","impl":["migration v7 postgres: removed RLS DO block (kept ADD COLUMN project_id + indexes)","withProjectTx and quotePGString removed (dead code after RLS gone)","SaveSession/GetSession/CloseSession/ListSessions project-filtered","SaveRun/GetRun/ListRuns/Recall/ListItems/LinkResearch project-filtered","ResearchStatus documented global-by-design (same as Stats)","package-level doc explains Postgres is partial-impl + SQLite is production"]}'
    judge_reasoning = "T5 closed: W3-003 (Postgres RLS dead code) fixed via option (b). Migration v7 no longer creates RLS policies. Every implemented Postgres method (lifecycle, audit, sessions, research, projects) now requires an active project and filters by project_id. Methods that are not yet implemented (SaveSpec, GetSpec, SaveArtifact, etc.) remain stubs returning ErrNotConfigured — production runs SQLite today. If you want RLS back, see option (a) in spec 171 T5 (wire withProjectTx + add live Postgres tests). 48/48 green suite. Spec 171 fully resolved; spec 157 closes."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2

# Drift log: spec 171 closes (all 5 tasks done)
$argsFile4 = Join-Path $tmpDir "drift-171-close.json"
$driftPayload2 = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"status":"closed","reason":"all 5 tasks T1-T5 resolved; spec 171 fully implemented"}'
    judge_reasoning = "Spec 171 (Wave 3 part 1.5 remediation plan) closes. All 5 tasks completed: T1 W3-001 GetRun+ListItems cross-project isolation; T2 W3-005 SetActiveProject validation; T3 W3-004 default project auto-seed; T4 (a-g) W3-002 read-method project filtering + global-by-design documentation; T5 W3-003 Postgres option (b). 48/48 green suite. spec 157 can now be marked drift_resolved since the underlying review findings are closed."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload2 | Out-File -FilePath $argsFile4 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile4 -TimeoutMs 15000 2>&1 | Select-Object -First 2

# Drift log: spec 157 closes (origin spec that triggered the remediation)
$argsFile5 = Join-Path $tmpDir "drift-157-close.json"
$driftPayload3 = @{
    artifact_id     = 157
    spec_id         = 157
    verdict         = "drift_resolved"
    spec_diff       = '{"status":"closed","reason":"all 5 HIGH findings (W3-001..W3-005) resolved via spec 171 remediation"}'
    judge_reasoning = "Spec 157 (Wave 3 part 1 review root spec) closes. The 5 HIGH findings raised in review/w3p1 are all resolved via spec 171 sub-tasks: W3-001 (T1, GetRun+ListItems leak), W3-005 (T2, SetActiveProject no validation), W3-004 (T3, default project not auto-seeded), W3-002 (T4a-T4g, ~21 read methods without project filter + global-by-design documentation), W3-003 (T5, Postgres option b). Suite 48/48 verde. Wave 3 part 1 now ships correctly; Wave 3 part 2 (orchestrators) unblocked."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$driftPayload3 | Out-File -FilePath $argsFile5 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile5 -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== T5 closed; spec 171 closed; spec 157 closed ==="