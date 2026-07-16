# vibe-flow/log-orchestrator-o4.ps1
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$wrapper = "C:\Users\Nico\AppData\Local\Temp\opencode\dark-agents-call.ps1"
$tmpDir = Join-Path $env:TEMP "dark-mem-o4"
if (Test-Path $tmpDir) { Get-ChildItem $tmpDir -Filter *.json -ErrorAction SilentlyContinue | Remove-Item }
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

$root = "C:\Users\Nico\Documents\dark-memory-mcp"
$artifacts = @(
    "internal\orchestration\recall_context.go",
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

$argsFile3 = Join-Path $tmpDir "drift-173-o4.json"
$payload3 = @{
    artifact_id     = 173
    spec_id         = 173
    verdict         = "drift_resolved"
    spec_diff       = '{"task":"O4","name":"RecallContext","status":"resolved","impl":["internal/orchestration/recall_context.go: RecallContext orchestrator","Store.Recall + economy.Atlan pipeline","tiered CapTotal based on MaxTokens","two-stage truncation loop","per-item snippet shrink fallback"],"tests":["TestRecallContext_HappyPath PASS","TestRecallContext_MissingQuery PASS","TestRecallContext_TightBudget PASS","TestRecallContext_NoProject PASS"],"note":"test caught a subtle bug (dedup collapsing identical items) — fixed by adding unique URLs"}'
    judge_reasoning = "O4 (RecallContext) closed: retrieval orchestrator with token budget. Wraps Store.Recall, runs economy.Atlan, tiered CapTotal heuristic, two-stage truncation. 69/69 green suite."
    reconciled_at   = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffZ")
} | ConvertTo-Json -Depth 4 -Compress
$payload3 | Out-File -FilePath $argsFile3 -Encoding utf8 -NoNewline
& powershell -NoProfile -ExecutionPolicy Bypass -File $wrapper -Tool "dark_research_drift_log" -ArgsFile $argsFile3 -TimeoutMs 15000 2>&1 | Select-Object -First 2
Write-Host "=== O4 (RecallContext) closed ==="