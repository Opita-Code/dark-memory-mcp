# vibe-flow/log-orchestrator-o1.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-o1"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\orchestration\orchestrator.go",
    "internal\orchestration\session_start.go",
    "internal\session\types.go",
    "tests\orchestration\orchestrator_test.go"
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
        spec_id       = 173
        session_id    = "dark-memory-mcp-wave3-orchestrators"
    } | ConvertTo-Json -Depth 4 -Compress
    $payload | Out-File -FilePath $argsFile -Encoding utf8 -NoNewline
    $out = & powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_artifact_log" -ArgsFile $argsFile -TimeoutMs 15000 2>&1
    if ($out -match '"artifact_id"') { Write-Host "[ok]   $rel"; $ok++ }
    else { Write-Host "[FAIL] $rel"; $fail++ }
}
Write-Host "Artifact log: $ok ok, $fail fail"

$argsFile3 = Join-Path $tmpDir "drift-173-o1.json"
$payload3 = @{
    artifact_id     = 173
    spec_id         = 173
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"O1","name":"SessionStart","status":"resolved","impl":["internal/orchestration/orchestrator.go: Orchestrator struct + New() constructor + injectable now","internal/orchestration/session_start.go: SessionStart orchestrator with input validation + SetActiveProject + SaveSession","internal/session/types.go: NewSessionID() helper (sess- + 8 hex bytes)","tests/orchestration/orchestrator_test.go: 6 tests"],"tests":["TestSessionStart_HappyPath PASS","TestSessionStart_MissingOperator PASS","TestSessionStart_MissingProjectID PASS","TestSessionStart_UnknownProject PASS","TestSessionStart_DefaultProject PASS","TestSessionStart_RowReachStore PASS"]}'
    judge_reasoning = "O1 (SessionStart) closed: first orchestrator shipped. Validates input, sets active project (which validates against projects table), saves session with WriteContext carrying orchestrator actor. Tests cover happy path, missing fields, unknown project rejection, default-project special case, and session row reaches store. Full suite 54/54 green."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload3 | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host "=== O1 (SessionStart) closed ==="