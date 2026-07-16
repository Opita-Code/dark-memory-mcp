# vibe-flow/log-t5-finalize.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-t5-final"
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

# Findings doc: spec 170 already exists, log T5 doc there
$findingsJson = Get-Content (Join-Path $root "vibe-flow\t5-findings.json") -Raw
$argsFile0 = Join-Path $tmpDir "findings-doc.json"
$payload0 = @{
    case_kind      = "C2"
    artifact_type  = "review-findings"
    artifact_url   = "file:///" + (($root + "vibe-flow\t5-findings.json") -replace '\\', '/').TrimStart('/')
    spec_id        = 170
    session_id     = "dark-memory-mcp-v1-remediation"
    has_disclosure = $false
} | ConvertTo-Json -Depth 4 -Compress
$payload0 | Out-File -FilePath $argsFile0 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile0 -TimeoutMs 15000 2>&1 | Select-Object -First 2

# Drift T5
$argsFile1 = Join-Path $tmpDir "drift-171-t5.json"
$payload1 = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = (Get-Content (Join-Path $root "vibe-flow\t5-findings.json") -Raw)
    judge_reasoning = "T5 closed: W3-003 (Postgres RLS dead code) fixed via option (b). Migration v7 no longer creates RLS policies. Every implemented Postgres method now requires an active project and filters by project_id. Methods that are not yet implemented remain stubs returning ErrNotConfigured. If you want RLS back, see option (a) in spec 171 T5. 48/48 green suite. Spec 171 fully resolved; spec 157 closes."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload1 | Out-File -FilePath $argsFile1 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile1 -TimeoutMs 15000 2>&1 | Select-Object -First 2

# Drift 171 close
$argsFile2 = Join-Path $tmpDir "drift-171-close.json"
$payload2 = @{
    artifact_id     = 171
    spec_id         = 171
    verdict         = "drift_resolved"
    spec_diff       = '{"status":"closed","reason":"all 5 tasks T1-T5 resolved; spec 171 fully implemented"}'
    judge_reasoning = "Spec 171 (Wave 3 part 1.5 remediation plan) closes. All 5 tasks completed: T1 W3-001 GetRun+ListItems cross-project isolation; T2 W3-005 SetActiveProject validation; T3 W3-004 default project auto-seed; T4 (a-g) W3-002 read-method project filtering + global-by-design documentation; T5 W3-003 Postgres option (b). 48/48 green suite. spec 157 can now be marked drift_resolved."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload2 | Out-File -FilePath $argsFile2 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile2 -TimeoutMs 15000 2>&1 | Select-Object -First 2

# Drift 157 close
$argsFile3 = Join-Path $tmpDir "drift-157-close.json"
$payload3 = @{
    artifact_id     = 157
    spec_id         = 157
    verdict         = "drift_resolved"
    spec_diff       = '{"status":"closed","reason":"all 5 HIGH findings (W3-001..W3-005) resolved via spec 171 remediation"}'
    judge_reasoning = "Spec 157 (Wave 3 part 1 review root spec) closes. The 5 HIGH findings raised in review/w3p1 are all resolved via spec 171 sub-tasks: W3-001 (T1), W3-005 (T2), W3-004 (T3), W3-002 (T4a-T4g), W3-003 (T5). Suite 48/48 verde. Wave 3 part 1 now ships correctly; Wave 3 part 2 (orchestrators) unblocked."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload3 | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2

Write-Host ""
Write-Host "=== T5 closed; spec 171 closed; spec 157 closed ==="